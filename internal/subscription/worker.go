package subscription

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"cheburnet/internal/config"
	"cheburnet/internal/network"
	"cheburnet/pkg/happ"
	"cheburnet/pkg/uri"

	"gopkg.in/yaml.v3"
)

const maxSubscriptionSize = 4 << 20 // 4 MiB лимит

type Worker struct {
	autoHWID      bool
	customHWID    string
	mu            sync.Mutex
	cancelMap     map[string]context.CancelFunc
	client        *http.Client
	directClient  *http.Client
	isEngineAlive func() bool
}

func NewWorker(autoHWID bool, customHWID string, mixedPort int, engineAliveChecker func() bool) *Worker {
	transport := network.NewSmartTransport(8*time.Second, mixedPort, engineAliveChecker)
	directTransport := network.NewBypassTransport(8*time.Second, network.EmergencyDirectMarkInt)

	return &Worker{
		autoHWID:      autoHWID,
		customHWID:    customHWID,
		cancelMap:     make(map[string]context.CancelFunc),
		isEngineAlive: engineAliveChecker,
		client: &http.Client{
			Timeout:   15 * time.Second,
			Transport: transport,
		},
		directClient: &http.Client{
			Timeout:   15 * time.Second,
			Transport: directTransport,
		},
	}
}

func safeHost(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		return u.Host
	}
	return "unknown-host"
}

func parseDurationSafe(intervalStr string) time.Duration {
	clean := strings.ToLower(strings.TrimSpace(intervalStr))
	switch clean {
	case "1m":
		return 1 * time.Minute
	case "2m":
		return 2 * time.Minute
	case "5m":
		return 5 * time.Minute
	case "10m":
		return 10 * time.Minute
	case "30m":
		return 30 * time.Minute
	case "1h", "1":
		return 1 * time.Hour
	case "3h", "3":
		return 3 * time.Hour
	case "6h", "6":
		return 6 * time.Hour
	case "12h", "12":
		return 12 * time.Hour
	case "24h", "24":
		return 24 * time.Hour
	default:
		d, err := time.ParseDuration(clean)
		if err == nil && d >= 1*time.Minute {
			return d
		}
		return 24 * time.Hour
	}
}

func (w *Worker) StartSubscriptionLoops(ctx context.Context, subs []config.SubscriptionConfig, onUpdate func(sub config.SubscriptionConfig)) {
	w.mu.Lock()
	defer w.mu.Unlock()

	for _, cancel := range w.cancelMap {
		cancel()
	}
	w.cancelMap = make(map[string]context.CancelFunc)

	for _, sub := range subs {
		if !sub.Enabled || strings.TrimSpace(sub.URL) == "" {
			continue
		}

		s := sub
		interval := parseDurationSafe(s.UpdateInterval)
		subCtx, subCancel := context.WithCancel(ctx)
		w.cancelMap[s.Name+"|"+s.URL] = subCancel

		log.Printf("[subscription] Registered auto-update loop for %s (interval: %v, host: %s)",
			s.Name, interval, safeHost(s.URL))

		go func(targetSub config.SubscriptionConfig, d time.Duration) {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[subscription] PANIC recovered in auto-update loop for %s: %v", targetSub.Name, r)
				}
			}()

			ticker := time.NewTicker(d)
			defer ticker.Stop()

			for {
				select {
				case <-subCtx.Done():
					return
				case <-ticker.C:
					log.Printf("[subscription] Triggered auto-update for: %s (host: %s, interval: %v)",
						targetSub.Name, safeHost(targetSub.URL), d)
					onUpdate(targetSub)
				}
			}
		}(s, interval)
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
	randBytes := make([]byte, 16)
	_, _ = rand.Read(randBytes)
	return hex.EncodeToString(randBytes)
}

