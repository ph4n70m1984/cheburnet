package singbox

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

func parsePortsAndRanges(rawPorts []string) ([]uint16, []string) {
	var singlePorts []uint16
	var portRanges []string

	for _, p := range rawPorts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}

		if strings.Contains(p, ":") || strings.Contains(p, "-") {
			normalized := strings.ReplaceAll(p, "-", ":")
			parts := strings.Split(normalized, ":")
			if len(parts) == 2 {
				start, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
				end, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
				if err1 == nil && err2 == nil && start > 0 && end <= 65535 && start <= end {
					portRanges = append(portRanges, fmt.Sprintf("%d:%d", start, end))
				}
			}
			continue
		}

		if val, err := strconv.Atoi(p); err == nil && val > 0 && val <= 65535 {
			singlePorts = append(singlePorts, uint16(val))
		}
	}

	return singlePorts, portRanges
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

func getRouterLANIP() string {
	iface, err := net.InterfaceByName("br-lan")
	if err == nil {
		addrs, err := iface.Addrs()
		if err == nil {
			for _, addr := range addrs {
				var ip net.IP
				switch v := addr.(type) {
				case *net.IPNet:
					ip = v.IP
				case *net.IPAddr:
					ip = v.IP
				}
				if ip != nil && ip.To4() != nil && !ip.IsLoopback() {
					return ip.String()
				}
			}
		}
	}
	return "0.0.0.0"
}

