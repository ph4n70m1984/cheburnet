package subscription

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
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

type xrayProfileItem struct {
	Remarks   string             `json:"remarks"`
	Outbounds []xrayOutboundItem `json:"outbounds"`
}

type xrayOutboundItem struct {
	Tag            string                 `json:"tag"`
	Protocol       string                 `json:"protocol"`
	Settings       map[string]interface{} `json:"settings"`
	StreamSettings map[string]interface{} `json:"streamSettings"`
}

func filterNodesByRegex(nodes []*config.GenericNode, patterns []string) []*config.GenericNode {
	if len(patterns) == 0 {
		return nodes
	}

	var compiled []*regexp.Regexp
	for _, p := range patterns {
		p = strings.TrimSpace(strings.Trim(p, "'\""))
		if p == "" {
			continue
		}
		if re, err := regexp.Compile("(?i)" + p); err == nil {
			compiled = append(compiled, re)
		}
	}

	if len(compiled) == 0 {
		return nodes
	}

	filtered := make([]*config.GenericNode, 0, len(nodes))
	for _, node := range nodes {
		exclude := false
		for _, re := range compiled {
			if re.MatchString(node.Tag) {
				exclude = true
				break
			}
		}
		if !exclude {
			filtered = append(filtered, node)
		}
	}
	return filtered
}

func (w *Worker) FetchNodes(ctx context.Context, sub config.SubscriptionConfig) ([]*config.GenericNode, error) {
	reqURL := strings.TrimSpace(sub.URL)
	subName := strings.TrimSpace(sub.Name)

	targetHWID := strings.TrimSpace(sub.HWID)
	if targetHWID == "" && w.autoHWID {
		targetHWID = w.getOrGenerateHWID()
	}

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
	req.Header.Set("Connection", "keep-alive")

	if targetHWID != "" {
		req.Header.Set("x-hwid", targetHWID)
		req.Header.Set("hwid", targetHWID)
		req.Header.Set("X-HWID", targetHWID)
	}

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

	var nodes []*config.GenericNode
	seenTags := make(map[string]bool)

	// Вспомогательная функция для генерации уникального тега с учетом имени провайдера
	makeUniqueTag := func(rawName string) string {
		tag := strings.TrimSpace(rawName)
		if tag == "" {
			tag = "node"
		}
		if subName != "" && !strings.HasPrefix(tag, subName+" ") {
			tag = fmt.Sprintf("[%s] %s", subName, tag)
		}
		base := tag
		counter := 1
		for seenTags[tag] {
			counter++
			tag = fmt.Sprintf("%s (%d)", base, counter)
		}
		seenTags[tag] = true
		return tag
	}

	// 1. Попытка распарсить как Xray JSON массив профилей (Remnawave/Happ)
	if xrayNodes := parseXrayJSON(body, targetHWID, sub.ExcludeRegex, subName, seenTags); len(xrayNodes) > 0 {
		nodes = xrayNodes
	} else {
		// 2. Попытка распарсить как Clash YAML
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

				uniqueTag := makeUniqueTag(p.Name)

				node := &config.GenericNode{
					Tag:         uniqueTag,
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
		} else {
			// 3. Base64
			content := string(body)
			trimmed := strings.TrimSpace(content)

			if !strings.Contains(trimmed, "://") {
				if dec, err := base64.StdEncoding.DecodeString(trimmed); err == nil {
					content = string(dec)
				} else if decURL, err := base64.RawURLEncoding.DecodeString(trimmed); err == nil {
					content = string(decURL)
				} else if decRaw, err := base64.RawStdEncoding.DecodeString(trimmed); err == nil {
					content = string(decRaw)
				}
			}

			// 4. Plaintext построчно
			scanner := bufio.NewScanner(strings.NewReader(content))
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if line == "" || strings.HasPrefix(line, "#") || strings.Contains(line, "не поддерживается") {
					continue
				}

				node, err := uri.ParseNodeURI(line, w.autoHWID, targetHWID)
				if err == nil && node != nil {
					node.Tag = makeUniqueTag(node.Tag)
					nodes = append(nodes, node)
				}
			}
		}

		// Дополнительная фильтрация нод по регулярным выражениям для форматов Clash/Plaintext
		nodes = filterNodesByRegex(nodes, sub.ExcludeRegex)
	}

	if len(nodes) == 0 {
		return nil, fmt.Errorf("панель не вернула серверов либо все были отфильтрованы правилом exclude_regex")
	}

	return nodes, nil
}