type ClashConfig struct {
	Proxies []struct {
		Name        string `yaml:"name"`
		Type        string `yaml:"type"`
		Server      string `yaml:"server"`
		Port        int    `yaml:"port"`
		UUID        string `yaml:"uuid"`
		Password    string `yaml:"password"`
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

func filterNodesByCompiledRegex(nodes []*config.GenericNode, compiled []*regexp.Regexp, filterMode string) []*config.GenericNode {
	if len(compiled) == 0 {
		return nodes
	}

	isIncludeMode := strings.EqualFold(filterMode, "include")
	filtered := make([]*config.GenericNode, 0, len(nodes))

	for _, node := range nodes {
		matched := false
		for _, re := range compiled {
			if re.MatchString(node.Tag) {
				matched = true
				break
			}
		}

		if isIncludeMode {
			if matched {
				filtered = append(filtered, node)
			}
		} else {
			if !matched {
				filtered = append(filtered, node)
			}
		}
	}
	return filtered
}

func (w *Worker) FetchNodes(ctx context.Context, sub config.SubscriptionConfig) ([]*config.GenericNode, error) {
	reqURL := strings.TrimSpace(sub.URL)
	targetHWID := strings.TrimSpace(sub.HWID)
	if targetHWID == "" && w.autoHWID {
		targetHWID = w.getOrGenerateHWID()
	}

	if happ.IsCrypt4(reqURL) {
		decrypted, err := happ.DecryptCrypt4(reqURL, targetHWID, sub.HWID, "HappDefaultSalt")
		if err != nil {
			return nil, fmt.Errorf("failed to decrypt inline crypt4: %w", err)
		}
		return w.parseContent(decrypted, sub, targetHWID)
	}

	body, err := w.fetchPayload(ctx, sub, targetHWID, w.client)
	if err == nil {
		nodes, parseErr := w.parseContent(body, sub, targetHWID)
		if parseErr == nil && len(nodes) > 0 {
			return nodes, nil
		}
		if parseErr != nil {
			err = parseErr
		}
	}

	host := safeHost(reqURL)
	log.Printf("[subscription] WARN: Primary fetch failed for host %s (%v). Retrying via Direct Bypass...", host, err)
	directBody, directErr := w.fetchPayload(ctx, sub, targetHWID, w.directClient)
	if directErr == nil {
		nodes, parseErr := w.parseContent(directBody, sub, targetHWID)
		if parseErr == nil && len(nodes) > 0 {
			log.Printf("[subscription] INFO: Direct Bypass fetch SUCCESS for host %s (recovered %d nodes)", host, len(nodes))
			return nodes, nil
		}
		directErr = parseErr
	}

	log.Printf("[subscription] ERROR: Direct Bypass fetch also failed for host %s: %v", host, directErr)
	return nil, fmt.Errorf("primary error: %v; direct bypass error: %w", err, directErr)
}

func (w *Worker) fetchPayload(ctx context.Context, sub config.SubscriptionConfig, targetHWID string, httpClient *http.Client) ([]byte, error) {
	reqURL := strings.TrimSpace(sub.URL)
	reqCtx, reqCancel := context.WithTimeout(ctx, 12*time.Second)
	defer reqCancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}

	ua := strings.TrimSpace(sub.UserAgent)
	if ua == "" {
		ua = "Happ/4.3.5"
	}

	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Connection", "close")

	if targetHWID != "" {
		req.Header.Set("x-hwid", targetHWID)
		req.Header.Set("hwid", targetHWID)
		req.Header.Set("X-HWID", targetHWID)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http fetch error: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 512))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("subscription HTTP status: %d", resp.StatusCode)
	}

	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, maxSubscriptionSize+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if len(rawBody) > maxSubscriptionSize {
		return nil, fmt.Errorf("subscription response exceeded maximum size limit of %d bytes", maxSubscriptionSize)
	}

	strBody := strings.TrimSpace(string(rawBody))
	if happ.IsCrypt4(strBody) {
		decrypted, err := happ.DecryptCrypt4(strBody, targetHWID, sub.HWID, "HappDefaultSalt")
		if err != nil {
			return nil, fmt.Errorf("failed to decrypt downloaded crypt4 body: %w", err)
		}
		return decrypted, nil
	}

	return rawBody, nil
}

