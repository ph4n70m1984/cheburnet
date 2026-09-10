package xray

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"cheburnet/internal/config"
	"cheburnet/internal/network"
)

type Builder struct {
	rulesLoader *network.CompressedRulesetLoader
}

func NewBuilder() *Builder {
	return &Builder{
		rulesLoader: network.NewCompressedRulesetLoader(),
	}
}

func (b *Builder) EnsureAssets(ctx context.Context) error {
	assetDir := os.Getenv("XRAY_LOCATION_ASSET")
	if assetDir == "" {
		assetDir = "/usr/share/xray"
	}

	geositePath := filepath.Join(assetDir, "geosite.dat")
	if _, err := os.Stat(geositePath); err == nil {
		return nil
	}

	if err := os.MkdirAll(assetDir, 0755); err != nil {
		return fmt.Errorf("failed to create asset dir: %w", err)
	}

	url := "https://github.com/v2fly/domain-list-community/releases/latest/download/dlc.dat"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create download request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download geosite.dat failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download geosite.dat returned status: %d", resp.StatusCode)
	}

	out, err := os.Create(geositePath)
	if err != nil {
		return fmt.Errorf("create geosite.dat file: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, resp.Body); err != nil {
		return fmt.Errorf("save geosite.dat failed: %w", err)
	}

	return nil
}

func (b *Builder) ValidateConfig(configPath string) error {
	cmd := exec.Command("xray", "run", "-test", "-c", configPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("xray test failed: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func parsePortsForXray(rawPorts []string) string {
	var formatted []string
	for _, p := range rawPorts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}

		if strings.Contains(p, ":") || strings.Contains(p, "-") {
			normalized := strings.ReplaceAll(p, ":", "-")
			parts := strings.Split(normalized, "-")
			if len(parts) == 2 {
				start, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
				end, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
				if err1 == nil && err2 == nil && start > 0 && end <= 65535 && start <= end {
					formatted = append(formatted, fmt.Sprintf("%d-%d", start, end))
				}
			}
			continue
		}

		if val, err := strconv.Atoi(p); err == nil && val > 0 && val <= 65535 {
			formatted = append(formatted, strconv.Itoa(val))
		}
	}
	return strings.Join(formatted, ",")
}

func mapRuleSetToXrayGeosite(rs string) string {
	rs = strings.ToLower(strings.TrimSpace(rs))
	if rs == "" {
		return ""
	}
	if strings.HasPrefix(rs, "geosite:") {
		rs = strings.TrimPrefix(rs, "geosite:")
	}
	rs = strings.ReplaceAll(rs, "_", "-")
	return "geosite:" + rs
}

func resolveTargetToCIDR(target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}

	if strings.Contains(target, ":") && !strings.Contains(target, ".") {
		file, err := os.Open("/tmp/dhcp.leases")
		if err == nil {
			defer file.Close()
			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				fields := strings.Fields(scanner.Text())
				if len(fields) >= 3 {
					if strings.EqualFold(fields[1], target) {
						target = fields[2]
						break
					}
				}
			}
		}
	}

	if !strings.Contains(target, "/") && net.ParseIP(target) != nil {
		return target + "/32"
	}

	return target
}

