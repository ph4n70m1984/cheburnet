package config

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type UCIStorage struct{}

func NewUCIStorage() *UCIStorage {
	return &UCIStorage{}
}

func generateSecureHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func sanitizeToken(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "'\"`")
	return strings.TrimSpace(s)
}

func unquoteUCIValue(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && ((s[0] == '\'' && s[len(s)-1] == '\'') || (s[0] == '"' && s[len(s)-1] == '"')) {
		s = s[1 : len(s)-1]
	}
	s = strings.ReplaceAll(s, "'\\''", "'")
	return strings.TrimSpace(s)
}

func parseTextLines(raw string) []string {
	var result []string
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if idx := strings.Index(line, "//"); idx != -1 {
			line = strings.TrimSpace(line[:idx])
		}
		if idx := strings.Index(line, "#"); idx != -1 {
			line = strings.TrimSpace(line[:idx])
		}
		parts := strings.FieldsFunc(line, func(r rune) bool {
			return r == ' ' || r == ',' || r == '\t' || r == '\r' || r == '\n'
		})
		for _, p := range parts {
			clean := sanitizeToken(p)
			if clean != "" {
				result = append(result, clean)
			}
		}
	}
	return result
}

func parseCustomSRSRules(rawList []string) []CustomSRSRule {
	var rules []CustomSRSRule
	for _, raw := range rawList {
		raw = sanitizeToken(raw)
		if raw == "" {
			continue
		}

		parts := strings.Split(raw, "|")
		rule := CustomSRSRule{
			Enabled:        true,
			DownloadDetour: "direct",
		}

		if len(parts) == 1 {
			rule.URL = sanitizeToken(parts[0])
			rule.Name = "custom"
		} else if len(parts) == 2 {
			rule.Name = sanitizeToken(parts[0])
			rule.URL = sanitizeToken(parts[1])
		} else if len(parts) >= 3 {
			rule.Name = sanitizeToken(parts[0])
			rule.URL = sanitizeToken(parts[1])
			detour := strings.ToLower(sanitizeToken(parts[2]))
			if detour == "proxy" {
				rule.DownloadDetour = "proxy"
			}
		}

		if rule.URL != "" {
			rules = append(rules, rule)
		}
	}
	return rules
}

type uciCache struct {
	scalars map[string]string
	lists   map[string][]string
	rawShow string
}

func loadUCICache(packageName string) (*uciCache, error) {
	out, err := exec.Command("uci", "-q", "show", packageName).Output()
	if err != nil {
		return nil, err
	}

	cache := &uciCache{
		scalars: make(map[string]string),
		lists:   make(map[string][]string),
		rawShow: string(out),
	}

	scanner := bufio.NewScanner(bytes.NewReader(out))
	var currentKey string
	var currentVal strings.Builder
	inMultiLine := false

	for scanner.Scan() {
		line := scanner.Text()

		if inMultiLine {
			currentVal.WriteString("\n")
			currentVal.WriteString(line)
			trimmed := strings.TrimRight(line, " \t\r")
			if strings.HasSuffix(trimmed, "'") || strings.HasSuffix(trimmed, "\"") {
				inMultiLine = false
				storeUCIEntry(cache, currentKey, currentVal.String())
				currentKey = ""
				currentVal.Reset()
			}
			continue
		}

		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		parts := strings.SplitN(trimmed, "=", 2)
		if len(parts) != 2 {
			continue
		}

		k := strings.TrimSpace(parts[0])
		v := parts[1]

		vTrimmed := strings.TrimSpace(v)
		if (strings.HasPrefix(vTrimmed, "'") && !strings.HasSuffix(vTrimmed[1:], "'")) ||
			(strings.HasPrefix(vTrimmed, "\"") && !strings.HasSuffix(vTrimmed[1:], "\"")) {
			inMultiLine = true
			currentKey = k
			currentVal.WriteString(v)
			continue
		}

		storeUCIEntry(cache, k, v)
	}

	if inMultiLine && currentKey != "" {
		storeUCIEntry(cache, currentKey, currentVal.String())
	}

	return cache, nil
}

func storeUCIEntry(cache *uciCache, k, rawVal string) {
	v := sanitizeToken(rawVal)
	if idx := strings.Index(k, "["); idx != -1 && strings.HasSuffix(k, "]") {
		baseKey := k[:idx]
		if v != "" {
			cache.lists[baseKey] = append(cache.lists[baseKey], v)
		}
	} else {
		cache.scalars[k] = v
	}
}

