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
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"cheburnet/internal/config"
	"cheburnet/internal/network"
	"cheburnet/pkg/happ"
	"cheburnet/pkg/uri"

	"gopkg.in/yaml.v3"
)

// maxSubscriptionSize снижен до 4 MiB: защита от OOM на роутерах со 128 МБ RAM
const maxSubscriptionSize = 4 << 20

// EmergencyDirectMark — отдельная метка для аварийного запроса подписок напрямую
const EmergencyDirectMark = 0x00300000

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

	// Используем каноничную метку network.EmergencyDirectMarkInt
	directTransport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: 8 * time.Second,
			Control: func(networkProto, address string, c syscall.RawConn) error {
				return c.Control(func(fd uintptr) {
					_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, network.EmergencyDirectMarkInt)
				})
			},
		}).DialContext,
		ResponseHeaderTimeout: 8 * time.Second,
		DisableKeepAlives:     true,
	}

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
		// Ключ включает имя и URL для исключения конфликтов коллизий
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
	// Генерация уникального псевдослучайного HWID вместо хардкода
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

	// 1. Статические happ://crypt4/ парсятся офлайн
	if happ.IsCrypt4(reqURL) {
		decrypted, err := happ.DecryptCrypt4(reqURL, targetHWID, sub.HWID, "HappDefaultSalt")
		if err != nil {
			return nil, fmt.Errorf("failed to decrypt inline crypt4: %w", err)
		}
		return w.parseContent(decrypted, sub, targetHWID)
	}

	// 2. Первая попытка через основной клиент (SmartTransport)
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

	// 3. Безусловный фоллбэк на прямой аварийный клиент
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
	defer resp.Body.Close()

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

	// 1. Попытка распарсить как Xray JSON[cite: 7]
	if xrayNodes := parseXrayJSON(body, targetHWID, sub.CompiledRegex, filterMode, subName, seenTags); len(xrayNodes) > 0 {
		nodes = xrayNodes
	} else {
		// 2. Попытка распарсить как Clash YAML[cite: 7]
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
			// 3. Потоковое Base64 декодирование и Plaintext URI парсинг без раздувания кучи
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

			// 4. Построчный потоковый парсинг URI
			scanner := bufio.NewScanner(streamReader)
			scanBuf := make([]byte, 32*1024)
			scanner.Buffer(scanBuf, 64*1024)

			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if line == "" || strings.HasPrefix(line, "#") || strings.Contains(line, "не поддерживается") {
					continue
				}

				if happ.IsCrypt4(line) {
					if dec, err := happ.DecryptCrypt4(line, targetHWID, sub.HWID, "HappDefaultSalt"); err == nil {
						line = string(dec)
					}
				}

				node, err := uri.ParseNodeURI(line, w.autoHWID, targetHWID)
				if err == nil && node != nil {
					node.Tag = makeUniqueTag(node.Tag)
					nodes = append(nodes, node)
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
						if spx, ok := reality["spiderX"].(string); ok {
							node.Path = spx
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
