package xray

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
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

func (b *Builder) Build(cfg *config.CheburConfig, outputPath string) error {
	isGlobal := cfg.RoutingMode == "global"

	// 1. Формирование DNS серверов
	var dnsServers []interface{}

	dnsServerAddr := cfg.DNSServer
	if dnsServerAddr == "" {
		dnsServerAddr = "8.8.8.8"
	}

	switch cfg.DNSProtocol {
	case "doh", "https":
		if !strings.HasPrefix(dnsServerAddr, "https://") {
			dnsServerAddr = fmt.Sprintf("https://%s/dns-query", dnsServerAddr)
		}
		dnsServers = append(dnsServers, dnsServerAddr)
	case "dot", "tls":
		if !strings.HasPrefix(dnsServerAddr, "tcp+local://") {
			dnsServers = append(dnsServers, fmt.Sprintf("tcp://%s:853", dnsServerAddr))
		}
	default:
		if !strings.Contains(dnsServerAddr, ":") {
			dnsServers = append(dnsServers, dnsServerAddr+":53")
		} else {
			dnsServers = append(dnsServers, dnsServerAddr)
		}
	}

	if cfg.BootstrapDNS != "" {
		bootstrap := cfg.BootstrapDNS
		if !strings.Contains(bootstrap, ":") {
			bootstrap += ":53"
		}
		dnsServers = append(dnsServers, bootstrap)
	}

	// В режиме выборочных правил локальные домены могут резолвиться локально
	if !isGlobal {
		dnsServers = append(dnsServers, "localhost")
	}

	xrayConfig := map[string]interface{}{
		"log": map[string]interface{}{
			"loglevel": "warning",
		},
		"api": map[string]interface{}{
			"tag":      "api",
			"services": []string{"StatsService"},
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
	}

	tproxyPort := cfg.TProxyPort
	if tproxyPort == 0 {
		tproxyPort = 1602
	}

	// 2. Inbounds (TProxy + DNS Inbound + Mixed Port + Dokodemo API)
	inbounds := []map[string]interface{}{
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
			"listen":   "0.0.0.0", // Обязательно 0.0.0.0 для TProxy перехвата
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
				"destOverride": []string{"http", "tls", "quic"},
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
	}

	if cfg.MixedPort > 0 {
		inbounds = append(inbounds, map[string]interface{}{
			"tag":      "mixed-in",
			"listen":   "127.0.0.1",
			"port":     cfg.MixedPort,
			"protocol": "socks",
			"settings": map[string]interface{}{
				"auth": "noauth",
				"udp":  true,
			},
		})
	}
	xrayConfig["inbounds"] = inbounds

	// 3. Outbounds (Прямой выход, Блокировка, DNS и ноды)
	sockopt := map[string]interface{}{
		"mark": 2097152, // 0x200000 NFT SelfMark
	}

	outbounds := []map[string]interface{}{
		{
			"tag":      "direct",
			"protocol": "freedom",
			"streamSettings": map[string]interface{}{
				"sockopt": sockopt,
			},
		},
		{
			"tag":      "block",
			"protocol": "blackhole",
			"settings": map[string]interface{}{
				"response": map[string]string{"type": "none"},
			},
		},
		{
			"tag":      "dns-out",
			"protocol": "dns",
			"streamSettings": map[string]interface{}{
				"sockopt": sockopt,
			},
		},
	}

	var allNodeTags []string
	for _, node := range cfg.Nodes {
		ob, err := b.buildNodeOutbound(node)
		if err == nil {
			outbounds = append(outbounds, ob)
			allNodeTags = append(allNodeTags, node.Tag)
		}
	}

	// 4. Балансировка (Observatory + Balancers)
	var balancers []map[string]interface{}
	primaryProxyTag := "direct"

	if len(cfg.Groups) > 0 {
		for _, grp := range cfg.Groups {
			balancers = append(balancers, map[string]interface{}{
				"tag":      grp.Tag,
				"selector": grp.Nodes,
				"strategy": map[string]interface{}{
					"type": "leastPing",
				},
			})
			if primaryProxyTag == "direct" {
				primaryProxyTag = grp.Tag
			}
		}

		checkURL := "https://www.gstatic.com/generate_204"
		if cfg.Groups[0].TargetURL != "" {
			checkURL = cfg.Groups[0].TargetURL
		}

		xrayConfig["observatory"] = map[string]interface{}{
			"subjectSelector":   allNodeTags,
			"probeUrl":          checkURL,
			"probeInterval":     "3m",
			"enableConcurrency": true,
		}
	} else if len(allNodeTags) > 0 {
		balancers = append(balancers, map[string]interface{}{
			"tag":      "proxy-balancer",
			"selector": allNodeTags,
			"strategy": map[string]interface{}{
				"type": "leastPing",
			},
		})

		xrayConfig["observatory"] = map[string]interface{}{
			"subjectSelector":   allNodeTags,
			"probeUrl":          "https://www.gstatic.com/generate_204",
			"probeInterval":     "3m",
			"enableConcurrency": true,
		}
		primaryProxyTag = "proxy-balancer"
	}

	xrayConfig["outbounds"] = outbounds

	// 5. Таблица правил маршрутизации (Routing Rules)
	rules := []map[string]interface{}{
		{
			"type":        "field",
			"inboundTag":  []string{"api-in"},
			"outboundTag": "api",
		},
		{
			"type":        "field",
			"inboundTag":  []string{"dns-in"},
			"outboundTag": "dns-out",
		},
	}

	// --- ПРИОРИТЕТ 1: Client Policies ---
	var directClients []string
	var fullProxyClients []string

	for _, cp := range cfg.ClientPolicies {
		if !cp.Enabled || cp.Target == "" {
			continue
		}
		cidr := resolveTargetToCIDR(cp.Target)
		if cidr == "" {
			continue
		}

		switch cp.Mode {
		case config.ClientModeDirect:
			directClients = append(directClients, cidr)
		case config.ClientModeFullProxy:
			fullProxyClients = append(fullProxyClients, cidr)
		}
	}

	if len(directClients) > 0 {
		rules = append(rules, map[string]interface{}{
			"type":        "field",
			"inboundTag":  []string{"tproxy-in"},
			"source":      directClients,
			"outboundTag": "direct",
		})
	}

	if len(fullProxyClients) > 0 && primaryProxyTag != "direct" {
		rule := map[string]interface{}{
			"type":       "field",
			"inboundTag": []string{"tproxy-in"},
			"source":     fullProxyClients,
		}
		if len(balancers) > 0 {
			rule["balancerTag"] = primaryProxyTag
		} else {
			rule["outboundTag"] = primaryProxyTag
		}
		rules = append(rules, rule)
	}

	// --- ПРИОРИТЕТ 2: Общие правила маршрутизации ---
	if primaryProxyTag != "direct" {
		if isGlobal {
			// РЕЖИМ GLOBAL: весь оставшийся входящий трафик уходит в прокси
			rule := map[string]interface{}{
				"type":       "field",
				"inboundTag": []string{"tproxy-in"},
			}
			if len(balancers) > 0 {
				rule["balancerTag"] = primaryProxyTag
			} else {
				rule["outboundTag"] = primaryProxyTag
			}
			rules = append(rules, rule)
		} else {
			// Проверяем наличие discord в списке наборов правил
			hasDiscord := false
			for _, rs := range cfg.RuleSets {
				if rs == "discord" {
					hasDiscord = true
					break
				}
			}

			// РЕЖИМ RULES: роутинг по спискам подсетей и доменов
			totalSubnets := append([]string(nil), cfg.CustomSubnets...)
			for _, rs := range cfg.RuleSets {
				subnets, err := b.rulesLoader.GetSubnets(rs)
				if err == nil && len(subnets) > 0 {
					totalSubnets = append(totalSubnets, subnets...)
				}
			}

			if len(totalSubnets) > 0 {
				rule := map[string]interface{}{
					"type":       "field",
					"inboundTag": []string{"tproxy-in"},
					"ip":         totalSubnets,
				}
				if len(balancers) > 0 {
					rule["balancerTag"] = primaryProxyTag
				} else {
					rule["outboundTag"] = primaryProxyTag
				}
				rules = append(rules, rule)
			}

			// Явный перехват голосовых портов Discord UDP (WebRTC & Handshake)
			if hasDiscord {
				discordUdpRule := map[string]interface{}{
					"type":       "field",
					"inboundTag": []string{"tproxy-in"},
					"network":    "udp",
					"port":       "443,50000-65535",
				}
				if len(balancers) > 0 {
					discordUdpRule["balancerTag"] = primaryProxyTag
				} else {
					discordUdpRule["outboundTag"] = primaryProxyTag
				}
				rules = append(rules, discordUdpRule)
			}

			var totalDomains []string
			for _, d := range cfg.CustomDomains {
				d = strings.TrimSpace(d)
				if d != "" {
					totalDomains = append(totalDomains, "domain:"+d)
				}
			}

			for _, filePath := range cfg.LocalListFiles {
				if f, err := os.Open(filePath); err == nil {
					sc := bufio.NewScanner(f)
					for sc.Scan() {
						l := strings.TrimSpace(sc.Text())
						if l != "" && !strings.HasPrefix(l, "#") {
							totalDomains = append(totalDomains, "domain:"+l)
						}
					}
					f.Close()
				}
			}

			for _, rs := range cfg.RuleSets {
				switch rs {
				case "youtube":
					totalDomains = append(totalDomains, "domain:youtube.com", "domain:googlevideo.com", "domain:ytimg.com")
				case "meta":
					totalDomains = append(totalDomains, "domain:instagram.com", "domain:facebook.com", "domain:cdninstagram.com")
				case "telegram":
					totalDomains = append(totalDomains, "domain:t.me", "domain:telegram.org")
				case "discord":
					totalDomains = append(totalDomains, "domain:discord.com", "domain:discord.gg", "domain:discordapp.com")
				case "twitter":
					totalDomains = append(totalDomains, "domain:x.com", "domain:twitter.com", "domain:twimg.com")
				case "google_ai":
					totalDomains = append(totalDomains, "domain:gemini.google.com", "domain:generativelanguage.googleapis.com")
				case "russia_inside":
					totalDomains = append(totalDomains, "geosite:category-ru")
				}
			}

			if len(totalDomains) > 0 {
				rule := map[string]interface{}{
					"type":       "field",
					"inboundTag": []string{"tproxy-in"},
					"domain":     totalDomains,
				}
				if len(balancers) > 0 {
					rule["balancerTag"] = primaryProxyTag
				} else {
					rule["outboundTag"] = primaryProxyTag
				}
				rules = append(rules, rule)
			}
		}
	}

	// Mixed-in порт всегда направляется в прокси
	if cfg.MixedPort > 0 && primaryProxyTag != "direct" {
		rule := map[string]interface{}{
			"type":       "field",
			"inboundTag": []string{"mixed-in"},
		}
		if len(balancers) > 0 {
			rule["balancerTag"] = primaryProxyTag
		} else {
			rule["outboundTag"] = primaryProxyTag
		}
		rules = append(rules, rule)
	}

	routingObj := map[string]interface{}{
		"domainStrategy": "IPIfNonMatch",
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
	out := map[string]interface{}{
		"tag":      node.Tag,
		"protocol": node.Protocol,
	}

	sockopt := map[string]interface{}{
		"mark": 2097152, // 0x200000 NFT SelfMark
	}

	switch node.Protocol {
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

	case "hysteria2":
		out["protocol"] = "hysteria2"
		out["settings"] = map[string]interface{}{
			"servers": []map[string]interface{}{
				{
					"address":  node.Address,
					"port":     node.Port,
					"password": node.Password,
				},
			},
		}
		out["streamSettings"] = map[string]interface{}{
			"network":  "udp",
			"security": "tls",
			"tlsSettings": map[string]interface{}{
				"serverName":    node.SNI,
				"allowInsecure": node.Insecure,
			},
			"sockopt": sockopt,
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
			"servers": []map[string]interface{}{
				{
					"address":  node.Address,
					"port":     node.Port,
					"password": node.Password,
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