func (c *uciCache) get(key, def string) string {
	if val, ok := c.scalars[key]; ok && val != "" {
		return val
	}
	return def
}

func (c *uciCache) getInt(key string, def int) int {
	valStr := c.get(key, "")
	if valStr == "" {
		return def
	}
	val, err := strconv.Atoi(valStr)
	if err != nil {
		return def
	}
	return val
}

func (c *uciCache) getList(key string) []string {
	var result []string

	if list, ok := c.lists[key]; ok && len(list) > 0 {
		for _, item := range list {
			tokens := parseTextLines(item)
			result = append(result, tokens...)
		}
	}

	if val, ok := c.scalars[key]; ok && strings.TrimSpace(val) != "" {
		tokens := parseTextLines(val)
		result = append(result, tokens...)
	}

	if len(result) > 0 {
		seen := make(map[string]bool)
		unique := make([]string, 0, len(result))
		for _, item := range result {
			if !seen[item] {
				seen[item] = true
				unique = append(unique, item)
			}
		}
		return unique
	}

	return nil
}

func (u *UCIStorage) parseNodeGroupSections(rawShow string) []NodeFilterGroup {
	var groups []NodeFilterGroup
	secMap := make(map[string]*NodeFilterGroup)
	scanner := bufio.NewScanner(strings.NewReader(rawShow))

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "cheburnet.@node_group[") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		keyParts := strings.Split(parts[0], ".")
		if len(keyParts) < 3 {
			continue
		}

		secID := keyParts[1]
		val := parts[1]

		if _, ok := secMap[secID]; !ok {
			secMap[secID] = &NodeFilterGroup{
				Enabled:  true,
				Priority: 0,
			}
		}

		propName := keyParts[2]
		if idx := strings.Index(propName, "["); idx != -1 {
			propName = propName[:idx]
		}

		cleanVal := sanitizeToken(val)

		switch propName {
		case "name":
			secMap[secID].Name = cleanVal
		case "priority":
			if p, err := strconv.Atoi(cleanVal); err == nil {
				secMap[secID].Priority = p
			}
		case "enabled":
			secMap[secID].Enabled = (cleanVal == "1" || strings.EqualFold(cleanVal, "true"))
		case "regex":
			cleanRegex := unquoteUCIValue(val)
			if cleanRegex != "" {
				secMap[secID].Regex = append(secMap[secID].Regex, cleanRegex)
			}
		}
	}

	for _, g := range secMap {
		if g.Name != "" && g.Enabled && len(g.Regex) > 0 {
			groups = append(groups, *g)
		}
	}
	return groups
}

