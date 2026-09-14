package singbox

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"cheburnet/internal/config"
	"cheburnet/internal/network"
	"cheburnet/internal/ruleset"
)

type BuilderV13 struct {
	rulesLoader *network.CompressedRulesetLoader
}

func NewBuilderV13() *BuilderV13 {
	return &BuilderV13{
		rulesLoader: network.NewCompressedRulesetLoader(),
	}
}

func (b *BuilderV13) Build(cfg *config.CheburConfig, outputPath string) error {
	routerIP := getRouterLANIP()
	clashController := fmt.Sprintf("%s:9090", routerIP)

	bootstrapServer := cfg.BootstrapDNS
	if bootstrapServer == "" {
		bootstrapServer = "77.88.8.8"
	}

	// 1. Формирование параметров DNS серверов для Sing-Box 1.12 - 1.14+
	var remoteDNSType string
	var remoteDNSServer string
	var remoteDNSPort uint16 = 53
	var remoteServerName string

	remoteRaw := cfg.DNSServer
	if remoteRaw == "" {
		remoteRaw = "8.8.8.8"
	}

	switch cfg.DNSProtocol {
	case "doh", "https":
		remoteDNSType = "https"
		clean := strings.TrimPrefix(remoteRaw, "https://")
		hostPart, portPart, err := net.SplitHostPort(clean)
		if err == nil {
			remoteDNSServer = hostPart
			p, _ := strconv.Atoi(portPart)
			remoteDNSPort = uint16(p)
		} else {
			remoteDNSServer = clean
			remoteDNSPort = 443
		}
		remoteServerName = remoteDNSServer

	case "dot", "tls":
		remoteDNSType = "tls"
		clean := strings.TrimPrefix(remoteRaw, "tls://")
		hostPart, portPart, err := net.SplitHostPort(clean)
		if err == nil {
			remoteDNSServer = hostPart
			p, _ := strconv.Atoi(portPart)
			remoteDNSPort = uint16(p)
		} else {
			remoteDNSServer = clean
			remoteDNSPort = 853
		}
		remoteServerName = remoteDNSServer

	case "tcp":
		remoteDNSType = "tcp"
		clean := strings.TrimPrefix(remoteRaw, "tcp://")
		hostPart, portPart, err := net.SplitHostPort(clean)
		if err == nil {
			remoteDNSServer = hostPart
			p, _ := strconv.Atoi(portPart)
			remoteDNSPort = uint16(p)
		} else {
			remoteDNSServer = clean
			remoteDNSPort = 53
		}

	default:
		remoteDNSType = "udp"
		clean := strings.TrimPrefix(remoteRaw, "udp://")
		hostPart, portPart, err := net.SplitHostPort(clean)
		if err == nil {
			remoteDNSServer = hostPart
			p, _ := strconv.Atoi(portPart)
			remoteDNSPort = uint16(p)
		} else {
			remoteDNSServer = clean
			remoteDNSPort = 53
		}
	}

	// 2. Обработка локальных пользовательских SRS
	type localSRS struct {
		tag  string
		path string
	}
	var customSRSObjects []localSRS
	var customSRSTags []string

	for idx, srs := range cfg.CustomSRSRulesets {
		if !srs.Enabled || strings.TrimSpace(srs.URL) == "" {
			continue
		}
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(srs.URL)))[:12]
		filePath := filepath.Join(ruleset.RulesetDir, fmt.Sprintf("srs_%s.srs", hash))

		if _, err := os.Stat(filePath); err == nil {
			tag := fmt.Sprintf("custom-srs-%d", idx+1)
			customSRSObjects = append(customSRSObjects, localSRS{tag: tag, path: filePath})
			customSRSTags = append(customSRSTags, tag)
		}
	}

	// 3. Списки сервисов и маршрутизация
	isGlobal := cfg.RoutingMode == "global"

	activeRuleSetsMap := make(map[string]bool)
	for _, rs := range cfg.RuleSets {
		norm := strings.ToLower(strings.TrimSpace(rs))
		if norm != "" {
			activeRuleSetsMap[norm] = true
		}
	}
	for _, rp := range cfg.RoutePolicies {
		if rp.Enabled {
			for _, rs := range rp.RuleSets {
				norm := strings.ToLower(strings.TrimSpace(rs))
				if norm != "" {
					activeRuleSetsMap[norm] = true
				}
			}
		}
	}

	var allRuleSets []string
	for rs := range activeRuleSetsMap {
		allRuleSets = append(allRuleSets, rs)
	}

	dnsRuleSetList := append([]string(nil), allRuleSets...)
	dnsRuleSetList = append(dnsRuleSetList, customSRSTags...)

	// 4. Правила DNS
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

	if isGlobal {
		dnsRules = append(dnsRules, map[string]interface{}{
			"server": "fakeip-dns",
		})
	} else {
		var fakeipDomains []string
		fakeipDomains = append(fakeipDomains, cfg.CustomDomains...)
		for _, rp := range cfg.RoutePolicies {
			if rp.Enabled && len(rp.Domains) > 0 {
				fakeipDomains = append(fakeipDomains, rp.Domains...)
			}
		}

		if len(fakeipDomains) > 0 {
			dnsRules = append(dnsRules, map[string]interface{}{
				"server":        "fakeip-dns",
				"domain_suffix": fakeipDomains,
			})
		}
		if len(dnsRuleSetList) > 0 {
			dnsRules = append(dnsRules, map[string]interface{}{
				"server":   "fakeip-dns",
				"rule_set": dnsRuleSetList,
			})
		}
	}

	// Спецификация серверов DNS (Sing-Box 1.12 - 1.14+)
	remoteServerEntry := map[string]interface{}{
		"tag":         "remote-dns",
		"type":        remoteDNSType,
		"server":      remoteDNSServer,
		"server_port": remoteDNSPort,
	}

	if net.ParseIP(remoteDNSServer) == nil {
		remoteServerEntry["domain_resolver"] = "bootstrap-dns"
	}

	if remoteServerName != "" {
		remoteServerEntry["server_name"] = remoteServerName
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
			remoteServerEntry,
		},
		"rules":             dnsRules,
		"final":             "remote-dns",
		"strategy":          "ipv4_only",
		"independent_cache": true,
	}

	// 5. Experimental / Clash API
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
				"listen":        "::",
				"listen_port":   tproxyPort,
				"tcp_fast_open": true,
				"udp_fragment":  true,
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

	// 6. Outbounds (узлы, группы, селекторы)
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
			urltestTag := fmt.Sprintf("%s-auto", grp.Tag)
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
				"type":                        "urltest",
				"tag":                         urltestTag,
				"outbounds":                   grp.Nodes,
				"url":                         targetURL,
				"interval":                    interval,
				"tolerance":                   tolerance,
				"idle_timeout":                "30m",
				"interrupt_exist_connections": false,
			})

			selectorList := append([]string{urltestTag}, grp.Nodes...)
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
		urltestTag := "auto"
		selectorTag := "PROXY"

		outbounds = append(outbounds, map[string]interface{}{
			"type":                        "urltest",
			"tag":                         urltestTag,
			"outbounds":                   allNodeTags,
			"url":                         "https://www.gstatic.com/generate_204",
			"interval":                    "3m",
			"tolerance":                   50,
			"idle_timeout":                "30m",
			"interrupt_exist_connections": false,
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

	// 7. Route Rules (сниффинг выполняется здесь)
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

	if activeOutboundTag != "direct-out" {
		if isGlobal {
			routeRules = append(routeRules, map[string]interface{}{
				"action":   "route",
				"inbound":  []string{"tproxy-in"},
				"outbound": activeOutboundTag,
			})
		} else {
			for _, rp := range cfg.RoutePolicies {
				if !rp.Enabled || rp.Outbound == "" {
					continue
				}

				totalPolicySubnets := append([]string(nil), rp.Subnets...)
				var policyRuleSets []string
				for _, rs := range rp.RuleSets {
					cleanRS := strings.ToLower(strings.TrimSpace(rs))
					if cleanRS == "" {
						continue
					}
					policyRuleSets = append(policyRuleSets, cleanRS)
					if subnets, err := b.rulesLoader.GetSubnets(cleanRS); err == nil && len(subnets) > 0 {
						totalPolicySubnets = append(totalPolicySubnets, subnets...)
					}
				}

				if len(totalPolicySubnets) > 0 {
					routeRules = append(routeRules, map[string]interface{}{
						"action":   "route",
						"inbound":  []string{"tproxy-in"},
						"ip_cidr":  totalPolicySubnets,
						"outbound": rp.Outbound,
					})
				}

				if len(rp.Domains) > 0 {
					routeRules = append(routeRules, map[string]interface{}{
						"action":        "route",
						"inbound":       []string{"tproxy-in"},
						"domain_suffix": rp.Domains,
						"outbound":      rp.Outbound,
					})
				}

				if len(policyRuleSets) > 0 {
					routeRules = append(routeRules, map[string]interface{}{
						"action":   "route",
						"inbound":  []string{"tproxy-in"},
						"outbound": rp.Outbound,
						"rule_set": policyRuleSets,
					})
				}
			}

			totalSubnets := append([]string(nil), cfg.CustomSubnets...)
			var defaultRuleSets []string
			hasDiscord := false

			for _, rs := range cfg.RuleSets {
				cleanRS := strings.ToLower(strings.TrimSpace(rs))
				if cleanRS == "" {
					continue
				}
				defaultRuleSets = append(defaultRuleSets, cleanRS)
				if cleanRS == "discord" {
					hasDiscord = true
				}
				subnets, err := b.rulesLoader.GetSubnets(cleanRS)
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

			if len(defaultRuleSets) > 0 {
				routeRules = append(routeRules, map[string]interface{}{
					"action":   "route",
					"inbound":  []string{"tproxy-in"},
					"outbound": activeOutboundTag,
					"rule_set": defaultRuleSets,
				})
			}

			if len(customSRSTags) > 0 {
				routeRules = append(routeRules, map[string]interface{}{
					"action":   "route",
					"inbound":  []string{"tproxy-in"},
					"outbound": activeOutboundTag,
					"rule_set": customSRSTags,
				})
			}
		}
	}

	routeRules = append(routeRules, map[string]interface{}{
		"action":   "route",
		"inbound":  []string{"mixed-in"},
		"outbound": activeOutboundTag,
	})

	// 8. Remote и Local RuleSets
	var ruleSetObjects []map[string]interface{}
	if !isGlobal {
		for _, rs := range allRuleSets {
			srsName := mapToSRSName(rs)
			ruleSetObjects = append(ruleSetObjects, map[string]interface{}{
				"type":            "remote",
				"tag":             rs,
				"format":          "binary",
				"url":             fmt.Sprintf("https://github.com/itdoginfo/allow-domains/releases/latest/download/%s.srs", srsName),
				"download_detour": "direct-out",
				"update_interval": "1d",
			})
		}

		for _, srs := range customSRSObjects {
			ruleSetObjects = append(ruleSetObjects, map[string]interface{}{
				"type":   "local",
				"tag":    srs.tag,
				"format": "binary",
				"path":   srs.path,
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
		return fmt.Errorf("marshal sing-box 1.13 config: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return err
	}

	return os.WriteFile(outputPath, data, 0644)
}

func (b *BuilderV13) buildNodeOutbound(node *config.GenericNode) (map[string]interface{}, error) {
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
