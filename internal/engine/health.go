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

// VerifyEngineAlive проверяет только локальную работоспособность (процесс не завис, порты подняты)
func VerifyEngineAlive(ctx context.Context, cfg *config.CheburConfig) error {
	dialer := &net.Dialer{Timeout: 1 * time.Second}

	// Проверяем Inbound порт прокси
	proxyTarget := fmt.Sprintf("127.0.0.1:%d", cfg.MixedPort)
	if cfg.MixedPort == 0 {
		proxyTarget = "127.0.0.1:4534"
	}
	conn, err := dialer.DialContext(ctx, "tcp", proxyTarget)
	if err != nil {
		return fmt.Errorf("local proxy inbound unreachable: %w", err)
	}
	_ = conn.Close()

	// Проверяем локальный DNS-inbound
	dnsTarget := fmt.Sprintf("127.0.0.42:%d", cfg.DNSPort)
	if cfg.DNSPort == 0 {
		dnsTarget = "127.0.0.42:53"
	}
	connDNS, err := dialer.DialContext(ctx, "udp", dnsTarget)
	if err != nil {
		return fmt.Errorf("local dns inbound unreachable: %w", err)
	}
	_ = connDNS.Close()

	return nil
}

// VerifyTraffic делает полный E2E прогон через работающее ядро
func VerifyTraffic(ctx context.Context, cfg *config.CheburConfig) error {
	// Сначала проверяем, что ядро вообще живо локально
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
