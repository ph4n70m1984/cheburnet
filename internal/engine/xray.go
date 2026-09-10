package engine

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cheburnet/internal/config"
	"cheburnet/internal/engine/xray"
)

const (
	XrayAssetDir       = "/usr/share/xray"
	GeositePath        = "/usr/share/xray/geosite.dat"
	GeositeDownloadURL = "https://github.com/itdoginfo/allow-domains/releases/latest/download/geosite.dat"
)

type XrayEngine struct {
	builder *xray.Builder
	cmd     *exec.Cmd
	cfg     *config.CheburConfig
	mu      sync.Mutex
}

func NewXrayEngine() *XrayEngine {
	return &XrayEngine{
		builder: xray.NewBuilder(),
	}
}

func (x *XrayEngine) Name() string {
	return "xray"
}

// moveFileCrossDevice безопасно перемещает файл, выполняя fallback на потоковое копирование при ошибке EXDEV
func moveFileCrossDevice(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}

	_ = os.Remove(src)
	return nil
}

// EnsureAssets проверяет наличие /usr/share/xray/geosite.dat и при отсутствии
// скачивает его, резолвя адрес через dns_server и dns_protocol из UCI-конфига
func (x *XrayEngine) EnsureAssets(ctx context.Context) error {
	if _, err := os.Stat(GeositePath); err == nil {
		return nil
	}

	log.Printf("[INFO] %s not found. Downloading asset from %s...", GeositePath, GeositeDownloadURL)
	if err := os.MkdirAll(XrayAssetDir, 0755); err != nil {
		return fmt.Errorf("failed to create asset directory %s: %w", XrayAssetDir, err)
	}

	// Создаем временный файл в той же файловой системе для исключения межфайловых коллизий
	tmpFile := filepath.Join(XrayAssetDir, "geosite.dat.tmp")
	defer os.Remove(tmpFile)

	x.mu.Lock()
	var (
		dnsAddr  string
		dnsProto string
	)
	if x.cfg != nil {
		dnsAddr = x.cfg.DNSServer
		dnsProto = strings.ToLower(strings.TrimSpace(x.cfg.DNSProtocol))
	}
	x.mu.Unlock()

	if dnsAddr == "" || dnsAddr == "127.0.0.1" || dnsAddr == "::1" || dnsAddr == "localhost" {
		dnsAddr = "8.8.8.8"
	}
	if dnsProto == "" {
		dnsProto = "udp"
	}

	if idx := strings.Index(dnsAddr, "://"); idx != -1 {
		dnsAddr = dnsAddr[idx+3:]
	}
	if idx := strings.Index(dnsAddr, "/"); idx != -1 {
		dnsAddr = dnsAddr[:idx]
	}

	netProto := "udp4"
	targetPort := "53"

	switch dnsProto {
	case "tcp":
		netProto = "tcp4"
	case "dot", "tls":
		netProto = "tcp4"
		targetPort = "853"
	default:
		netProto = "udp4"
		targetPort = "53"
	}

	if !strings.Contains(dnsAddr, ":") {
		dnsAddr = net.JoinHostPort(dnsAddr, targetPort)
	}

	dialer := &net.Dialer{
		Timeout: 5 * time.Second,
		Resolver: &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				d := net.Dialer{Timeout: 3 * time.Second}
				return d.DialContext(ctx, netProto, dnsAddr)
			},
		},
	}

	transport := &http.Transport{
		DialContext:         dialer.DialContext,
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 10 * time.Second,
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   90 * time.Second,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, GeositeDownloadURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create asset request: %w", err)
	}
	req.Header.Set("User-Agent", "CheburNet-XrayAssetDownloader")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download geosite.dat using DNS %s (%s): %w", dnsAddr, netProto, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download geosite.dat: HTTP %d", resp.StatusCode)
	}

	out, err := os.OpenFile(tmpFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}

	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		return fmt.Errorf("failed to write geosite.dat: %w", err)
	}
	out.Close()

	if err := moveFileCrossDevice(tmpFile, GeositePath); err != nil {
		return fmt.Errorf("failed to place geosite.dat into %s: %w", GeositePath, err)
	}

	log.Printf("[INFO] Successfully installed %s via DNS %s (%s)", GeositePath, dnsAddr, netProto)
	return nil
}

func (x *XrayEngine) BuildConfig(cfg *config.CheburConfig, targetPath string) error {
	x.mu.Lock()
	x.cfg = cfg
	x.mu.Unlock()
	return x.builder.Build(cfg, targetPath)
}

func (x *XrayEngine) ValidateConfig(configPath string) error {
	cmd := exec.Command("xray", "run", "-test", "-c", configPath)
	cmd.Env = append(os.Environ(), "XRAY_LOCATION_ASSET="+XrayAssetDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("xray -test failed: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (x *XrayEngine) Start(ctx context.Context, configPath string) error {
	x.mu.Lock()
	defer x.mu.Unlock()

	if err := x.EnsureAssets(ctx); err != nil {
		return fmt.Errorf("assets check failed: %w", err)
	}

	x.cmd = NewIsolatedCmd(ctx, "xray", "run", "-c", configPath)
	x.cmd.Env = append(x.cmd.Env, "XRAY_LOCATION_ASSET="+XrayAssetDir)

	return x.cmd.Start()
}

func (x *XrayEngine) Stop() error {
	x.mu.Lock()
	defer x.mu.Unlock()

	if x.cmd != nil {
		err := TerminateCmd(x.cmd)
		x.cmd = nil
		if err != nil {
			return fmt.Errorf("failed to safely terminate xray: %w", err)
		}
	}
	return nil
}

func (x *XrayEngine) CollectMetrics(ctx context.Context) (*UnifiedMetrics, error) {
	metrics := &UnifiedMetrics{
		NodeLatencies: make(map[string]int64),
	}

	x.mu.Lock()
	cfg := x.cfg
	x.mu.Unlock()

	if cfg == nil {
		return metrics, nil
	}

	for _, node := range cfg.Nodes {
		target := net.JoinHostPort(node.Address, fmt.Sprintf("%d", node.Port))
		d := net.Dialer{Timeout: 1500 * time.Millisecond}

		start := time.Now()
		conn, err := d.DialContext(ctx, "tcp", target)
		if err == nil {
			_ = conn.Close()
			metrics.NodeLatencies[node.Tag] = time.Since(start).Milliseconds()
		} else {
			metrics.NodeLatencies[node.Tag] = 0
		}
	}

	return metrics, nil
}