func formatDNSServerForXray(rawAddr, protocol string) string {
	rawAddr = strings.TrimSpace(rawAddr)
	if rawAddr == "" {
		return ""
	}

	protocol = strings.ToLower(strings.TrimSpace(protocol))

	switch protocol {
	case "doh", "https":
		if !strings.HasPrefix(rawAddr, "https://") && !strings.HasPrefix(rawAddr, "http://") {
			return fmt.Sprintf("https://%s/dns-query", rawAddr)
		}
		return rawAddr

	case "dot", "tls":
		cleanAddr := rawAddr
		if idx := strings.Index(cleanAddr, "://"); idx != -1 {
			cleanAddr = cleanAddr[idx+3:]
		}
		if _, _, err := net.SplitHostPort(cleanAddr); err != nil {
			cleanAddr = net.JoinHostPort(cleanAddr, "853")
		}
		return fmt.Sprintf("tcp://%s", cleanAddr)

	case "tcp":
		cleanAddr := rawAddr
		if idx := strings.Index(cleanAddr, "://"); idx != -1 {
			cleanAddr = cleanAddr[idx+3:]
		}
		if _, _, err := net.SplitHostPort(cleanAddr); err != nil {
			cleanAddr = net.JoinHostPort(cleanAddr, "53")
		}
		return fmt.Sprintf("tcp://%s", cleanAddr)

	default: // udp
		cleanAddr := rawAddr
		if idx := strings.Index(cleanAddr, "://"); idx != -1 {
			cleanAddr = cleanAddr[idx+3:]
		}

		host, port, err := net.SplitHostPort(cleanAddr)
		if err == nil {
			if port == "53" {
				return host
			}
			return fmt.Sprintf("udp://%s", net.JoinHostPort(host, port))
		}

		return cleanAddr
	}
}