func parseXrayJSON(data []byte, targetHWID string, excludeRegexes []string, subName string, seenTags map[string]bool) []*config.GenericNode {
	var profiles []xrayProfileItem
	if err := json.Unmarshal(data, &profiles); err != nil {
		var single xrayProfileItem
		if errSingle := json.Unmarshal(data, &single); errSingle == nil {
			profiles = append(profiles, single)
		} else {
			return nil
		}
	}

	var compiled []*regexp.Regexp
	for _, p := range excludeRegexes {
		p = strings.TrimSpace(strings.Trim(p, "'\""))
		if p == "" {
			continue
		}
		if re, err := regexp.Compile("(?i)" + p); err == nil {
			compiled = append(compiled, re)
		}
	}

	var nodes []*config.GenericNode

	makeUniqueTag := func(rawName string) string {
		tag := strings.TrimSpace(rawName)
		if tag == "" {
			tag = "node"
		}
		if subName != "" && !strings.HasPrefix(tag, subName+" ") {
			tag = fmt.Sprintf("[%s] %s", subName, tag)
		}
		base := tag
		counter := 1
		for seenTags[tag] {
			counter++
			tag = fmt.Sprintf("%s (%d)", base, counter)
		}
		seenTags[tag] = true
		return tag
	}

	for _, prof := range profiles {
		baseRemarks := strings.TrimSpace(prof.Remarks)

		for _, ob := range prof.Outbounds {
			if ob.Protocol != "vless" && ob.Protocol != "hysteria2" && ob.Protocol != "shadowsocks" && ob.Protocol != "trojan" {
				continue
			}

			rawTag := ob.Tag
			if baseRemarks != "" && !strings.Contains(baseRemarks, "Автовыбор") {
				rawTag = fmt.Sprintf("%s (%s)", baseRemarks, ob.Tag)
			}

			// Проверка совпадений с регулярными выражениями
			excluded := false
			for _, re := range compiled {
				if re.MatchString(rawTag) || (baseRemarks != "" && re.MatchString(baseRemarks)) || re.MatchString(ob.Tag) {
					excluded = true
					break
				}
			}
			if excluded {
				continue
			}

			uniqueTag := makeUniqueTag(rawTag)

			node := &config.GenericNode{
				Tag:      uniqueTag,
				Protocol: ob.Protocol,
				HWID:     targetHWID,
			}

			if ob.Protocol == "vless" {
				if vnext, ok := ob.Settings["vnext"].([]interface{}); ok && len(vnext) > 0 {
					if firstTarget, ok := vnext[0].(map[string]interface{}); ok {
						if addr, ok := firstTarget["address"].(string); ok {
							node.Address = addr
						}
						if port, ok := firstTarget["port"].(float64); ok {
							node.Port = int(port)
						}
						if users, ok := firstTarget["users"].([]interface{}); ok && len(users) > 0 {
							if u, ok := users[0].(map[string]interface{}); ok {
								if id, ok := u["id"].(string); ok {
									node.UUID = id
								}
								if flow, ok := u["flow"].(string); ok {
									node.Flow = flow
								}
							}
						}
					}
				}

				if ss := ob.StreamSettings; ss != nil {
					if netType, ok := ss["network"].(string); ok {
						node.Network = netType
					}
					if sec, ok := ss["security"].(string); ok {
						node.Security = sec
					}
					if reality, ok := ss["realitySettings"].(map[string]interface{}); ok {
						if sni, ok := reality["serverName"].(string); ok {
							node.SNI = sni
						}
						if pbk, ok := reality["publicKey"].(string); ok {
							node.PublicKey = pbk
						}
						if sid, ok := reality["shortId"].(string); ok {
							node.ShortID = sid
						}
						if fp, ok := reality["fingerprint"].(string); ok {
							node.Fingerprint = fp
						}
					}
				}
			}

			if node.Address != "" && node.Port > 0 {
				nodes = append(nodes, node)
			}
		}
	}

	return nodes
}
