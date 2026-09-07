package config

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type UCIStorage struct{}

func NewUCIStorage() *UCIStorage {
	return &UCIStorage{}
}

func parseTextLines(raw string) []string {
	var result []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if idx := strings.Index(line, "//"); idx != -1 {
			line = strings.TrimSpace(line[:idx])
		}
		if idx := strings.Index(line, "#"); idx != -1 {
			line = strings.TrimSpace(line[:idx])
		}
		parts := strings.FieldsFunc(line, func(r rune) bool {
			return r == ' ' || r == ',' || r == '\t'
		})
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				result = append(result, p)
			}
		}
	}
	return result
}

func (u *UCIStorage) Load() (*CheburConfig, error) {
	cfg := &CheburConfig{
		Engine:       u.get("cheburnet.main.engine", "sing-box"),
		RoutingMode:  u.get("cheburnet.main.routing_mode", "rules"),
		SourceMode:   u.get("cheburnet.main.source_mode", "subscription"),
		AutoHWID:     u.get("cheburnet.main.auto_hwid", "1") == "1",
		CustomHWID:   u.get("cheburnet.main.custom_hwid", ""),
		TProxyPort:   u.getInt("cheburnet.main.tproxy_port", 1602),
		DNSPort:      u.getInt("cheburnet.main.dns_port", 53),
		MixedPort:    u.getInt("cheburnet.main.mixed_port", 4534),
		SourceIface:  u.get("cheburnet.main.source_interface", "br-lan"),
		DNSProtocol:  u.get("cheburnet.main.dns_protocol", "udp"),
		DNSServer:    u.get("cheburnet.main.dns_server", "8.8.8.8"),
		BootstrapDNS: u.get("cheburnet.main.bootstrap_dns", "77.88.8.8"),
		DNSTTL:       u.getInt("cheburnet.main.dns_ttl", 60),
		EnableYACD:   u.get("cheburnet.main.enable_yacd", "1") == "1",
		AutoUpdate:   u.get("cheburnet.main.auto_update", "0") == "1",
	}

	// 1. Чтение типизированных секций 'subscription' (новый формат с user_agent и hwid)
	cfg.Subscriptions = u.loadSubscriptionSections()

	// 2. Обратная совместимость: чтение старого 'list subscription' из main
	if len(cfg.Subscriptions) == 0 {
		if out, err := exec.Command("uci", "-q", "get", "cheburnet.main.subscription").Output(); err == nil {
			lines := strings.Fields(string(out))
			for _, raw := range lines {
				val := strings.TrimSpace(raw)
				if val != "" {
					cfg.Subscriptions = append(cfg.Subscriptions, SubscriptionConfig{
						URL:       val,
						UserAgent: "Happ/4.1.3 (iPhone; iOS 17.5.1; Scale/3.00)",
						Enabled:   true,
					})
				}
			}
		}
	}

	// Чтение списка одиночных ссылок нод (vless://, hy2:// и др.)
	if out, err := exec.Command("uci", "-q", "get", "cheburnet.main.manual_nodes").Output(); err == nil {
		lines := strings.Fields(string(out))
		for _, raw := range lines {
			val := strings.TrimSpace(raw)
			if val != "" {
				cfg.ManualNodes = append(cfg.ManualNodes, val)
			}
		}
	}

	// Чтение наборов правил (.srs)
	if out, err := exec.Command("uci", "-q", "get", "cheburnet.main.rulesets").Output(); err == nil {
		trimmed := strings.TrimSpace(string(out))
		if len(trimmed) > 0 {
			lines := strings.Fields(trimmed)
			cfg.RuleSets = append(cfg.RuleSets, lines...)
		} else {
			cfg.RuleSets = []string{"russia_inside", "youtube", "meta", "telegram", "google_ai"}
		}
	} else {
		cfg.RuleSets = []string{"russia_inside", "youtube", "meta", "telegram", "google_ai"}
	}

	// Чтение кастомных доменов и подсетей
	cfg.CustomDomains = parseTextLines(u.get("cheburnet.main.custom_domains", ""))
	cfg.CustomSubnets = parseTextLines(u.get("cheburnet.main.custom_subnets", ""))

	// Чтение путей к локальным файлам .lst
	if out, err := exec.Command("uci", "-q", "get", "cheburnet.main.local_list_files").Output(); err == nil {
		lines := strings.Fields(string(out))
		for _, raw := range lines {
			val := strings.TrimSpace(raw)
			if val != "" {
				cfg.LocalListFiles = append(cfg.LocalListFiles, val)
			}
		}
	}

	return cfg, nil
}

func (u *UCIStorage) loadSubscriptionSections() []SubscriptionConfig {
	var subs []SubscriptionConfig
	out, err := exec.Command("uci", "-q", "show", "cheburnet").Output()
	if err != nil {
		return subs
	}

	secMap := make(map[string]*SubscriptionConfig)
	lines := strings.Split(string(out), "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
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
		val := strings.Trim(parts[1], "'\"")

		if _, ok := secMap[secID]; !ok {
			secMap[secID] = &SubscriptionConfig{
				UserAgent: "Happ/4.1.3 (iPhone; iOS 17.5.1; Scale/3.00)",
				Enabled:   true,
			}
		}

		switch keyParts[2] {
		case "url":
			secMap[secID].URL = val
		case "user_agent":
			secMap[secID].UserAgent = val
		case "hwid":
			secMap[secID].HWID = val
		case "enabled":
			secMap[secID].Enabled = (val == "1" || val == "true")
		}
	}

	for _, sub := range secMap {
		if sub.URL != "" && sub.Enabled {
			subs = append(subs, *sub)
		}
	}
	return subs
}

func (u *UCIStorage) SaveEngine(engineName string) error {
	_ = exec.Command("uci", "set", "cheburnet.main.engine="+engineName).Run()
	return exec.Command("uci", "commit", "cheburnet").Run()
}

func (u *UCIStorage) SaveRuleSets(rulesets []string) error {
	_ = exec.Command("uci", "delete", "cheburnet.main.rulesets").Run()
	for _, rs := range rulesets {
		_ = exec.Command("uci", "add_list", "cheburnet.main.rulesets="+rs).Run()
	}
	return exec.Command("uci", "commit", "cheburnet").Run()
}

func (u *UCIStorage) get(key, def string) string {
	out, err := exec.Command("uci", "-q", "get", key).Output()
	if err != nil {
		return def
	}
	val := strings.TrimSpace(string(out))
	if val == "" {
		return def
	}
	return val
}

func (u *UCIStorage) getInt(key string, def int) int {
	valStr := u.get(key, "")
	if valStr == "" {
		return def
	}
	val, err := strconv.Atoi(valStr)
	if err != nil {
		return def
	}
	return val
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

	_ = exec.Command("uci", "set", fmt.Sprintf("cheburnet.%s.url=%s", secID, sub.URL)).Run()
	_ = exec.Command("uci", "set", fmt.Sprintf("cheburnet.%s.user_agent=%s", secID, ua)).Run()
	if sub.HWID != "" {
		_ = exec.Command("uci", "set", fmt.Sprintf("cheburnet.%s.hwid=%s", secID, sub.HWID)).Run()
	}
	_ = exec.Command("uci", "set", fmt.Sprintf("cheburnet.%s.enabled=1", secID)).Run()

	return exec.Command("uci", "commit", "cheburnet").Run()
}

func (u *UCIStorage) AddManualNode(rawURI string) error {
	_ = exec.Command("uci", "add_list", "cheburnet.main.manual_nodes="+rawURI).Run()
	return exec.Command("uci", "commit", "cheburnet").Run()
}