func (w *Worker) parseContent(body []byte, sub config.SubscriptionConfig, targetHWID string) ([]*config.GenericNode, error) {
	subName := strings.TrimSpace(sub.Name)
	filterMode := sub.FilterMode
	if filterMode == "" {
		filterMode = "exclude"
	}

	if len(sub.CompiledRegex) == 0 && len(sub.ExcludeRegex) > 0 {
		sub.CompileFilters()
	}

	var nodes []*config.GenericNode
	seenTags := make(map[string]bool)

	makeUniqueTag := func(rawName string) string {
		tag := strings.TrimSpace(rawName)
		if tag == "" {
			tag = "node"
		}
		prefix := fmt.Sprintf("[%s] ", subName)
		if subName != "" && !strings.HasPrefix(tag, prefix) {
			tag = prefix + tag
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

	if xrayNodes := parseXrayJSON(body, targetHWID, sub.CompiledRegex, filterMode, subName, seenTags); len(xrayNodes) > 0 {
		nodes = xrayNodes
	} else {
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
					Password:    p.Password,
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
			trimmed := bytes.TrimSpace(body)
			var streamReader io.Reader = bytes.NewReader(trimmed)

			if !bytes.Contains(trimmed, []byte("://")) {
				encodings := []*base64.Encoding{
					base64.StdEncoding,
					base64.RawStdEncoding,
					base64.URLEncoding,
					base64.RawURLEncoding,
				}

				for _, enc := range encodings {
					decodedStream := base64.NewDecoder(enc, bytes.NewReader(trimmed))
					checkBuf := make([]byte, 4096)
					n, err := decodedStream.Read(checkBuf)
					if err == nil && n > 0 && bytes.Contains(checkBuf[:n], []byte("://")) {
						streamReader = io.MultiReader(bytes.NewReader(checkBuf[:n]), decodedStream)
						break
					}
				}
			}

			scanner := bufio.NewScanner(streamReader)
			scanBuf := make([]byte, 32*1024)
			scanner.Buffer(scanBuf, 64*1024)

			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if line == "" || strings.HasPrefix(line, "#") || strings.Contains(line, "не поддерживается") {
					continue
				}

				var linesToProcess []string
				if happ.IsCrypt4(line) {
					if dec, err := happ.DecryptCrypt4(line, targetHWID, sub.HWID, "HappDefaultSalt"); err == nil {
						linesToProcess = strings.Split(string(dec), "\n")
					}
				} else {
					linesToProcess = []string{line}
				}

				for _, rawL := range linesToProcess {
					rawL = strings.TrimSpace(rawL)
					if rawL == "" || strings.HasPrefix(rawL, "#") {
						continue
					}
					node, err := uri.ParseNodeURI(rawL, w.autoHWID, targetHWID)
					if err == nil && node != nil {
						node.Tag = makeUniqueTag(node.Tag)
						nodes = append(nodes, node)
					}
				}
			}
		}

		nodes = filterNodesByCompiledRegex(nodes, sub.CompiledRegex, filterMode)
	}

	if len(nodes) == 0 {
		return nil, fmt.Errorf("панель не вернула серверов либо все были отфильтрованы правилом regex (%s)", filterMode)
	}

	return nodes, nil
}

