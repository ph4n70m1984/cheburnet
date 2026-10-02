package singbox

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"cheburnet/internal/config"
	"cheburnet/internal/network"
	"cheburnet/internal/ruleset"
)

type BuilderV14 struct {
	rulesLoader    *network.CompressedRulesetLoader
	rulesetManager *ruleset.Manager
	binPath        string
}

func NewBuilderV14() *BuilderV14 {
	return &BuilderV14{
		rulesLoader:    network.NewCompressedRulesetLoader(),
		rulesetManager: ruleset.NewManager(nil, 4534),
	}
}

func NewBuilderV14WithBin(binPath string) *BuilderV14 {
	b := NewBuilderV14()
	b.binPath = binPath
	if binPath != "" {
		SetBinaryPath(binPath)
	}
	return b
}

func (b *BuilderV14) supportsX25519MLKEM() bool {
	return SupportsX25519MLKEM(b.binPath)
}

func (b *BuilderV14) Build(cfg *config.CheburConfig, outputPath string) error {
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

	dnsRules = append(dnsRules, map[string]interface{}{
		"server": "fakeip-dns",
	})

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

	groupIfaceMap := make(map[string]string)
	groupRegexMap := make(map[string][]*regexp.Regexp)
	for _, ng := range cfg.NodeGroups {
		if !ng.Enabled {
			continue
		}
		if ng.BindInterface != "" {
			groupIfaceMap[ng.Name] = ng.BindInterface
		}
		var regs []*regexp.Regexp
		for _, rStr := range ng.Regex {
			if rStr != "" {
				if re, err := regexp.Compile("(?i)" + rStr); err == nil {
					regs = append(regs, re)
				}
			}
		}
		if len(regs) > 0 {
			groupRegexMap[ng.Name] = regs
		}
	}

	var allNodeTags []string
	skippedCount := 0

	for _, node := range cfg.Nodes {
		nodeBindIface := strings.TrimSpace(node.BindInterface)
		if nodeBindIface == "" {
			for grpName, regs := range groupRegexMap {
				for _, re := range regs {
					if re.MatchString(node.Tag) {
						if iface, ok := groupIfaceMap[grpName]; ok {
							nodeBindIface = iface
							break
						}
					}
				}
				if nodeBindIface != "" {
					break
				}
			}
		}

		ob, err := b.buildNodeOutbound(node, nodeBindIface)
		if err != nil {
			log.Printf("[WARN] [builder_v14] Skipped node '%s' (protocol: %s): %v", node.Tag, node.Protocol, err)
			skippedCount++
			continue
		}
		outbounds = append(outbounds, ob)
		allNodeTags = append(allNodeTags, node.Tag)
	}

	if skippedCount > 0 {
		log.Printf("[INFO] [builder_v14] Successfully compiled %d/%d nodes into sing-box outbounds (%d unsupported nodes skipped)",
			len(allNodeTags), len(cfg.Nodes), skippedCount)
	} else {
		log.Printf("[INFO] [builder_v14] Successfully compiled %d/%d nodes into sing-box outbounds",
			len(allNodeTags), len(cfg.Nodes))
	}

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

	createdGroups := make(map[string]bool)

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
				"interrupt_exist_connections": true,
			})

			selectorList := append([]string{urltestTag}, validGrpNodes...)
			outbounds = append(outbounds, map[string]interface{}{
				"type":                        "selector",
				"tag":                         grp.Tag,
				"outbounds":                   selectorList,
				"default":                     urltestTag,
				"interrupt_exist_connections": true,
			})
			createdGroups[grp.Tag] = true

			if activeOutboundTag == "direct-out" {
				activeOutboundTag = grp.Tag
			}
		}
	} else if len(cfg.NodeGroups) > 0 {
		for _, ng := range cfg.NodeGroups {
			if !ng.Enabled {
				continue
			}
			var matchedNodes []string
			regs := groupRegexMap[ng.Name]
			for _, tag := range allNodeTags {
				for _, re := range regs {
					if re.MatchString(tag) {
						matchedNodes = append(matchedNodes, tag)
						break
					}
				}
			}
			if len(matchedNodes) == 0 {
				continue
			}

			urltestTag := fmt.Sprintf("%s-auto", ng.Name)
			outbounds = append(outbounds, map[string]interface{}{
				"type":                        "urltest",
				"tag":                         urltestTag,
				"outbounds":                   matchedNodes,
				"url":                         globalURLTestURL,
				"interval":                    globalURLTestInterval,
				"tolerance":                   globalURLTestTolerance,
				"interrupt_exist_connections": true,
			})

			selectorList := append([]string{urltestTag}, matchedNodes...)
			outbounds = append(outbounds, map[string]interface{}{
				"type":                        "selector",
				"tag":                         ng.Name,
				"outbounds":                   selectorList,
				"default":                     urltestTag,
				"interrupt_exist_connections": true,
			})
			createdGroups[ng.Name] = true
		}
	}

	if len(allNodeTags) > 0 {
		urltestTag := "auto"
		selectorTag := config.MainSelectorTag

		outbounds = append(outbounds, map[string]interface{}{
			"type":                        "urltest",
			"tag":                         urltestTag,
			"outbounds":                   allNodeTags,
			"url":                         globalURLTestURL,
			"interval":                    globalURLTestInterval,
			"tolerance":                   globalURLTestTolerance,
			"interrupt_exist_connections": true,
		})

		if configType == "urltest" {
			selectorList := append([]string{urltestTag}, allNodeTags...)
			outbounds = append(outbounds, map[string]interface{}{
				"type":                        "selector",
				"tag":                         selectorTag,
				"outbounds":                   selectorList,
				"default":                     urltestTag,
				"interrupt_exist_connections": true,
			})
		} else {
			outbounds = append(outbounds, map[string]interface{}{
				"type":                        "selector",
				"tag":                         selectorTag,
				"outbounds":                   allNodeTags,
				"default":                     allNodeTags[0],
				"interrupt_exist_connections": true,
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

	for _, cp := range cfg.ClientPolicies {
		if !cp.Enabled || cp.Target == "" {
			continue
		}
		cidr := resolveTargetToCIDRWithLeases(cp.Target, leasesMap)
		if cidr == "" {
			continue
		}

		if cp.Mode == config.ClientModeDirect {
			routeRules = append(routeRules, map[string]interface{}{
				"action":         "route",
				"inbound":        []string{"tproxy-in"},
				"source_ip_cidr": []string{cidr},
				"outbound":       "direct-out",
			})
			continue
		}

		targetOutbound := activeOutboundTag
		if trimmed := strings.TrimSpace(cp.Outbound); trimmed != "" {
			targetOutbound = trimmed
		}

		if cp.Mode == config.ClientModeFullProxy {
			routeRules = append(routeRules, map[string]interface{}{
				"action":         "route",
				"inbound":        []string{"tproxy-in"},
				"source_ip_cidr": []string{cidr},
				"outbound":       targetOutbound,
			})
		}
	}

	for _, rp := range cfg.RoutePolicies {
		if !rp.Enabled || rp.Outbound == "" {
			continue
		}

		targetOutbound := rp.Outbound
		if strings.EqualFold(targetOutbound, "direct") {
			targetOutbound = "direct-out"
		} else if (strings.EqualFold(targetOutbound, "auto") || strings.EqualFold(targetOutbound, config.MainSelectorTag)) && configType != "urltest" {
			targetOutbound = config.MainSelectorTag
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

		if len(policyRuleSets) > 0 {
			routeRules = append(routeRules, map[string]interface{}{
				"action":   "route",
				"inbound":  []string{"tproxy-in"},
				"outbound": targetOutbound,
				"rule_set": policyRuleSets,
			})
		}
	}

	if isGlobal {
		if activeOutboundTag != "direct-out" {
			routeRules = append(routeRules, map[string]interface{}{
				"action":   "route",
				"inbound":  []string{"tproxy-in"},
				"outbound": activeOutboundTag,
			})
		}
	} else {
		if len(cleanCustomDomains) > 0 {
			routeRules = append(routeRules, map[string]interface{}{
				"action":        "route",
				"inbound":       []string{"tproxy-in"},
				"domain_suffix": cleanCustomDomains,
				"outbound":      activeOutboundTag,
			})
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

		if !isGlobal {
			routeRules = append(routeRules, map[string]interface{}{
				"action":   "route",
				"inbound":  []string{"tproxy-in"},
				"ip_cidr":  []string{"198.18.0.0/15"},
				"outbound": "direct-out",
			})
		} else if activeOutboundTag != "direct-out" {
			routeRules = append(routeRules, map[string]interface{}{
				"action":   "route",
				"inbound":  []string{"tproxy-in"},
				"ip_cidr":  []string{"198.18.0.0/15"},
				"outbound": activeOutboundTag,
			})
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
		return fmt.Errorf("marshal sing-box 1.14 config: %w", err)
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

func (b *BuilderV14) buildNodeOutbound(node *config.GenericNode, bindIface string) (map[string]interface{}, error) {
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

	if bindIface != "" {
		out["bind_interface"] = bindIface
	}

	tagLower := strings.ToLower(tag)
	addrLower := strings.ToLower(addr)
	hostLower := strings.ToLower(node.Host)
	sniLower := strings.ToLower(node.SNI)

	isBridge := strings.Contains(tagLower, "bridge") ||
		strings.Contains(addrLower, "bridge") ||
		strings.Contains(hostLower, "bridge") ||
		strings.Contains(sniLower, "bridge")

	switch proto {
	case "vless", "vlite":
		out["type"] = "vless"
		out["uuid"] = strings.TrimSpace(node.UUID)
		if node.Flow != "" {
			out["flow"] = strings.TrimSpace(node.Flow)
		}

		sec := strings.ToLower(strings.TrimSpace(node.Security))
		netType := strings.ToLower(strings.TrimSpace(node.Network))

		if sec == "tls" || sec == "reality" || node.SNI != "" || node.PublicKey != "" {
			isInsecure := node.Insecure
			if isBridge {
				isInsecure = true
			}

			tlsMap := map[string]interface{}{
				"enabled":     true,
				"server_name": strings.TrimSpace(node.SNI),
				"insecure":    isInsecure,
			}
			if node.Fingerprint != "" {
				tlsMap["utls"] = map[string]interface{}{
					"enabled":     true,
					"fingerprint": strings.TrimSpace(node.Fingerprint),
				}
			}

			if (netType == "xhttp" || netType == "splithttp") && !isBridge {
				tlsMap["alpn"] = []string{"h2"}
			}

			if sec == "reality" || node.PublicKey != "" {
				realityMap := map[string]interface{}{
					"enabled":    true,
					"public_key": strings.TrimSpace(node.PublicKey),
					"short_id":   strings.TrimSpace(node.ShortID),
				}
				if b.supportsX25519MLKEM() {
					realityMap["support_x25519mlkem768"] = true
				}
				tlsMap["reality"] = realityMap
			}
			out["tls"] = tlsMap
		}

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
			tr, err := buildXHTTPTransport(node, b.binPath)
			if err != nil {
				return nil, err
			}
			out["transport"] = tr
			out["packet_encoding"] = ""
			delete(out, "flow")
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
		netType := strings.ToLower(strings.TrimSpace(node.Network))
		isBridge := strings.Contains(strings.ToLower(node.Tag), "bridge") || strings.Contains(strings.ToLower(node.Address), "bridge")

		tlsMap := map[string]interface{}{
			"enabled":     true,
			"server_name": strings.TrimSpace(node.SNI),
			"insecure":    node.Insecure || isBridge,
		}
		if (netType == "xhttp" || netType == "splithttp") && !isBridge {
			tlsMap["alpn"] = []string{"h2"}
		}
		out["tls"] = tlsMap

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
			tr, err := buildXHTTPTransport(node, b.binPath)
			if err != nil {
				return nil, err
			}
			out["transport"] = tr
			out["packet_encoding"] = ""
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
