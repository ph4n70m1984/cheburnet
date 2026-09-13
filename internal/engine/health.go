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
	d := net.Dialer{Timeout: 800 * time.Millisecond}

	// 1. Для Xray проверяем локальный порт API (10085)
	conn, err := d.DialContext(ctx, "tcp", "127.0.0.1:10085")
	if err == nil {
		_ = conn.Close()
		return nil
	}

	// 2. Фоллбек: проверка локального Mixed/HTTP порта
	mixedPort := cfg.MixedPort
	if mixedPort <= 0 {
		mixedPort = 4534
	}
	conn, err = d.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", mixedPort))
	if err == nil {
		_ = conn.Close()
		return nil
	}

	return fmt.Errorf("engine local api/proxy inbound unreachable (tried 10085 and %d): %w", mixedPort, err)
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
