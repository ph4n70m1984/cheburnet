package config

type ClientMode string

const (
	ClientModeRules     ClientMode = "rules"      // По спискам (стандартная фильтрация)
	ClientModeFullProxy ClientMode = "full_proxy" // Всё в прокси (любой трафик в туннель)
	ClientModeDirect    ClientMode = "direct"     // Прямой доступ (мимо всех прокси)
)

type ClientPolicy struct {
	Name    string     `json:"name"`
	Target  string     `json:"target"` // IP или MAC
	Mode    ClientMode `json:"mode"`
	Enabled bool       `json:"enabled"`
}

type RoutePolicy struct {
	Name     string   `json:"name"`      // Название секции (например "ai" или "youtube")
	Enabled  bool     `json:"enabled"`   // Включено/выключено
	RuleSets []string `json:"rule_sets"` // Списки правил (.srs), например "google_ai"
	Domains  []string `json:"domains"`   // Пользовательские домены
	Subnets  []string `json:"subnets"`   // Пользовательские подсети
	Outbound string   `json:"outbound"`  // Тег ноды выхода (например "RU-001-1 (vless)" или "AUTO")
}

type GenericNode struct {
	Tag          string `json:"tag"`
	Address      string `json:"address"`
	Port         int    `json:"port"`
	Protocol     string `json:"protocol"` // "vless", "shadowsocks", "trojan", "socks", "hysteria2"
	Method       string `json:"method,omitempty"`
	UUID         string `json:"uuid,omitempty"`
	Password     string `json:"password,omitempty"`
	Username     string `json:"username,omitempty"`
	Flow         string `json:"flow,omitempty"`
	Network      string `json:"network,omitempty"`
	Security     string `json:"security,omitempty"`
	SNI          string `json:"sni,omitempty"`
	Fingerprint  string `json:"fingerprint,omitempty"`
	PublicKey    string `json:"public_key,omitempty"`
	ShortID      string `json:"short_id,omitempty"`
	Path         string `json:"path,omitempty"`
	Host         string `json:"host,omitempty"`
	HWID         string `json:"hwid,omitempty"`
	Insecure     bool   `json:"insecure,omitempty"`
	ObfsType     string `json:"obfs_type,omitempty"`
	ObfsPassword string `json:"obfs_password,omitempty"`
	PortRange    string `json:"port_range,omitempty"`
	SocksVersion string `json:"socks_version,omitempty"`
}

type BalancingGroup struct {
	Tag       string   `json:"tag"`
	Strategy  string   `json:"strategy"`
	Nodes     []string `json:"nodes"`
	TargetURL string   `json:"target_url"`
	Interval  string   `json:"interval"`
	Tolerance int      `json:"tolerance"`
}

type SubscriptionConfig struct {
	Name           string   `json:"name"`
	URL            string   `json:"url"`
	UserAgent      string   `json:"user_agent"`
	HWID           string   `json:"hwid,omitempty"`
	Enabled        bool     `json:"enabled"`
	FilterMode     string   `json:"filter_mode,omitempty"`     // "exclude" (Blacklist) или "include" (Whitelist)
	ExcludeRegex   []string `json:"exclude_regex,omitempty"`   // Регулярные выражения фильтра
	UpdateInterval string   `json:"update_interval,omitempty"` // "1h", "3h", "6h", "12h", "24h"
}

type CustomSRSRule struct {
	Name           string `json:"name"`
	URL            string `json:"url"`
	DownloadDetour string `json:"download_detour"` // "direct" или "proxy"
	Enabled        bool   `json:"enabled"`
}

