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

// VerifyEngineAlive проверяет локальную жизнеспособность sing-box:
// 1. TCP-сокет входящего смешанного прокси-порта (cfg.MixedPort, по умолчанию 4534).
// 2. Локальный порт Clash API ядра (:9090) в качестве fallback-проверки.
func VerifyEngineAlive(ctx context.Context, cfg *config.CheburConfig) error {
	d := net.Dialer{Timeout: 800 * time.Millisecond}

	mixedPort := 4534
	if cfg != nil && cfg.MixedPort > 0 {
		mixedPort = cfg.MixedPort
	}

	// 1. Проверяем входящий смешанный порт sing-box
	conn, err := d.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", mixedPort))
	if err == nil {
		_ = conn.Close()
		return nil
	}

	// 2. Фоллбек: опрашиваем порт Clash API (:9090) самого sing-box
	clashConn, clashErr := d.DialContext(ctx, "tcp", "127.0.0.1:9090")
	if clashErr == nil {
		_ = clashConn.Close()
		return nil
	}

	return fmt.Errorf("sing-box is down: mixed inbound (:%d) unreachable (%v), clash api (:9090) unreachable (%v)", mixedPort, err, clashErr)
}

// VerifyTraffic выполняет полный сквозной E2E-тест генерации 204 через исходящий прокси sing-box
func VerifyTraffic(ctx context.Context, cfg *config.CheburConfig) error {
	if err := VerifyEngineAlive(ctx, cfg); err != nil {
		return err
	}

	proxyPort := 4534
	if cfg != nil && cfg.MixedPort > 0 {
		proxyPort = cfg.MixedPort
	}

	proxyURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", proxyPort))

	client := &http.Client{
		Transport: &http.Transport{
			Proxy:             http.ProxyURL(proxyURL),
			DisableKeepAlives: true,
		},
		Timeout: 3500 * time.Millisecond,
	}

	// Используем стабильный Cloudflare 204 вместо подверженного блокировкам gstatic
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://cp.cloudflare.com/generate_204", nil)
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
