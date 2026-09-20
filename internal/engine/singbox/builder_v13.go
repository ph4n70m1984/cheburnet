package singbox

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
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
	rulesLoader    *network.CompressedRulesetLoader
	rulesetManager *ruleset.Manager
}

func NewBuilderV13() *BuilderV13 {
	return &BuilderV13{
		rulesLoader:    network.NewCompressedRulesetLoader(),
		rulesetManager: ruleset.NewManager(nil, 4534),
	}
}

func (b *BuilderV13) Build(cfg *config.CheburConfig, outputPath string) error {
	clashController := "0.0.0.0:9090"

	bootstrapServer := cfg.BootstrapDNS
	if bootstrapServer == "" {
		bootstrapServer = "77.88.8.8"
	}

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

	cleanCustomDomains := cleanTokens(cfg.CustomDomains)

	if isGlobal {
		dnsRules = append(dnsRules, map[string]interface{}{
			"server": "fakeip-dns",
		})
	} else {
		var fakeipDomains []string
		fakeipDomains = append(fakeipDomains, cleanCustomDomains...)
		for _, rp := range cfg.RoutePolicies {
			if rp.Enabled && len(rp.Domains) > 0 {
				fakeipDomains = append(fakeipDomains, cleanTokens(rp.Domains)...)
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

	clashAPIMap := map[string]interface{}{
		"external_controller": clashController,
		"default_mode":        "rule",
	}
	if trimmedSecret := strings.TrimSpace(cfg.ClashAPISecret); trimmedSecret != "" {
		clashAPIMap["secret"] = trimmedSecret
	}

	experimentalConfig := map[string]interface{}{
		"cache_file": map[string]interface{}{
			"enabled":      true,
			"path":         "/tmp/sing-box-cache.db",
			"store_fakeip": true,
		},
		"clash_api": clashAPIMap,
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

	outbounds := []map[string]interface{}{
		{
			"type": "direct",
			"tag":  "direct-out",
		},
	}

	var allNodeTags []string
	for _, node := range cfg.Nodes {
		ob, err := b.buildNodeOutbound(node)
		if err != nil {
			log.Printf("[WARN] [builder_v13] Skipped node '%s' (protocol: %s): %v", node.Tag, node.Protocol, err)
			continue
		}
		outbounds = append(outbounds, ob)
		allNodeTags = append(allNodeTags, node.Tag)
	}

	log.Printf("[INFO] [builder_v13] Successfully compiled %d/%d nodes into sing-box outbounds", len(allNodeTags), len(cfg.Nodes))

	activeOutboundTag := "direct-out"

	configType := strings.ToLower(strings.TrimSpace(cfg.ConfigType))
	if configType == "" {
		configType = "urltest"
	}

	globalURLTestInterval := strings.TrimSpace(cfg.URLTestInterval)
	if globalURLTestInterval == "" {
		globalURLTestInterval = "3m"
	}

	globalURLTestTolerance := cfg.URLTestTolerance
	if globalURLTestTolerance <= 0 {
		globalURLTestTolerance = 50
	}

	globalURLTestURL := strings.TrimSpace(cfg.URLTestURL)
	if globalURLTestURL == "" {
		globalURLTestURL = "http://cp.cloudflare.com/generate_204"
	}

	if len(cfg.Groups) > 0 {
		for _, grp := range cfg.Groups {
			var validGrpNodes []string
			for _, gn := range grp.Nodes {
				for _, at := range allNodeTags {
					if gn == at {
						validGrpNodes = append(validGrpNodes, gn)
						break
					}
				}
			}

			if len(validGrpNodes) == 0 {
				continue
			}

			urltestTag := fmt.Sprintf("%s-auto", grp.Tag)

			interval := grp.Interval
			if interval == "" {
				interval = globalURLTestInterval
			}

			tolerance := grp.Tolerance
			if tolerance == 0 {
				tolerance = globalURLTestTolerance
			}

			targetURL := grp.TargetURL
			if targetURL == "" {
				targetURL = globalURLTestURL
			}

			outbounds = append(outbounds, map[string]interface{}{
				"type":                        "urltest",
				"tag":                         urltestTag,
				"outbounds":                   validGrpNodes,
				"url":                         targetURL,
				"interval":                    interval,
				"tolerance":                   tolerance,
				"interrupt_exist_connections": false,
			})

			if configType == "urltest" {
				selectorList := append([]string{urltestTag}, validGrpNodes...)
				outbounds = append(outbounds, map[string]interface{}{
					"type":      "selector",
					"tag":       grp.Tag,
					"outbounds": selectorList,
					"default":   urltestTag,
				})
			} else {
				outbounds = append(outbounds, map[string]interface{}{
					"type":      "selector",
					"tag":       grp.Tag,
					"outbounds": validGrpNodes,
					"default":   validGrpNodes[0],
				})
			}

			if activeOutboundTag == "direct-out" {
				activeOutboundTag = grp.Tag
			}
		}
	} else if len(allNodeTags) > 0 {
		urltestTag := "auto"
		selectorTag := "PROXY"

		// urltest ВСЕГДА присутствует в ядре для автоматического наполнения истории задержек в Clash API
		outbounds = append(outbounds, map[string]interface{}{
			"type":                        "urltest",
			"tag":                         urltestTag,
			"outbounds":                   allNodeTags,
			"url":                         globalURLTestURL,
			"interval":                    globalURLTestInterval,
			"tolerance":                   globalURLTestTolerance,
			"interrupt_exist_connections": false,
		})

		if configType == "urltest" {
			selectorList := append([]string{urltestTag}, allNodeTags...)
			outbounds = append(outbounds, map[string]interface{}{
				"type":      "selector",
				"tag":       selectorTag,
				"outbounds": selectorList,
				"default":   urltestTag,
			})
		} else {
			// В адаптивном режиме PROXY содержит только реальные узлы и переключается cheburnetd
			outbounds = append(outbounds, map[string]interface{}{
				"type":      "selector",
				"tag":       selectorTag,
				"outbounds": allNodeTags,
				"default":   allNodeTags[0],
			})
		}

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

	leasesMap := loadDHCPLeasesMap()

	var directClients []string
	var fullProxyClients []string

	for _, cp := range cfg.ClientPolicies {
		if !cp.Enabled || cp.Target == "" {
			continue
		}
		cidr := resolveTargetToCIDRWithLeases(cp.Target, leasesMap)
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
		routeRules = append(routeRules, map[string]interface{}{
			"action":   "route",
			"inbound":  []string{"tproxy-in"},
			"ip_cidr":  []string{"198.18.0.0/15"},
			"outbound": activeOutboundTag,
		})

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

				targetOutbound := rp.Outbound
				if (strings.EqualFold(targetOutbound, "auto") || strings.EqualFold(targetOutbound, "PROXY")) && configType != "urltest" {
					targetOutbound = "PROXY"
				}

				totalPolicySubnets := append([]string(nil), cleanTokens(rp.Subnets)...)
				var policyRuleSets []string
				hasPolicyTelegram := false

				for _, rs := range rp.RuleSets {
					cleanRS := strings.ToLower(strings.TrimSpace(rs))
					if cleanRS == "" {
						continue
					}
					policyRuleSets = append(policyRuleSets, cleanRS)
					if cleanRS == "telegram" {
						hasPolicyTelegram = true
					}
					if subnets, err := b.rulesLoader.GetSubnets(cleanRS); err == nil && len(subnets) > 0 {
						totalPolicySubnets = append(totalPolicySubnets, subnets...)
					}
				}

				if hasPolicyTelegram {
					totalPolicySubnets = append(totalPolicySubnets, getTelegramSubnets()...)
				}

				if len(totalPolicySubnets) > 0 {
					routeRules = append(routeRules, map[string]interface{}{
						"action":   "route",
						"inbound":  []string{"tproxy-in"},
						"ip_cidr":  totalPolicySubnets,
						"outbound": targetOutbound,
					})
				}

				rpDomains := cleanTokens(rp.Domains)
				if len(rpDomains) > 0 {
					routeRules = append(routeRules, map[string]interface{}{
						"action":        "route",
						"inbound":       []string{"tproxy-in"},
						"domain_suffix": rpDomains,
						"outbound":      targetOutbound,
					})
				}

				if len(policyRuleSets) > 0 {
					routeRules = append(routeRules, map[string]interface{}{
						"action":   "route",
						"inbound":  []string{"tproxy-in"},
						"outbound": targetOutbound,
						"rule_set": policyRuleSets,
					})
				}
			}

			totalSubnets := append([]string(nil), cleanTokens(cfg.CustomSubnets)...)
			var defaultRuleSets []string
			hasDiscord := false
			hasTelegram := false

			for _, rs := range cfg.RuleSets {
				cleanRS := strings.ToLower(strings.TrimSpace(rs))
				if cleanRS == "" {
					continue
				}
				defaultRuleSets = append(defaultRuleSets, cleanRS)
				if cleanRS == "discord" {
					hasDiscord = true
				}
				if cleanRS == "telegram" {
					hasTelegram = true
				}
				subnets, err := b.rulesLoader.GetSubnets(cleanRS)
				if err == nil && len(subnets) > 0 {
					totalSubnets = append(totalSubnets, subnets...)
				}
			}

			if hasTelegram {
				totalSubnets = append(totalSubnets, getTelegramSubnets()...)
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

			if len(cleanCustomDomains) > 0 {
				routeRules = append(routeRules, map[string]interface{}{
					"action":        "route",
					"inbound":       []string{"tproxy-in"},
					"domain_suffix": cleanCustomDomains,
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

	var ruleSetObjects []map[string]interface{}
	if !isGlobal {
		for _, rs := range allRuleSets {
			localPath, err := b.rulesetManager.FetchSystemRuleSet(rs)
			if err == nil && localPath != "" {
				ruleSetObjects = append(ruleSetObjects, map[string]interface{}{
					"type":   "local",
					"tag":    rs,
					"format": "binary",
					"path":   localPath,
				})
			} else {
				srsName := ruleset.MapToSRSName(rs)
				ruleSetObjects = append(ruleSetObjects, map[string]interface{}{
					"type":            "remote",
					"tag":             rs,
					"format":          "binary",
					"url":             fmt.Sprintf("https://github.com/itdoginfo/allow-domains/releases/latest/download/%s.srs", srsName),
					"download_detour": "direct-out",
					"update_interval": "1d",
				})
			}
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

	data, err := json.Marshal(sbConfig)
	if err != nil {
		return fmt.Errorf("marshal sing-box 1.13 config: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return err
	}

	tmpPath := outputPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, outputPath)
}

func (b *BuilderV13) buildNodeOutbound(node *config.GenericNode) (map[string]interface{}, error) {
	if node == nil {
		return nil, fmt.Errorf("node is nil")
	}

	tag := strings.TrimSpace(node.Tag)
	addr := strings.TrimSpace(node.Address)
	if tag == "" || addr == "" || node.Port <= 0 {
		return nil, fmt.Errorf("invalid node address or port: tag='%s', addr='%s', port=%d", tag, addr, node.Port)
	}

	proto := strings.ToLower(strings.TrimSpace(node.Protocol))

	out := map[string]interface{}{
		"tag":         tag,
		"server":      addr,
		"server_port": node.Port,
	}

	switch proto {
	case "vless", "vlite":
		out["type"] = "vless"
		out["uuid"] = strings.TrimSpace(node.UUID)
		if node.Flow != "" {
			out["flow"] = strings.TrimSpace(node.Flow)
		}

		sec := strings.ToLower(strings.TrimSpace(node.Security))
		if sec == "tls" || sec == "reality" || node.SNI != "" || node.PublicKey != "" {
			tlsMap := map[string]interface{}{
				"enabled":     true,
				"server_name": strings.TrimSpace(node.SNI),
				"insecure":    node.Insecure,
			}
			if node.Fingerprint != "" {
				tlsMap["utls"] = map[string]interface{}{
					"enabled":     true,
					"fingerprint": strings.TrimSpace(node.Fingerprint),
				}
			}
			if sec == "reality" || node.PublicKey != "" {
				realityMap := map[string]interface{}{
					"enabled":    true,
					"public_key": strings.TrimSpace(node.PublicKey),
					"short_id":   strings.TrimSpace(node.ShortID),
				}
				tlsMap["reality"] = realityMap
			}
			out["tls"] = tlsMap
		}

		netType := strings.ToLower(strings.TrimSpace(node.Network))
		if netType == "ws" {
			out["transport"] = map[string]interface{}{
				"type":    "ws",
				"path":    node.Path,
				"headers": map[string]string{"Host": node.Host},
			}
		} else if netType == "grpc" {
			out["transport"] = map[string]interface{}{
				"type":         "grpc",
				"service_name": node.Path,
			}
		} else if netType == "xhttp" || netType == "splithttp" {
			return nil, fmt.Errorf("skipped: transport '%s' is only supported by extended/lx sing-box builds", netType)
		}

	case "hysteria2", "hy2", "hysteria":
		out["type"] = "hysteria2"
		password := node.Password
		if password == "" {
			password = node.UUID
		}
		out["password"] = strings.TrimSpace(password)

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
			"server_name": strings.TrimSpace(node.SNI),
			"insecure":    node.Insecure,
		}

	case "shadowsocks", "ss":
		out["type"] = "shadowsocks"
		out["method"] = strings.TrimSpace(node.Method)
		out["password"] = strings.TrimSpace(node.Password)

	case "trojan":
		out["type"] = "trojan"
		out["password"] = strings.TrimSpace(node.Password)
		out["tls"] = map[string]interface{}{
			"enabled":     true,
			"server_name": strings.TrimSpace(node.SNI),
			"insecure":    node.Insecure,
		}

	case "socks", "socks5":
		out["type"] = "socks"
		ver := node.SocksVersion
		if ver == "" {
			ver = "5"
		}
		out["version"] = ver
		if node.Username != "" {
			out["username"] = node.Username
			out["password"] = node.Password
		}

	default:
		return nil, fmt.Errorf("unsupported protocol '%s'", node.Protocol)
	}

	return out, nil
}