func (b *Builder) Build(cfg *config.CheburConfig, outputPath string) error {
	isGlobal := cfg.RoutingMode == "global"

	var nodeOutbounds []map[string]interface{}
	var allNodeTags []string
	for _, node := range cfg.Nodes {
		ob, err := b.buildNodeOutbound(node)
		if err == nil {
			nodeOutbounds = append(nodeOutbounds, ob)
			allNodeTags = append(allNodeTags, node.Tag)
		}
	}

	sockopt := map[string]interface{}{
		"mark": 2097152,
	}

	var outbounds []map[string]interface{}
	outbounds = append(outbounds,
		map[string]interface{}{
			"tag":      "direct",
			"protocol": "freedom",
			"streamSettings": map[string]interface{}{
				"sockopt": sockopt,
			},
		},
		map[string]interface{}{
			"tag":      "direct-out",
			"protocol": "freedom",
			"streamSettings": map[string]interface{}{
				"sockopt": sockopt,
			},
		},
		map[string]interface{}{
			"tag":      "block",
			"protocol": "blackhole",
			"settings": map[string]interface{}{
				"response": map[string]string{"type": "none"},
			},
		},
		map[string]interface{}{
			"tag":      "dns-out",
			"protocol": "dns",
			"streamSettings": map[string]interface{}{
				"sockopt": sockopt,
			},
		},
	)
	outbounds = append(outbounds, nodeOutbounds...)

	existingOutbounds := make(map[string]bool)
	for _, ob := range outbounds {
		if tag, ok := ob["tag"].(string); ok {
			existingOutbounds[tag] = true
		}
	}

	var balancers []map[string]interface{}
	primaryProxyTag := "direct"
	balancerTagsMap := make(map[string]bool)

	firstNodeFallback := ""
	if len(allNodeTags) > 0 {
		firstNodeFallback = allNodeTags[0]
	}

	if len(cfg.Groups) > 0 {
		for _, grp := range cfg.Groups {
			var validGroupNodes []string
			for _, nTag := range grp.Nodes {
				for _, validTag := range allNodeTags {
					if nTag == validTag {
						validGroupNodes = append(validGroupNodes, nTag)
						break
					}
				}
			}

			if len(validGroupNodes) == 0 {
				continue
			}

			bStrategy := map[string]interface{}{
				"type": "leastPing",
			}
			if len(validGroupNodes) > 0 {
				bStrategy["fallbackTag"] = validGroupNodes[0]
			}

			balancers = append(balancers, map[string]interface{}{
				"tag":      grp.Tag,
				"selector": validGroupNodes,
				"strategy": bStrategy,
			})
			balancerTagsMap[grp.Tag] = true
			if primaryProxyTag == "direct" {
				primaryProxyTag = grp.Tag
			}
		}
	} else if len(allNodeTags) > 0 {
		bStrategy := map[string]interface{}{
			"type": "leastPing",
		}
		if firstNodeFallback != "" {
			bStrategy["fallbackTag"] = firstNodeFallback
		}

		balancers = append(balancers, map[string]interface{}{
			"tag":      "proxy-balancer",
			"selector": allNodeTags,
			"strategy": bStrategy,
		})
		balancerTagsMap["proxy-balancer"] = true
		balancerTagsMap["PROXY"] = true
		primaryProxyTag = "proxy-balancer"
	}

	var proxyDomains []string
	hasTelegram := false
	for _, d := range cfg.CustomDomains {
		d = strings.TrimSpace(d)
		if d != "" {
			proxyDomains = append(proxyDomains, "domain:"+d)
		}
	}
	for _, filePath := range cfg.LocalListFiles {
		if f, err := os.Open(filePath); err == nil {
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				l := strings.TrimSpace(sc.Text())
				if l != "" && !strings.HasPrefix(l, "#") {
					proxyDomains = append(proxyDomains, "domain:"+l)
				}
			}
			f.Close()
		}
	}
	for _, rs := range cfg.RuleSets {
		if geoCat := mapRuleSetToXrayGeosite(rs); geoCat != "" {
			proxyDomains = append(proxyDomains, geoCat)
			if strings.Contains(geoCat, "telegram") {
				hasTelegram = true
			}
		}
	}
	if !hasTelegram {
		proxyDomains = append(proxyDomains, "geosite:telegram")
	}

	dnsServerAddr := cfg.DNSServer
	if dnsServerAddr == "" {
		dnsServerAddr = "8.8.8.8"
	}
	formattedDNS := formatDNSServerForXray(dnsServerAddr, cfg.DNSProtocol)

	var dnsServers []interface{}
	dnsServers = append(dnsServers, map[string]interface{}{
		"address": "fakedns",
		"domains": proxyDomains,
	})

	if !isGlobal {
		dnsServers = append(dnsServers, "localhost")
	}
	if formattedDNS != "" {
		dnsServers = append(dnsServers, formattedDNS)
	}

	tproxyPort := cfg.TProxyPort
	if tproxyPort == 0 {
		tproxyPort = 1602
	}

	xrayConfig := map[string]interface{}{
		"log": map[string]interface{}{
			"loglevel": "warning",
		},
		"api": map[string]interface{}{
			"tag":      "api",
			"services": []string{"StatsService"},
		},
		"fakedns": map[string]interface{}{
			"ipPool":   "198.18.0.0/15",
			"poolSize": 65535,
		},
		"stats": map[string]interface{}{},
		"policy": map[string]interface{}{
			"levels": map[string]interface{}{
				"0": map[string]interface{}{
					"statsUserUplink":   true,
					"statsUserDownlink": true,
				},
			},
			"system": map[string]interface{}{
				"statsInboundUplink":    true,
				"statsInboundDownlink":  true,
				"statsOutboundUplink":   true,
				"statsOutboundDownlink": true,
			},
		},
		"dns": map[string]interface{}{
			"servers":       dnsServers,
			"queryStrategy": "UseIPv4",
		},
		"inbounds": []map[string]interface{}{
			{
				"tag":      "api-in",
				"listen":   "127.0.0.1",
				"port":     10085,
				"protocol": "dokodemo-door",
				"settings": map[string]interface{}{
					"address": "127.0.0.1",
				},
			},
			{
				"tag":      "tproxy-in",
				"listen":   "0.0.0.0",
				"port":     tproxyPort,
				"protocol": "dokodemo-door",
				"settings": map[string]interface{}{
					"network":        "tcp,udp",
					"followRedirect": true,
				},
				"streamSettings": map[string]interface{}{
					"sockopt": map[string]interface{}{
						"tproxy": "tproxy",
					},
				},
				"sniffing": map[string]interface{}{
					"enabled":      true,
					"destOverride": []string{"fakedns", "http", "tls", "quic"},
					"metadataOnly": false,
					"routeOnly":    true,
				},
			},
			{
				"tag":      "dns-in",
				"listen":   "127.0.0.42",
				"port":     cfg.DNSPort,
				"protocol": "dokodemo-door",
				"settings": map[string]interface{}{
					"network": "tcp,udp",
					"address": "127.0.0.1",
					"port":    53,
				},
			},
		},
	}

	healthPort := cfg.MixedPort
	if healthPort <= 0 {
		healthPort = 4534
	}

	inbounds := xrayConfig["inbounds"].([]map[string]interface{})
	xrayConfig["inbounds"] = append(inbounds, map[string]interface{}{
		"tag":      "mixed-in",
		"listen":   "127.0.0.1",
		"port":     healthPort,
		"protocol": "socks",
		"settings": map[string]interface{}{
			"auth": "noauth",
			"udp":  true,
		},
	})

	if len(allNodeTags) > 0 {
		checkURL := "https://www.gstatic.com/generate_204"
		if len(cfg.Groups) > 0 && cfg.Groups[0].TargetURL != "" {
			checkURL = cfg.Groups[0].TargetURL
		}
		xrayConfig["observatory"] = map[string]interface{}{
			"subjectSelector":   allNodeTags,
			"probeUrl":          checkURL,
			"probeInterval":     "15s",
			"enableConcurrency": true,
		}
	}

	xrayConfig["outbounds"] = outbounds

	setRuleDetour := func(rule map[string]interface{}, target string) {
		if target == "PROXY" || target == "proxy-balancer" {
			target = primaryProxyTag
		}

		delete(rule, "balancerTag")
		delete(rule, "outboundTag")

		if balancerTagsMap[target] {
			rule["balancerTag"] = target
		} else if existingOutbounds[target] {
			rule["outboundTag"] = target
		} else {
			if balancerTagsMap[primaryProxyTag] {
				rule["balancerTag"] = primaryProxyTag
			} else {
				rule["outboundTag"] = primaryProxyTag
			}
		}
	}

	telegramCIDRs := []string{
		"194.221.0.0/16",
		"91.108.4.0/22",
		"91.108.8.0/21",
		"91.108.12.0/22",
		"91.108.16.0/21",
		"91.108.20.0/22",
		"91.108.56.0/22",
		"149.154.160.0/20",
		"149.154.164.0/22",
		"149.154.168.0/22",
		"149.154.172.0/22",
		"91.105.192.0/23",
		"185.76.151.0/24",
	}

	var rules []map[string]interface{}

	// 1. Системные инбоунды API и локального DNS
	rules = append(rules,
		map[string]interface{}{
			"type":        "field",
			"inboundTag":  []string{"api-in"},
			"outboundTag": "api",
		},
		map[string]interface{}{
			"type":        "field",
			"inboundTag":  []string{"dns-in"},
			"outboundTag": "dns-out",
		},
	)

	// 2. Блокируем UDP к Telegram, чтобы клиент не зависал на UDP/QUIC рукопожатии
	rules = append(rules, map[string]interface{}{
		"type":        "field",
		"inboundTag":  []string{"tproxy-in"},
		"ip":          telegramCIDRs,
		"network":     "udp",
		"outboundTag": "block",
	})

	// 3. Безусловно заворачиваем весь TCP трафик Telegram в активный балансировщик/прокси
	if primaryProxyTag != "direct" {
		tgRule := map[string]interface{}{
			"type":       "field",
			"inboundTag": []string{"tproxy-in"},
			"ip":         telegramCIDRs,
			"network":    "tcp",
		}
		setRuleDetour(tgRule, primaryProxyTag)
		rules = append(rules, tgRule)
	}

	// 4. Маршрутизация DNS сервера
	if primaryProxyTag != "direct" && formattedDNS != "" {
		cleanHost := dnsServerAddr
		if strings.Contains(cleanHost, "://") {
			cleanHost = strings.Split(cleanHost, "://")[1]
		}
		cleanHost = strings.Split(cleanHost, "/")[0]
		cleanHost = strings.Split(cleanHost, ":")[0]

		if net.ParseIP(cleanHost) != nil {
			dnsRouteRule := map[string]interface{}{
				"type": "field",
				"ip":   []string{cleanHost},
			}
			setRuleDetour(dnsRouteRule, primaryProxyTag)
			rules = append(rules, dnsRouteRule)
		}
	}

	// 5. Доменные правила (SNI / FakeDNS)
	if primaryProxyTag != "direct" && len(proxyDomains) > 0 {
		domainRule := map[string]interface{}{
			"type":       "field",
			"inboundTag": []string{"tproxy-in"},
			"domain":     proxyDomains,
		}
		setRuleDetour(domainRule, primaryProxyTag)
		rules = append(rules, domainRule)
	}

	// 6. Общие UDP порты
	if primaryProxyTag != "direct" {
		udpRule := map[string]interface{}{
			"type":       "field",
			"inboundTag": []string{"tproxy-in"},
			"network":    "udp",
			"port":       "50000-65535",
		}
		setRuleDetour(udpRule, primaryProxyTag)
		rules = append(rules, udpRule)
	}

	// 7. Пользовательские подсети и подсети сервисных списков
	if !isGlobal {
		totalSubnets := append([]string(nil), cfg.CustomSubnets...)
		for _, rs := range cfg.RuleSets {
			cleanRS := strings.ToLower(strings.TrimSpace(rs))
			subnets, err := b.rulesLoader.GetSubnets(cleanRS)
			if err == nil && len(subnets) > 0 {
				totalSubnets = append(totalSubnets, subnets...)
			}
		}

		if len(totalSubnets) > 0 && primaryProxyTag != "direct" {
			rule := map[string]interface{}{
				"type":       "field",
				"inboundTag": []string{"tproxy-in"},
				"ip":         totalSubnets,
			}
			setRuleDetour(rule, primaryProxyTag)
			rules = append(rules, rule)
		}
	}

	// 8. Перехват диапазона FakeDNS
	if primaryProxyTag != "direct" {
		fakeDnsRule := map[string]interface{}{
			"type":       "field",
			"inboundTag": []string{"tproxy-in"},
			"ip":         []string{"198.18.0.0/15"},
		}
		setRuleDetour(fakeDnsRule, primaryProxyTag)
		rules = append(rules, fakeDnsRule)
	}

	// 9. Mixed порт
	if cfg.MixedPort > 0 && primaryProxyTag != "direct" {
		rule := map[string]interface{}{
			"type":       "field",
			"inboundTag": []string{"mixed-in"},
		}
		setRuleDetour(rule, primaryProxyTag)
		rules = append(rules, rule)
	}

	// 10. Дефолтный безусловный перехватчик tproxy-in
	if primaryProxyTag != "direct" {
		defaultTProxyRule := map[string]interface{}{
			"type":       "field",
			"inboundTag": []string{"tproxy-in"},
		}
		setRuleDetour(defaultTProxyRule, primaryProxyTag)
		rules = append(rules, defaultTProxyRule)
	}

	routingObj := map[string]interface{}{
		"domainStrategy": "AsIs",
		"domainMatcher":  "hybrid",
		"rules":          rules,
	}
	if len(balancers) > 0 {
		routingObj["balancers"] = balancers
	}
	xrayConfig["routing"] = routingObj

	data, err := json.MarshalIndent(xrayConfig, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal xray config: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return err
	}

	return os.WriteFile(outputPath, data, 0644)
}

