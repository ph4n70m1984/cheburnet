package singbox

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"cheburnet/internal/config"
	"cheburnet/internal/network"
	"cheburnet/internal/ruleset"
)

type Builder struct {
	rulesLoader    *network.CompressedRulesetLoader
	rulesetManager *ruleset.Manager
}

func NewBuilder() *Builder {
	return &Builder{
		rulesLoader:    network.NewCompressedRulesetLoader(),
		rulesetManager: ruleset.NewManager(nil, 4534),
	}
}

func cleanTokens(items []string) []string {
	var res []string
	for _, item := range items {
		lines := strings.FieldsFunc(item, func(r rune) bool {
			return r == '\n' || r == '\r' || r == ' ' || r == '\t' || r == ','
		})
		for _, l := range lines {
			l = strings.TrimSpace(l)
			l = strings.Trim(l, "'\"`")
			if l != "" {
				res = append(res, l)
			}
		}
	}
	return res
}

func detectSingBoxVersion() (major, minor, patch int) {
	binPath := "/usr/bin/sing-box"
	if _, err := os.Stat(binPath); err != nil {
		binPath = "sing-box"
	}
	out, err := exec.Command(binPath, "version").Output()
	if err != nil {
		return 1, 12, 0
	}

	fields := strings.Fields(string(out))
	if len(fields) >= 3 {
		rawVer := strings.TrimPrefix(fields[2], "v")
		parts := strings.Split(rawVer, ".")
		if len(parts) >= 2 {
			major, _ = strconv.Atoi(parts[0])
			minor, _ = strconv.Atoi(parts[1])
			if len(parts) >= 3 {
				subParts := strings.Split(parts[2], "-")
				patch, _ = strconv.Atoi(subParts[0])
			}
			return major, minor, patch
		}
	}
	return 1, 12, 0
}

func loadDHCPLeasesMap() map[string]string {
	leases := make(map[string]string)
	file, err := os.Open("/tmp/dhcp.leases")
	if err != nil {
		return leases
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 3 {
			mac := strings.ToLower(fields[1])
			ip := fields[2]
			leases[mac] = ip
		}
	}
	return leases
}

func parsePortsAndRanges(rawPorts []string) ([]uint16, []string) {
	var singlePorts []uint16
	var portRanges []string

	for _, p := range cleanTokens(rawPorts) {
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

func resolveTargetToCIDRWithLeases(target string, leases map[string]string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}

	if strings.Contains(target, ":") && !strings.Contains(target, ".") {
		if ip, exists := leases[strings.ToLower(target)]; exists {
			target = ip
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

func mapToSRSName(rs string) string {
	rs = strings.ToLower(strings.TrimSpace(rs))
	switch rs {
	case "google_ai", "google-ai":
		return "google_ai"
	case "russia_inside", "russia-inside":
		return "russia_inside"
	case "russia_outside", "russia-outside":
		return "russia_outside"
	case "ukraine_inside", "ukraine-inside":
		return "ukraine_inside"
	case "google_meet", "google-meet":
		return "google_meet"
	case "google_play", "google-play":
		return "google_play"
	default:
		return strings.ReplaceAll(rs, "-", "_")
	}
}

func (b *Builder) Build(cfg *config.CheburConfig, outputPath string) error {
	clashController := "0.0.0.0:9090"

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

	cleanCustomDomains := cleanTokens(cfg.CustomDomains)

	if isGlobal {
		dnsRules = append(dnsRules, map[string]interface{}{
			"action": "route",
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
				"action":        "route",
				"server":        "fakeip-dns",
				"domain_suffix": fakeipDomains,
			})
		}
		if len(dnsRuleSetList) > 0 {
			dnsRules = append(dnsRules, map[string]interface{}{
				"action":   "route",
				"server":   "fakeip-dns",
				"rule_set": dnsRuleSetList,
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
		} else {
			log.Printf("[builder] WARN: Skipping node '%s': %v", node.Tag, err)
		}
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

	// 1. ПРИОРИТЕТНЫЕ ПОЛЬЗОВАТЕЛЬСКИЕ ПРАВИЛА (Route Policies)
	for _, rp := range cfg.RoutePolicies {
		if !rp.Enabled || rp.Outbound == "" {
			continue
		}

		targetOutbound := rp.Outbound
		if strings.EqualFold(targetOutbound, "direct") {
			targetOutbound = "direct-out"
		} else if (strings.EqualFold(targetOutbound, "auto") || strings.EqualFold(targetOutbound, "PROXY")) && configType != "urltest" {
			targetOutbound = "PROXY"
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
		// 2. ПОЛЬЗОВАТЕЛЬСКИЕ ДОМЕНЫ И СЕТИ ПО УМОЛЧАНИЮ
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

		// 3. ПЕРЕХВАТ ОСТАВШИХСЯ FAKE-IP
		if activeOutboundTag != "direct-out" {
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
		return fmt.Errorf("marshal sing-box config: %w", err)
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

		netType := strings.ToLower(strings.TrimSpace(node.Network))
		if netType == "ws" || netType == "websocket" {
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
			tr, err := buildXHTTPTransport(node)
			if err != nil {
				return nil, err
			}
			out["transport"] = tr
			delete(out, "flow")
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

		netType := strings.ToLower(strings.TrimSpace(node.Network))
		if netType == "ws" || netType == "websocket" {
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
			tr, err := buildXHTTPTransport(node)
			if err != nil {
				return nil, err
			}
			out["transport"] = tr
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
