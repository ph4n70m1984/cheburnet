package subscription

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"cheburnet/internal/config"
	"cheburnet/pkg/uri"

	"gopkg.in/yaml.v3"
)

type Worker struct {
	autoHWID   bool
	customHWID string
}

func NewWorker(autoHWID bool, customHWID string) *Worker {
	return &Worker{
		autoHWID:   autoHWID,
		customHWID: customHWID,
	}
}

func (w *Worker) getOrGenerateHWID() string {
	if w.customHWID != "" {
		return w.customHWID
	}
	if !w.autoHWID {
		return ""
	}
	for _, iface := range []string{"br-lan", "eth0", "lan"} {
		if mac, err := os.ReadFile("/sys/class/net/" + iface + "/address"); err == nil {
			cleaned := strings.TrimSpace(string(mac))
			if cleaned != "" {
				hash := sha256.Sum256([]byte(cleaned))
				return hex.EncodeToString(hash[:16])
			}
		}
	}
	return "00112233445566778899aabbccddeeff"
}

type ClashConfig struct {
	Proxies []struct {
		Name        string `yaml:"name"`
		Type        string `yaml:"type"`
		Server      string `yaml:"server"`
		Port        int    `yaml:"port"`
		UUID        string `yaml:"uuid"`
		Network     string `yaml:"network"`
		Flow        string `yaml:"flow"`
		TLS         bool   `yaml:"tls"`
		Servername  string `yaml:"servername"`
		RealityOpts struct {
			PublicKey string `yaml:"public-key"`
			ShortID   string `yaml:"short-id"`
		} `yaml:"reality-opts"`
		ClientFingerprint string `yaml:"client-fingerprint"`
	} `yaml:"proxies"`
}

func (w *Worker) FetchNodes(ctx context.Context, sub config.SubscriptionConfig) ([]*config.GenericNode, error) {
	reqURL := strings.TrimSpace(sub.URL)

	// Приоритет: 1) HWID конкретной подписки -> 2) глобальный авто HWID
	targetHWID := strings.TrimSpace(sub.HWID)
	if targetHWID == "" && w.autoHWID {
		targetHWID = w.getOrGenerateHWID()
	}

	// Изолируем контекст запроса от верхнего ctx
	reqCtx, reqCancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer reqCancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}

	ua := strings.TrimSpace(sub.UserAgent)
	if ua == "" {
		ua = "Happ/4.1.3 (iPhone; iOS 17.5.1; Scale/3.00)"
	}

	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "*/*")

	// Передаем точный заголовок, аналогично рабочему curl
	if targetHWID != "" {
		req.Header.Set("x-hwid", targetHWID)
	}

	// Резолвер напрямую через публичный DNS во избежание проблем с локальным 53 портом
	dialer := &net.Dialer{
		Timeout:   6 * time.Second,
		KeepAlive: 0,
		Resolver: &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				d := net.Dialer{Timeout: 3 * time.Second}
				conn, err := d.DialContext(ctx, "udp", "77.88.8.8:53")
				if err != nil {
					return d.DialContext(ctx, "udp", "8.8.8.8:53")
				}
				return conn, nil
			},
		},
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			ResponseHeaderTimeout: 8 * time.Second,
			DisableKeepAlives:     true,
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http fetch error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("subscription HTTP status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	content := string(body)
	var nodes []*config.GenericNode

	// 1. Попытка распарсить как Clash YAML
	var clashCfg ClashConfig
	if err := yaml.Unmarshal(body, &clashCfg); err == nil && len(clashCfg.Proxies) > 0 {
		for _, p := range clashCfg.Proxies {
			if strings.Contains(p.Name, "не поддерживается") || strings.Contains(p.Name, "not supported") {
				continue
			}

			sec := "none"
			if p.TLS {
				sec = "tls"
			}
			if p.RealityOpts.PublicKey != "" {
				sec = "reality"
			}

			node := &config.GenericNode{
				Tag:         p.Name,
				Address:     p.Server,
				Port:        p.Port,
				Protocol:    p.Type,
				UUID:        p.UUID,
				Flow:        p.Flow,
				Network:     p.Network,
				Security:    sec,
				SNI:         p.Servername,
				Fingerprint: p.ClientFingerprint,
				PublicKey:   p.RealityOpts.PublicKey,
				ShortID:     p.RealityOpts.ShortID,
				HWID:        targetHWID,
			}
			nodes = append(nodes, node)
		}

		if len(nodes) > 0 {
			return nodes, nil
		}
	}

	// 2. Base64
	if dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(content)); err == nil {
		content = string(dec)
	} else if decURL, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(content)); err == nil {
		content = string(decURL)
	}

	// 3. Plaintext построчно
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.Contains(line, "не поддерживается") {
			continue
		}

		node, err := uri.ParseNodeURI(line, w.autoHWID, targetHWID)
		if err == nil {
			nodes = append(nodes, node)
		}
	}

	if len(nodes) == 0 {
		return nil, fmt.Errorf("панель не вернула рабочих серверов")
	}

	return nodes, nil
}