func (b *Builder) buildNodeOutbound(node *config.GenericNode) (map[string]interface{}, error) {
	proto := strings.ToLower(node.Protocol)

	out := map[string]interface{}{
		"tag":      node.Tag,
		"protocol": node.Protocol,
	}

	sockopt := map[string]interface{}{
		"mark": 2097152,
	}

	switch proto {
	case "vless":
		vnextUser := map[string]interface{}{
			"id":         node.UUID,
			"encryption": "none",
			"level":      0,
		}
		if node.Flow != "" {
			vnextUser["flow"] = node.Flow
		}

		out["settings"] = map[string]interface{}{
			"vnext": []map[string]interface{}{
				{
					"address": node.Address,
					"port":    node.Port,
					"users":   []map[string]interface{}{vnextUser},
				},
			},
		}

		streamSettings := map[string]interface{}{
			"network":  node.Network,
			"security": node.Security,
			"sockopt":  sockopt,
		}

		if node.Security == "reality" {
			streamSettings["realitySettings"] = map[string]interface{}{
				"show":        false,
				"fingerprint": node.Fingerprint,
				"serverName":  node.SNI,
				"publicKey":   node.PublicKey,
				"shortId":     node.ShortID,
				"spiderX":     "/",
			}
		} else if node.Security == "tls" {
			streamSettings["tlsSettings"] = map[string]interface{}{
				"serverName":    node.SNI,
				"allowInsecure": node.Insecure,
				"fingerprint":   node.Fingerprint,
			}
		}

		if node.Network == "ws" {
			streamSettings["wsSettings"] = map[string]interface{}{
				"path": node.Path,
				"headers": map[string]string{
					"Host": node.Host,
				},
			}
		} else if node.Network == "grpc" {
			streamSettings["grpcSettings"] = map[string]interface{}{
				"serviceName": node.Path,
				"multiMode":   true,
			}
		}

		out["streamSettings"] = streamSettings

	case "hysteria2", "hysteria":
		authPass := node.Password
		if authPass == "" {
			authPass = node.UUID
		}

		serverName := strings.TrimSpace(node.SNI)
		if serverName == "" {
			serverName = strings.TrimSpace(node.Address)
		}

		out["protocol"] = "hysteria"
		out["settings"] = map[string]interface{}{
			"address": node.Address,
			"port":    node.Port,
			"version": 2,
		}

		tlsSettings := map[string]interface{}{
			"serverName":              serverName,
			"allowInsecure":           node.Insecure,
			"enableSessionResumption": false,
			"alpn":                    []string{"h3"},
		}
		if node.Fingerprint != "" {
			tlsSettings["fingerprint"] = node.Fingerprint
		} else {
			tlsSettings["fingerprint"] = "chrome"
		}

		hysteriaSettings := map[string]interface{}{
			"version": 2,
			"auth":    authPass,
		}
		if node.PortRange != "" {
			hysteriaSettings["ports"] = node.PortRange
		}
		if node.ObfsType != "" {
			hysteriaSettings["obfs"] = map[string]string{
				"type":     node.ObfsType,
				"password": node.ObfsPassword,
			}
		}

		out["streamSettings"] = map[string]interface{}{
			"network":          "hysteria",
			"security":         "tls",
			"tlsSettings":      tlsSettings,
			"hysteriaSettings": hysteriaSettings,
			"sockopt":          sockopt,
		}

	case "shadowsocks":
		out["settings"] = map[string]interface{}{
			"servers": []map[string]interface{}{
				{
					"address":  node.Address,
					"port":     node.Port,
					"method":   node.Method,
					"password": node.Password,
				},
			},
		}
		out["streamSettings"] = map[string]interface{}{
			"sockopt": sockopt,
		}

	case "trojan":
		out["settings"] = map[string]interface{}{
			"settings": map[string]interface{}{
				"servers": []map[string]interface{}{
					{
						"address":  node.Address,
						"port":     node.Port,
						"password": node.Password,
					},
				},
			},
		}
		out["streamSettings"] = map[string]interface{}{
			"security": "tls",
			"tlsSettings": map[string]interface{}{
				"serverName":    node.SNI,
				"allowInsecure": node.Insecure,
			},
			"sockopt": sockopt,
		}

	case "socks":
		out["protocol"] = "socks"
		serverObj := map[string]interface{}{
			"address": node.Address,
			"port":    node.Port,
		}
		if node.Username != "" {
			serverObj["users"] = []map[string]interface{}{
				{
					"user":  node.Username,
					"pass":  node.Password,
					"level": 0,
				},
			}
		}
		out["settings"] = map[string]interface{}{
			"servers": []map[string]interface{}{serverObj},
		}
		out["streamSettings"] = map[string]interface{}{
			"sockopt": sockopt,
		}

	default:
		return nil, fmt.Errorf("unsupported protocol for xray: %s", node.Protocol)
	}

	return out, nil
}