func (b *Builder) Build(cfg *config.CheburConfig, outputPath string) error {
	routerIP := getRouterLANIP()
	clashController := fmt.Sprintf("%s:9090", routerIP)

	remoteDNSType := "udp"
	remoteDNSServer := cfg.DNSServer
	if remoteDNSServer == "" {
		remoteDNSServer = "8.8.8.8"
	}
	remoteDNSPort := 53

	switch cfg.DNSProtocol {
	case "doh", "https":
		remoteDNSType = "https"
		if !strings.HasPrefix(remoteDNSServer, "https://") {
			remoteDNSServer = fmt.Sprintf("https://%s/dns-query", remoteDNSServer)
		}
		remoteDNSPort = 443
	case "dot", "tls":
		remoteDNSType = "tls"
		remoteDNSPort = 853
	default:
		remoteDNSType = "udp"
		remoteDNSPort = 53
	}

	bootstrapServer := cfg.BootstrapDNS
	if bootstrapServer == "" {
		bootstrapServer = "77.88.8.8"
	}

	dnsRules := []map[string]interface{}{
		{
			"action":     "reject",
			"query_type": []string{"HTTPS"},
		},
		{
			"action":        "reject",
			"domain_suffix": []string{"use-application-dns.net"},
		},
	}

	isGlobal := cfg.RoutingMode == "global"

	if isGlobal {
		dnsRules = append(dnsRules, map[string]interface{}{
			"action": "route",
			"server": "fakeip-dns",
		})
	} else {
		if len(cfg.CustomDomains) > 0 {
			dnsRules = append(dnsRules, map[string]interface{}{
				"action":        "route",
				"server":        "fakeip-dns",
				"domain_suffix": cfg.CustomDomains,
			})
		}
		if len(cfg.RuleSets) > 0 {
			dnsRules = append(dnsRules, map[string]interface{}{
				"action":   "route",
				"server":   "fakeip-dns",
				"rule_set": cfg.RuleSets,
			})
		}
	}

	dnsConfig := map[string]interface{}{
		"servers": []map[string]interface{}{
			{
				"tag":         "bootstrap-dns",
				"type":        "udp",
				"server":      bootstrapServer,
				"server_port": 53,
			},
			{
				"tag":         "fakeip-dns",
				"type":        "fakeip",
				"inet4_range": "198.18.0.0/15",
			},
			{
				"tag":         "remote-dns",
				"type":        remoteDNSType,
				"server":      remoteDNSServer,
				"server_port": remoteDNSPort,
			},
		},
		"rules":             dnsRules,
		"final":             "remote-dns",
		"strategy":          "ipv4_only",
		"independent_cache": true,
	}

	experimentalConfig := map[string]interface{}{
		"cache_file": map[string]interface{}{
			"enabled":      true,
			"path":         "/tmp/sing-box-cache.db",
			"store_fakeip": true,
		},
		"clash_api": map[string]interface{}{
			"external_controller": clashController,
			"default_mode":        "rule",
		},
	}

	if cfg.EnableYACD {
		if clashAPI, ok := experimentalConfig["clash_api"].(map[string]interface{}); ok {
			clashAPI["external_ui"] = "yacd"
		}
	}

	tproxyPort := cfg.TProxyPort
	if tproxyPort == 0 {
		tproxyPort = 1602
	}

	sbConfig := map[string]interface{}{
		"log": map[string]interface{}{
			"level":     "warn",
			"timestamp": false,
		},
		"dns": dnsConfig,
		"inbounds": []map[string]interface{}{
			{
				"type":          "tproxy",
				"tag":           "tproxy-in",
				"listen":        "0.0.0.0",
				"listen_port":   tproxyPort,
				"tcp_fast_open": true,
				"udp_fragment":  true,
				"sniff":         true,
			},
			{
				"type":        "direct",
				"tag":         "dns-in",
				"listen":      "127.0.0.42",
				"listen_port": cfg.DNSPort,
			},
			{
				"type":        "mixed",
				"tag":         "mixed-in",
				"listen":      "127.0.0.1",
				"listen_port": cfg.MixedPort,
			},
		},
		"experimental": experimentalConfig,
	}

	outbounds := []map[string]interface{}{
		{
			"type": "direct",
			"tag":  "direct-out",
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

	activeOutboundTag := "direct-out"

	if len(cfg.Groups) > 0 {
		for _, grp := range cfg.Groups {
			urltestTag := fmt.Sprintf("%s-urltest", grp.Tag)
			interval := grp.Interval
			if interval == "" {
				interval = "3m"
			}
			tolerance := grp.Tolerance
			if tolerance == 0 {
				tolerance = 50
			}
			targetURL := grp.TargetURL
			if targetURL == "" {
				targetURL = "https://www.gstatic.com/generate_204"
			}

			outbounds = append(outbounds, map[string]interface{}{
				"type":      "urltest",
				"tag":       urltestTag,
				"outbounds": grp.Nodes,
				"url":       targetURL,
				"interval":  interval,
				"tolerance": tolerance,
			})

			selectorList := append(grp.Nodes, urltestTag)
			outbounds = append(outbounds, map[string]interface{}{
				"type":      "selector",
				"tag":       grp.Tag,
				"outbounds": selectorList,
				"default":   urltestTag,
			})

			if activeOutboundTag == "direct-out" {
				activeOutboundTag = grp.Tag
			}
		}
	} else if len(allNodeTags) > 0 {
		urltestTag := "AUTO"
		selectorTag := "PROXY"

		outbounds = append(outbounds, map[string]interface{}{
			"type":      "urltest",
			"tag":       urltestTag,
			"outbounds": allNodeTags,
			"url":       "https://www.gstatic.com/generate_204",
			"interval":  "3m",
			"tolerance": 50,
		})

		selectorList := append([]string{urltestTag}, allNodeTags...)
		outbounds = append(outbounds, map[string]interface{}{
			"type":      "selector",
			"tag":       selectorTag,
			"outbounds": selectorList,
			"default":   urltestTag,
		})

		activeOutboundTag = selectorTag
	}

	sbConfig["outbounds"] = outbounds

	routeRules := []map[string]interface{}{
		{
			"action":  "sniff",
			"inbound": []string{"tproxy-in", "dns-in"},
		},
		{
			"action":   "hijack-dns",
			"protocol": []string{"dns"},
		},
	}

	// 1. Клиентские политики (Client Policy)
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
		routeRules = append(routeRules, map[string]interface{}{
			"action":         "route",
			"inbound":        []string{"tproxy-in"},
			"source_ip_cidr": directClients,
			"outbound":       "direct-out",
		})
	}

	if len(fullProxyClients) > 0 && activeOutboundTag != "direct-out" {
		routeRules = append(routeRules, map[string]interface{}{
			"action":         "route",
			"inbound":        []string{"tproxy-in"},
			"source_ip_cidr": fullProxyClients,
			"outbound":       activeOutboundTag,
		})
	}

	// 2. Общие правила маршрутизации
	if activeOutboundTag != "direct-out" {
		if isGlobal {
			routeRules = append(routeRules, map[string]interface{}{
				"action":   "route",
				"inbound":  []string{"tproxy-in"},
				"outbound": activeOutboundTag,
			})
		} else {
			totalSubnets := append([]string(nil), cfg.CustomSubnets...)

			hasDiscord := false
			for _, rs := range cfg.RuleSets {
				if rs == "discord" {
					hasDiscord = true
				}
				subnets, err := b.rulesLoader.GetSubnets(rs)
				if err == nil && len(subnets) > 0 {
					totalSubnets = append(totalSubnets, subnets...)
				}
			}

			if len(totalSubnets) > 0 {
				routeRules = append(routeRules, map[string]interface{}{
					"action":   "route",
					"inbound":  []string{"tproxy-in"},
					"ip_cidr":  totalSubnets,
					"outbound": activeOutboundTag,
				})
			}

			// Явный перехват голосовых UDP портов Discord (WebRTC & Handshake)
			if hasDiscord {
				routeRules = append(routeRules, map[string]interface{}{
					"action":     "route",
					"inbound":    []string{"tproxy-in"},
					"network":    "udp",
					"port":       []uint16{443},
					"port_range": []string{"50000:65535"},
					"outbound":   activeOutboundTag,
				})
			}

			// Пользовательские порты и диапазоны
			if len(cfg.CustomPorts) > 0 {
				singlePorts, portRanges := parsePortsAndRanges(cfg.CustomPorts)
				if len(singlePorts) > 0 || len(portRanges) > 0 {
					portRule := map[string]interface{}{
						"action":   "route",
						"inbound":  []string{"tproxy-in"},
						"outbound": activeOutboundTag,
					}
					if len(singlePorts) > 0 {
						portRule["port"] = singlePorts
					}
					if len(portRanges) > 0 {
						portRule["port_range"] = portRanges
					}
					routeRules = append(routeRules, portRule)
				}
			}

			if len(cfg.CustomDomains) > 0 {
				routeRules = append(routeRules, map[string]interface{}{
					"action":        "route",
					"inbound":       []string{"tproxy-in"},
					"domain_suffix": cfg.CustomDomains,
					"outbound":      activeOutboundTag,
				})
			}

			if len(cfg.RuleSets) > 0 {
				routeRules = append(routeRules, map[string]interface{}{
					"action":   "route",
					"inbound":  []string{"tproxy-in"},
					"outbound": activeOutboundTag,
					"rule_set": cfg.RuleSets,
				})
			}
		}
	}

	routeRules = append(routeRules, map[string]interface{}{
		"action":   "route",
		"inbound":  []string{"mixed-in"},
		"outbound": activeOutboundTag,
	})

	var ruleSetObjects []map[string]interface{}
	if !isGlobal {
		for _, rs := range cfg.RuleSets {
			ruleSetObjects = append(ruleSetObjects, map[string]interface{}{
				"type":            "remote",
				"tag":             rs,
				"format":          "binary",
				"url":             fmt.Sprintf("https://github.com/itdoginfo/allow-domains/releases/latest/download/%s.srs", rs),
				"download_detour": activeOutboundTag,
				"update_interval": "1d",
			})
		}
	}

	finalOutbound := "direct-out"
	if isGlobal && activeOutboundTag != "direct-out" {
		finalOutbound = activeOutboundTag
	}

	sbConfig["route"] = map[string]interface{}{
		"rules":                   routeRules,
		"rule_set":                ruleSetObjects,
		"final":                   finalOutbound,
		"auto_detect_interface":   true,
		"default_domain_resolver": "bootstrap-dns",
		"default_mark":            2097152,
	}

	data, err := json.MarshalIndent(sbConfig, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal sing-box config: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return err
	}

	return os.WriteFile(outputPath, data, 0644)
}

func (b *Builder) buildNodeOutbound(node *config.GenericNode) (map[string]interface{}, error) {
	out := map[string]interface{}{
		"tag":         node.Tag,
		"server":      node.Address,
		"server_port": node.Port,
	}

	switch node.Protocol {
	case "vless":
		out["type"] = "vless"
		out["uuid"] = node.UUID
		if node.Flow != "" {
			out["flow"] = node.Flow
		}

		tlsMap := map[string]interface{}{
			"enabled":     true,
			"server_name": node.SNI,
			"insecure":    node.Insecure,
		}
		if node.Fingerprint != "" {
			tlsMap["utls"] = map[string]interface{}{
				"enabled":     true,
				"fingerprint": node.Fingerprint,
			}
		}
		if node.Security == "reality" {
			realityMap := map[string]interface{}{
				"enabled":    true,
				"public_key": node.PublicKey,
				"short_id":   node.ShortID,
			}
			tlsMap["reality"] = realityMap
		}
		out["tls"] = tlsMap

		if node.Network == "ws" {
			out["transport"] = map[string]interface{}{
				"type":    "ws",
				"path":    node.Path,
				"headers": map[string]string{"Host": node.Host},
			}
		} else if node.Network == "grpc" {
			out["transport"] = map[string]interface{}{
				"type":         "grpc",
				"service_name": node.Path,
			}
		}

	case "hysteria2":
		out["type"] = "hysteria2"
		out["password"] = node.Password
		if node.PortRange != "" {
			out["server_ports"] = strings.Split(node.PortRange, ",")
		}
		if node.ObfsType != "" {
			out["obfs"] = map[string]string{
				"type":     node.ObfsType,
				"password": node.ObfsPassword,
			}
		}
		out["tls"] = map[string]interface{}{
			"enabled":     true,
			"server_name": node.SNI,
			"insecure":    node.Insecure,
		}

	case "shadowsocks":
		out["type"] = "shadowsocks"
		out["method"] = node.Method
		out["password"] = node.Password

	case "trojan":
		out["type"] = "trojan"
		out["password"] = node.Password
		out["tls"] = map[string]interface{}{
			"enabled":     true,
			"server_name": node.SNI,
			"insecure":    node.Insecure,
		}

	case "socks":
		out["type"] = "socks"
		out["version"] = node.SocksVersion
		if node.Username != "" {
			out["username"] = node.Username
			out["password"] = node.Password
		}

	default:
		return nil, fmt.Errorf("unsupported protocol: %s", node.Protocol)
	}

	return out, nil
}
