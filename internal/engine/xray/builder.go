package xray

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
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
		return rs
	}
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

func (b *Builder) Build(cfg *config.CheburConfig, outputPath string) error {
	isGlobal := cfg.RoutingMode == "global"

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

	sockopt := map[string]interface{}{
		"mark": 2097152,
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
			"tag":      "direct-out",
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

	var balancers []map[string]interface{}
	primaryProxyTag := "direct"
	balancerTagsMap := make(map[string]bool)

	if len(cfg.Groups) > 0 {
		for _, grp := range cfg.Groups {
			balancers = append(balancers, map[string]interface{}{
				"tag":      grp.Tag,
				"selector": grp.Nodes,
				"strategy": map[string]interface{}{
					"type": "leastPing",
				},
			})
			balancerTagsMap[grp.Tag] = true
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
		balancerTagsMap["proxy-balancer"] = true
		balancerTagsMap["PROXY"] = true

		xrayConfig["observatory"] = map[string]interface{}{
			"subjectSelector":   allNodeTags,
			"probeUrl":          "https://www.gstatic.com/generate_204",
			"probeInterval":     "3m",
			"enableConcurrency": true,
		}
		primaryProxyTag = "proxy-balancer"
	}

	xrayConfig["outbounds"] = outbounds

	setRuleDetour := func(rule map[string]interface{}, target string) {
		if target == "PROXY" || target == "proxy-balancer" {
			target = primaryProxyTag
		}
		if balancerTagsMap[target] {
			rule["balancerTag"] = target
		} else {
			rule["outboundTag"] = target
		}
	}

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

	// 1. Клиенты
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
		setRuleDetour(rule, primaryProxyTag)
		rules = append(rules, rule)
	}

	// 2. Секции маршрутизации
	if !isGlobal {
		for _, rp := range cfg.RoutePolicies {
			if !rp.Enabled || rp.Outbound == "" {
				continue
			}

			outboundTarget := rp.Outbound

			totalPolicySubnets := append([]string(nil), rp.Subnets...)
			for _, rs := range rp.RuleSets {
				cleanRS := strings.ToLower(strings.TrimSpace(rs))
				if subnets, err := b.rulesLoader.GetSubnets(cleanRS); err == nil && len(subnets) > 0 {
					totalPolicySubnets = append(totalPolicySubnets, subnets...)
				}
			}

			if len(totalPolicySubnets) > 0 {
				rule := map[string]interface{}{
					"type":       "field",
					"inboundTag": []string{"tproxy-in"},
					"ip":         totalPolicySubnets,
				}
				setRuleDetour(rule, outboundTarget)
				rules = append(rules, rule)
			}

			var totalPolicyDomains []string
			for _, d := range rp.Domains {
				d = strings.TrimSpace(d)
				if d != "" {
					totalPolicyDomains = append(totalPolicyDomains, "domain:"+d)
				}
			}
			for _, rs := range rp.RuleSets {
				if geoCat := mapRuleSetToXrayGeosite(rs); geoCat != "" {
					totalPolicyDomains = append(totalPolicyDomains, geoCat)
				}
			}

			if len(totalPolicyDomains) > 0 {
				rule := map[string]interface{}{
					"type":       "field",
					"inboundTag": []string{"tproxy-in"},
					"domain":     totalPolicyDomains,
				}
				setRuleDetour(rule, outboundTarget)
				rules = append(rules, rule)
			}
		}
	}

	// 3. Дефолтные правила
	if primaryProxyTag != "direct" {
		if isGlobal {
			rule := map[string]interface{}{
				"type":       "field",
				"inboundTag": []string{"tproxy-in"},
			}
			setRuleDetour(rule, primaryProxyTag)
			rules = append(rules, rule)
		} else {
			hasDiscord := false
			for _, rs := range cfg.RuleSets {
				if strings.ToLower(strings.TrimSpace(rs)) == "discord" {
					hasDiscord = true
					break
				}
			}

			totalSubnets := append([]string(nil), cfg.CustomSubnets...)
			for _, rs := range cfg.RuleSets {
				cleanRS := strings.ToLower(strings.TrimSpace(rs))
				subnets, err := b.rulesLoader.GetSubnets(cleanRS)
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
				setRuleDetour(rule, primaryProxyTag)
				rules = append(rules, rule)
			}

			if hasDiscord {
				discordUdpRule := map[string]interface{}{
					"type":       "field",
					"inboundTag": []string{"tproxy-in"},
					"network":    "udp",
					"port":       "443,50000-65535",
				}
				setRuleDetour(discordUdpRule, primaryProxyTag)
				rules = append(rules, discordUdpRule)
			}

			if len(cfg.CustomPorts) > 0 {
				portStr := parsePortsForXray(cfg.CustomPorts)
				if portStr != "" {
					portRule := map[string]interface{}{
						"type":       "field",
						"inboundTag": []string{"tproxy-in"},
						"port":       portStr,
					}
					setRuleDetour(portRule, primaryProxyTag)
					rules = append(rules, portRule)
				}
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
				if geoCat := mapRuleSetToXrayGeosite(rs); geoCat != "" {
					totalDomains = append(totalDomains, geoCat)
				}
			}

			if len(totalDomains) > 0 {
				rule := map[string]interface{}{
					"type":       "field",
					"inboundTag": []string{"tproxy-in"},
					"domain":     totalDomains,
				}
				setRuleDetour(rule, primaryProxyTag)
				rules = append(rules, rule)
			}
		}
	}

	if cfg.MixedPort > 0 && primaryProxyTag != "direct" {
		rule := map[string]interface{}{
			"type":       "field",
			"inboundTag": []string{"mixed-in"},
		}
		setRuleDetour(rule, primaryProxyTag)
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
		"mark": 2097152,
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