func (u *UCIStorage) Load() (*CheburConfig, error) {
	cache, err := loadUCICache("cheburnet")
	if err != nil {
		return nil, fmt.Errorf("failed to read uci config: %w", err)
	}

	cfg := &CheburConfig{
		Engine:                cache.get("cheburnet.main.engine", "sing-box"),
		RoutingMode:           cache.get("cheburnet.main.routing_mode", "rules"),
		SourceMode:            cache.get("cheburnet.main.source_mode", "subscription"),
		ConfigType:            cache.get("cheburnet.main.config_type", "urltest"),
		AdaptiveInterval:      cache.get("cheburnet.main.adaptive_interval", "3m"),
		AutoHWID:              cache.get("cheburnet.main.auto_hwid", "1") == "1",
		CustomHWID:            cache.get("cheburnet.main.custom_hwid", ""),
		RulesetUpdateInterval: cache.get("cheburnet.main.ruleset_update_interval", "72h"),
		TProxyPort:            cache.getInt("cheburnet.main.tproxy_port", 1602),
		DNSPort:               cache.getInt("cheburnet.main.dns_port", 53),
		MixedPort:             cache.getInt("cheburnet.main.mixed_port", 4534),
		SourceIface:           cache.get("cheburnet.main.source_interface", "br-lan"),
		DNSProtocol:           cache.get("cheburnet.main.dns_protocol", "udp"),
		DNSServer:             cache.get("cheburnet.main.dns_server", "8.8.8.8"),
		BootstrapDNS:          cache.get("cheburnet.main.bootstrap_dns", "77.88.8.8"),
		DNSTTL:                cache.getInt("cheburnet.main.dns_ttl", 60),
		EnableYACD:            cache.get("cheburnet.main.enable_yacd", "1") == "1",
		AutoUpdate:            cache.get("cheburnet.main.auto_update", "0") == "1",
		UpdateChannel:         cache.get("cheburnet.main.update_channel", "release"),

		URLTestInterval:  cache.get("cheburnet.main.urltest_interval", "3m"),
		URLTestTolerance: cache.getInt("cheburnet.main.urltest_tolerance", 50),
		URLTestURL:       cache.get("cheburnet.main.urltest_url", "http://cp.cloudflare.com/generate_204"),

		ActiveGroup:        cache.get("cheburnet.main.active_group", "auto"),
		AutoFallbackLTE:    cache.get("cheburnet.main.auto_fallback_lte", "0") == "1",
		ScheduleLTEEnabled: cache.get("cheburnet.main.schedule_lte_enabled", "0") == "1",
		ScheduleLTEStart:   cache.get("cheburnet.main.schedule_lte_start", "21:00"),
		ScheduleLTEEnd:     cache.get("cheburnet.main.schedule_lte_end", "07:00"),

		PublicSubEnabled: cache.get("cheburnet.main.public_sub_enabled", "0") == "1",
		PublicSubPort:    cache.getInt("cheburnet.main.public_sub_port", 9443),
		PublicSubToken:   cache.get("cheburnet.main.public_sub_token", ""),
		ClashAPISecret:   cache.get("cheburnet.main.clash_api_secret", ""),

		APIToken: cache.get("cheburnet.main.api_token", ""),
	}

	if cfg.APIToken == "" {
		generatedToken := generateSecureHex(16)
		_ = exec.Command("uci", "set", "cheburnet.main.api_token="+generatedToken).Run()
		_ = exec.Command("uci", "commit", "cheburnet").Run()
		cfg.APIToken = generatedToken
	}

	cfg.NodeGroups = u.parseNodeGroupSections(cache.rawShow)
	cfg.Subscriptions = u.parseSubscriptionSections(cache.rawShow)

	if len(cfg.Subscriptions) == 0 {
		oldSubs := cache.getList("cheburnet.main.subscription")
		for _, raw := range oldSubs {
			val := sanitizeToken(raw)
			if val != "" {
				sub := SubscriptionConfig{
					URL:            val,
					UserAgent:      "Happ/4.1.3 (iPhone; iOS 17.5.1; Scale/3.00)",
					Enabled:        true,
					FilterMode:     "exclude",
					UpdateInterval: "24h",
				}
				sub.CompileFilters()
				cfg.Subscriptions = append(cfg.Subscriptions, sub)
			}
		}
	}

	cfg.ClientPolicies = u.parseClientRuleSections(cache.rawShow)
	cfg.RoutePolicies = u.parseRoutePolicySections(cache.rawShow)

	cfg.ManualNodes = cache.getList("cheburnet.main.manual_nodes")

	rawRuleSets := cache.getList("cheburnet.main.rulesets")
	if len(rawRuleSets) > 0 {
		cfg.RuleSets = rawRuleSets
	} else {
		cfg.RuleSets = []string{"russia_inside", "youtube", "meta", "telegram", "google_ai"}
	}

	cfg.CustomSRSRulesets = parseCustomSRSRules(cache.getList("cheburnet.main.custom_srs_rulesets"))
	cfg.CustomDomains = cache.getList("cheburnet.main.custom_domains")
	cfg.CustomSubnets = cache.getList("cheburnet.main.custom_subnets")
	cfg.CustomPorts = cache.getList("cheburnet.main.custom_ports")
	cfg.LocalListFiles = cache.getList("cheburnet.main.local_list_files")

	return cfg, nil
}

func (u *UCIStorage) SaveCoreSettings(cfg *CheburConfig) error {
	if cfg == nil {
		return fmt.Errorf("cannot persist nil config")
	}

	cmds := [][]string{
		{"set", fmt.Sprintf("cheburnet.main.engine=%s", cfg.Engine)},
		{"set", fmt.Sprintf("cheburnet.main.routing_mode=%s", cfg.RoutingMode)},
		{"set", fmt.Sprintf("cheburnet.main.source_mode=%s", cfg.SourceMode)},
		{"set", fmt.Sprintf("cheburnet.main.config_type=%s", cfg.ConfigType)},
		{"set", fmt.Sprintf("cheburnet.main.adaptive_interval=%s", cfg.AdaptiveInterval)},
		{"set", fmt.Sprintf("cheburnet.main.tproxy_port=%d", cfg.TProxyPort)},
		{"set", fmt.Sprintf("cheburnet.main.dns_port=%d", cfg.DNSPort)},
		{"set", fmt.Sprintf("cheburnet.main.mixed_port=%d", cfg.MixedPort)},
		{"set", fmt.Sprintf("cheburnet.main.update_channel=%s", cfg.UpdateChannel)},
	}

	for _, args := range cmds {
		cmd := exec.Command("uci", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("uci %v failed: %s (%w)", args, string(out), err)
		}
	}

	if out, err := exec.Command("uci", "commit", "cheburnet").CombinedOutput(); err != nil {
		return fmt.Errorf("uci commit cheburnet failed: %s (%w)", string(out), err)
	}

	return nil
}

