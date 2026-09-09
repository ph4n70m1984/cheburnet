package engine

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"cheburnet/internal/config"
)

// VerifyEngineAlive проверяет локальную жизнеспособность ядра:
// 1. TCP-сокет смешанного прокси-порта (mixed/http/socks inbound).
// 2. Реальный DNS-запрос через локальный UDP-вход ядра (127.0.0.42:53).
func VerifyEngineAlive(ctx context.Context, cfg *config.CheburConfig) error {
	dialer := &net.Dialer{Timeout: 1 * time.Second}

	// 1. Проверяем TCP-порт прокси
	proxyTarget := fmt.Sprintf("127.0.0.1:%d", cfg.MixedPort)
	if cfg.MixedPort == 0 {
		proxyTarget = "127.0.0.1:4534"
	}
	conn, err := dialer.DialContext(ctx, "tcp", proxyTarget)
	if err != nil {
		return fmt.Errorf("local proxy inbound unreachable: %w", err)
	}
	_ = conn.Close()

	// 2. Проверяем локальный DNS через реальный запрос A-записи
	dnsTarget := fmt.Sprintf("127.0.0.42:%d", cfg.DNSPort)
	if cfg.DNSPort == 0 {
		dnsTarget = "127.0.0.42:53"
	}

	r := &net.Resolver{
		PreferGo: true,
		Dial: func(dialCtx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{Timeout: 1500 * time.Millisecond}
			return d.DialContext(dialCtx, "udp", dnsTarget)
		},
	}

	lookupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	addrs, err := r.LookupHost(lookupCtx, "example.com")
	if err != nil || len(addrs) == 0 {
		return fmt.Errorf("local dns inbound (%s) query failed: %w", dnsTarget, err)
	}

	return nil
}

// VerifyTraffic выполняет полный сквозной E2E-тест генерации 204 через исходящий прокси
func VerifyTraffic(ctx context.Context, cfg *config.CheburConfig) error {
	if err := VerifyEngineAlive(ctx, cfg); err != nil {
		return err
	}

	proxyPort := cfg.MixedPort
	if proxyPort == 0 {
		proxyPort = 4534
	}
	proxyURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", proxyPort))

	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		},
		Timeout: 4 * time.Second,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.gstatic.com/generate_204", nil)
	if err != nil {
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("traffic test failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status from traffic test: %d", resp.StatusCode)
	}

	return nil
}