func parseXrayJSON(data []byte, targetHWID string, compiled []*regexp.Regexp, filterMode string, subName string, seenTags map[string]bool) []*config.GenericNode {
	var profiles []xrayProfileItem
	if err := json.Unmarshal(data, &profiles); err != nil {
		var single xrayProfileItem
		if errSingle := json.Unmarshal(data, &single); errSingle == nil {
			profiles = append(profiles, single)
		} else {
			return nil
		}
	}

	isIncludeMode := strings.EqualFold(filterMode, "include")
	var nodes []*config.GenericNode

	makeUniqueTag := func(rawName string) string {
		tag := strings.TrimSpace(rawName)
		if tag == "" {
			tag = "node"
		}
		prefix := fmt.Sprintf("[%s] ", subName)
		if subName != "" && !strings.HasPrefix(tag, prefix) {
			tag = prefix + tag
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
			proto := strings.ToLower(ob.Protocol)
			if proto != "vless" && proto != "hysteria" && proto != "hysteria2" && proto != "shadowsocks" && proto != "trojan" {
				continue
			}

			rawTag := ob.Tag
			if baseRemarks != "" && !strings.Contains(baseRemarks, "Автовыбор") {
				rawTag = fmt.Sprintf("%s (%s)", baseRemarks, ob.Tag)
			}

			if len(compiled) > 0 {
				matched := false
				for _, re := range compiled {
					if re.MatchString(rawTag) || (baseRemarks != "" && re.MatchString(baseRemarks)) || re.MatchString(ob.Tag) {
						matched = true
						break
					}
				}

				if isIncludeMode {
					if !matched {
						continue
					}
				} else {
					if matched {
						continue
					}
				}
			}

			uniqueTag := makeUniqueTag(rawTag)

			node := &config.GenericNode{
				Tag:      uniqueTag,
				Protocol: proto,
				HWID:     targetHWID,
			}

			if proto == "vless" {
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

					// 1. Разбор XHTTP (SplitHTTP) - включая секретный path, host, mode и extra
					if xhttp, ok := ss["xhttpSettings"].(map[string]interface{}); ok {
						if p, ok := xhttp["path"].(string); ok && p != "" {
							node.Path = p
						}
						if h, ok := xhttp["host"].(string); ok && h != "" {
							node.Host = h
						}
						if m, ok := xhttp["mode"].(string); ok && m != "" {
							node.XHTTPMode = m
						}
						if extra, ok := xhttp["extra"].(map[string]interface{}); ok {
							if pad, ok := extra["xPaddingBytes"].(string); ok && pad != "" {
								node.XHTTPPadding = pad
							}
							if noSSE, ok := extra["noSSEHeader"].(bool); ok && noSSE {
								node.XHTTPNoGRPC = true
							}
						}
					}

					// 2. Разбор gRPC
					if grpc, ok := ss["grpcSettings"].(map[string]interface{}); ok {
						if sName, ok := grpc["serviceName"].(string); ok && sName != "" {
							node.Path = sName
						}
					}

					// 3. Разбор WebSocket
					if ws, ok := ss["wsSettings"].(map[string]interface{}); ok {
						if p, ok := ws["path"].(string); ok && p != "" {
							node.Path = p
						}
						if h, ok := ws["headers"].(map[string]interface{}); ok {
							if hostH, ok := h["Host"].(string); ok {
								node.Host = hostH
							}
						}
					}

					// 4. Разбор Reality
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
						if spx, ok := reality["spiderX"].(string); ok && node.Path == "" {
							node.Path = spx
						}
					}

					// 5. Разбор стандартного TLS
					if tls, ok := ss["tlsSettings"].(map[string]interface{}); ok {
						if sni, ok := tls["serverName"].(string); ok {
							node.SNI = sni
						}
						if fp, ok := tls["fingerprint"].(string); ok {
							node.Fingerprint = fp
						}
					}
				}
			}

			if proto == "hysteria" || proto == "hysteria2" {
				node.Protocol = "hysteria2"
				node.Security = "tls"
				if addr, ok := ob.Settings["address"].(string); ok {
					node.Address = addr
				}
				if port, ok := ob.Settings["port"].(float64); ok {
					node.Port = int(port)
				}

				if ss := ob.StreamSettings; ss != nil {
					if hys, ok := ss["hysteriaSettings"].(map[string]interface{}); ok {
						if auth, ok := hys["auth"].(string); ok {
							node.Password = auth
							node.UUID = auth
						}
					}
					if tls, ok := ss["tlsSettings"].(map[string]interface{}); ok {
						if sni, ok := tls["serverName"].(string); ok {
							node.SNI = sni
						}
						if fp, ok := tls["fingerprint"].(string); ok {
							node.Fingerprint = fp
						}
					}
				}
			}

			if proto == "trojan" {
				node.Security = "tls"
				if servers, ok := ob.Settings["servers"].([]interface{}); ok && len(servers) > 0 {
					if srv, ok := servers[0].(map[string]interface{}); ok {
						if addr, ok := srv["address"].(string); ok {
							node.Address = addr
						}
						if port, ok := srv["port"].(float64); ok {
							node.Port = int(port)
						}
						if pwd, ok := srv["password"].(string); ok {
							node.Password = pwd
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