func (u *UCIStorage) parseSubscriptionSections(rawShow string) []SubscriptionConfig {
	var subs []SubscriptionConfig
	secMap := make(map[string]*SubscriptionConfig)
	scanner := bufio.NewScanner(strings.NewReader(rawShow))

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "cheburnet.@subscription[") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		keyParts := strings.Split(parts[0], ".")
		if len(keyParts) < 3 {
			continue
		}

		secID := keyParts[1]
		val := sanitizeToken(parts[1])

		if _, ok := secMap[secID]; !ok {
			secMap[secID] = &SubscriptionConfig{
				UserAgent:      "Happ/4.1.3 (iPhone; iOS 17.5.1; Scale/3.00)",
				Enabled:        true,
				FilterMode:     "exclude",
				UpdateInterval: "24h",
			}
		}

		propName := keyParts[2]
		if idx := strings.Index(propName, "["); idx != -1 {
			propName = propName[:idx]
		}

		switch propName {
		case "name":
			secMap[secID].Name = val
		case "url":
			secMap[secID].URL = val
		case "user_agent":
			secMap[secID].UserAgent = val
		case "hwid":
			secMap[secID].HWID = val
		case "update_interval":
			secMap[secID].UpdateInterval = val
		case "filter_mode":
			if strings.EqualFold(val, "include") {
				secMap[secID].FilterMode = "include"
			} else {
				secMap[secID].FilterMode = "exclude"
			}
		case "enabled":
			secMap[secID].Enabled = (val == "1" || strings.EqualFold(val, "true"))
		case "exclude_regex":
			rawRight := parts[1]
			if strings.Contains(rawRight, "'") {
				tokens := strings.Split(rawRight, "'")
				for _, token := range tokens {
					item := sanitizeToken(token)
					if item != "" {
						secMap[secID].ExcludeRegex = append(secMap[secID].ExcludeRegex, item)
					}
				}
			} else if val != "" {
				secMap[secID].ExcludeRegex = append(secMap[secID].ExcludeRegex, val)
			}
		}
	}

	for _, sub := range secMap {
		if sub.URL != "" && sub.Enabled {
			if sub.FilterMode == "" {
				sub.FilterMode = "exclude"
			}
			if sub.UpdateInterval == "" {
				sub.UpdateInterval = "24h"
			}
			sub.CompileFilters()
			subs = append(subs, *sub)
		}
	}
	return subs
}

func (u *UCIStorage) parseRoutePolicySections(rawShow string) []RoutePolicy {
	var policies []RoutePolicy
	secMap := make(map[string]*RoutePolicy)
	scanner := bufio.NewScanner(strings.NewReader(rawShow))

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "cheburnet.@route_policy[") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		keyParts := strings.Split(parts[0], ".")
		if len(keyParts) < 3 {
			continue
		}

		secID := keyParts[1]
		val := sanitizeToken(parts[1])

		if _, ok := secMap[secID]; !ok {
			secMap[secID] = &RoutePolicy{
				Enabled: true,
			}
		}

		propName := keyParts[2]
		if idx := strings.Index(propName, "["); idx != -1 {
			propName = propName[:idx]
		}

		switch propName {
		case "name":
			secMap[secID].Name = val
		case "outbound":
			secMap[secID].Outbound = val
		case "enabled":
			secMap[secID].Enabled = (val == "1" || strings.EqualFold(val, "true"))
		case "rulesets":
			rawRight := parts[1]
			if strings.Contains(rawRight, "'") {
				tokens := strings.Split(rawRight, "'")
				for _, token := range tokens {
					item := sanitizeToken(token)
					if item != "" {
						secMap[secID].RuleSets = append(secMap[secID].RuleSets, item)
					}
				}
			} else if val != "" {
				secMap[secID].RuleSets = append(secMap[secID].RuleSets, val)
			}
		case "custom_domains":
			secMap[secID].Domains = append(secMap[secID].Domains, parseTextLines(val)...)
		case "custom_subnets":
			secMap[secID].Subnets = append(secMap[secID].Subnets, parseTextLines(val)...)
		}
	}

	for _, p := range secMap {
		if p.Outbound != "" && p.Enabled && (len(p.RuleSets) > 0 || len(p.Domains) > 0 || len(p.Subnets) > 0) {
			policies = append(policies, *p)
		}
	}
	return policies
}