type CheburConfig struct {
	Engine                string               `json:"engine"`       // Всегда "sing-box"
	RoutingMode           string               `json:"routing_mode"` // "rules" или "global"
	SourceMode            string               `json:"source_mode"`
	AutoHWID              bool                 `json:"auto_hwid"`
	CustomHWID            string               `json:"custom_hwid"`
	AutoUpdate            bool                 `json:"auto_update"`
	UpdateChannel         string               `json:"update_channel"`          // "release" или "beta"
	RulesetUpdateInterval string               `json:"ruleset_update_interval"` // "24h", "72h", "168h"
	TProxyPort            int                  `json:"tproxy_port"`
	DNSPort               int                  `json:"dns_port"`
	MixedPort             int                  `json:"mixed_port"`
	SourceIface           string               `json:"source_iface"`
	DNSProtocol           string               `json:"dns_protocol"`
	DNSServer             string               `json:"dns_server"`
	BootstrapDNS          string               `json:"bootstrap_dns"`
	DNSTTL                int                  `json:"dns_ttl"`
	EnableYACD            bool                 `json:"enable_yacd"`
	URLTestInterval       string               `json:"urltest_interval"`  // Интервал тестирования задержки (например, "3m")
	URLTestTolerance      int                  `json:"urltest_tolerance"` // Допуск задержки в миллисекундах (например, 50)
	URLTestURL            string               `json:"urltest_url"`       // URL проверки доступности (generate_204)
	Nodes                 []*GenericNode       `json:"nodes"`
	Groups                []*BalancingGroup    `json:"groups"`
	Subscriptions         []SubscriptionConfig `json:"subscriptions"`
	ManualNodes           []string             `json:"manual_nodes"`
	RuleSets              []string             `json:"rule_sets"`
	CustomSRSRulesets     []CustomSRSRule      `json:"custom_srs_rulesets"` // Пользовательские бинарные SRS
	CustomDomains         []string             `json:"custom_domains"`      // Введенные вручную домены
	CustomSubnets         []string             `json:"custom_subnets"`      // Введенные вручную IP/CIDR
	CustomPorts           []string             `json:"custom_ports"`        // Введенные вручную порты и диапазоны
	LocalListFiles        []string             `json:"local_list_files"`    // Пути к .lst файлам на роутере
	ClientPolicies        []ClientPolicy       `json:"client_policies"`     // Правила маршрутизации по клиентам
	RoutePolicies         []RoutePolicy        `json:"route_policies"`      // Секции маршрутизации по сервисам
}

func (c *CheburConfig) Clone() *CheburConfig {
	if c == nil {
		return nil
	}

	// 1. Поверхностное копирование скаляров (string, int, bool, включая UpdateChannel)
	cp := *c

	// 2. Срезы строк
	if c.ManualNodes != nil {
		cp.ManualNodes = append([]string(nil), c.ManualNodes...)
	}
	if c.RuleSets != nil {
		cp.RuleSets = append([]string(nil), c.RuleSets...)
	}
	if c.CustomDomains != nil {
		cp.CustomDomains = append([]string(nil), c.CustomDomains...)
	}
	if c.CustomSubnets != nil {
		cp.CustomSubnets = append([]string(nil), c.CustomSubnets...)
	}
	if c.CustomPorts != nil {
		cp.CustomPorts = append([]string(nil), c.CustomPorts...)
	}
	if c.LocalListFiles != nil {
		cp.LocalListFiles = append([]string(nil), c.LocalListFiles...)
	}

	// 3. CustomSRSRulesets ([]CustomSRSRule)
	if c.CustomSRSRulesets != nil {
		cp.CustomSRSRulesets = append([]CustomSRSRule(nil), c.CustomSRSRulesets...)
	}

	// 4. Nodes ([]*GenericNode)
	if c.Nodes != nil {
		cp.Nodes = make([]*GenericNode, len(c.Nodes))
		for i, n := range c.Nodes {
			if n != nil {
				nodeCopy := *n
				cp.Nodes[i] = &nodeCopy
			}
		}
	}

	// 5. Groups ([]*BalancingGroup) со вложенным срезом Nodes
	if c.Groups != nil {
		cp.Groups = make([]*BalancingGroup, len(c.Groups))
		for i, g := range c.Groups {
			if g != nil {
				grpCopy := *g
				if g.Nodes != nil {
					grpCopy.Nodes = append([]string(nil), g.Nodes...)
				}
				cp.Groups[i] = &grpCopy
			}
		}
	}

	// 6. Subscriptions ([]SubscriptionConfig) со вложенным ExcludeRegex
	if c.Subscriptions != nil {
		cp.Subscriptions = make([]SubscriptionConfig, len(c.Subscriptions))
		for i, s := range c.Subscriptions {
			subCopy := s
			if s.ExcludeRegex != nil {
				subCopy.ExcludeRegex = append([]string(nil), s.ExcludeRegex...)
			}
			cp.Subscriptions[i] = subCopy
		}
	}

	// 7. ClientPolicies ([]ClientPolicy)
	if c.ClientPolicies != nil {
		cp.ClientPolicies = append([]ClientPolicy(nil), c.ClientPolicies...)
	}

	// 8. RoutePolicies ([]RoutePolicy) со всеми вложенными срезами строк
	if c.RoutePolicies != nil {
		cp.RoutePolicies = make([]RoutePolicy, len(c.RoutePolicies))
		for i, rp := range c.RoutePolicies {
			rpCopy := rp
			if rp.RuleSets != nil {
				rpCopy.RuleSets = append([]string(nil), rp.RuleSets...)
			}
			if rp.Domains != nil {
				rpCopy.Domains = append([]string(nil), rp.Domains...)
			}
			if rp.Subnets != nil {
				rpCopy.Subnets = append([]string(nil), rp.Subnets...)
			}
			cp.RoutePolicies[i] = rpCopy
		}
	}

	return &cp
}