func (u *UCIStorage) parseClientRuleSections(rawShow string) []ClientPolicy {
	var policies []ClientPolicy
	secMap := make(map[string]*ClientPolicy)
	scanner := bufio.NewScanner(strings.NewReader(rawShow))

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "cheburnet.@client_rule[") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		keyParts := strings.Split(parts[0], ".")
		if len(keyParts) < 3 {
			continue
		}

		secID := keyParts[1]
		val := sanitizeToken(parts[1])

		if _, ok := secMap[secID]; !ok {
			secMap[secID] = &ClientPolicy{
				Enabled: true,
				Mode:    ClientModeRules,
			}
		}

		switch keyParts[2] {
		case "name":
			secMap[secID].Name = val
		case "target":
			secMap[secID].Target = val
		case "mode":
			secMap[secID].Mode = ClientMode(val)
		case "enabled":
			secMap[secID].Enabled = (val == "1" || strings.EqualFold(val, "true"))
		}
	}

	for _, p := range secMap {
		if p.Target != "" && p.Enabled {
			policies = append(policies, *p)
		}
	}
	return policies
}

func (u *UCIStorage) SaveEngine(engineName string) error {
	_ = exec.Command("uci", "set", "cheburnet.main.engine="+engineName).Run()
	return exec.Command("uci", "commit", "cheburnet").Run()
}

func (u *UCIStorage) SaveRuleSets(rulesets []string) error {
	_ = exec.Command("uci", "delete", "cheburnet.main.rulesets").Run()
	for _, rs := range rulesets {
		clean := sanitizeToken(rs)
		if clean != "" {
			_ = exec.Command("uci", "add_list", "cheburnet.main.rulesets="+clean).Run()
		}
	}
	return exec.Command("uci", "commit", "cheburnet").Run()
}

func (u *UCIStorage) AddSubscription(sub SubscriptionConfig) error {
	out, err := exec.Command("uci", "add", "cheburnet", "subscription").Output()
	if err != nil {
		return err
	}
	secID := strings.TrimSpace(string(out))

	ua := sub.UserAgent
	if ua == "" {
		ua = "Happ/4.1.3 (iPhone; iOS 17.5.1; Scale/3.00)"
	}

	filterMode := sub.FilterMode
	if filterMode == "" {
		filterMode = "exclude"
	}

	interval := sub.UpdateInterval
	if interval == "" {
		interval = "24h"
	}

	if sub.Name != "" {
		_ = exec.Command("uci", "set", fmt.Sprintf("cheburnet.%s.name=%s", secID, sub.Name)).Run()
	}
	_ = exec.Command("uci", "set", fmt.Sprintf("cheburnet.%s.url=%s", secID, sub.URL)).Run()
	_ = exec.Command("uci", "set", fmt.Sprintf("cheburnet.%s.user_agent=%s", secID, ua)).Run()
	_ = exec.Command("uci", "set", fmt.Sprintf("cheburnet.%s.filter_mode=%s", secID, filterMode)).Run()
	_ = exec.Command("uci", "set", fmt.Sprintf("cheburnet.%s.update_interval=%s", secID, interval)).Run()
	if sub.HWID != "" {
		_ = exec.Command("uci", "set", fmt.Sprintf("cheburnet.%s.hwid=%s", secID, sub.HWID)).Run()
	}
	_ = exec.Command("uci", "set", fmt.Sprintf("cheburnet.%s.enabled=1", secID)).Run()

	for _, reg := range sub.ExcludeRegex {
		reg = sanitizeToken(reg)
		if reg != "" {
			_ = exec.Command("uci", "add_list", fmt.Sprintf("cheburnet.%s.exclude_regex=%s", secID, reg)).Run()
		}
	}

	return exec.Command("uci", "commit", "cheburnet").Run()
}

func (u *UCIStorage) AddManualNode(rawURI string) error {
	_ = exec.Command("uci", "add_list", "cheburnet.main.manual_nodes="+rawURI).Run()
	return exec.Command("uci", "commit", "cheburnet").Run()
}
