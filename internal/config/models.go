package config

import (
	"regexp"
	"strings"
)

// MainSelectorTag задает имя главного селектора прокси в sing-box.
// Используется синхронно во всех билдерах (v12/v13/v14), StateController и AdaptiveWorker[cite: 10, 11].
const MainSelectorTag = "PROXY"

type ClientMode string

const (
	ClientModeRules     ClientMode = "rules"
	ClientModeFullProxy ClientMode = "full_proxy"
	ClientModeDirect    ClientMode = "direct"
)

type ClientPolicy struct {
	Name    string     `json:"name"`
	Target  string     `json:"target"`
	Mode    ClientMode `json:"mode"`
	Enabled bool       `json:"enabled"`
}

type RoutePolicy struct {
	Name     string   `json:"name"`
	Enabled  bool     `json:"enabled"`
	RuleSets []string `json:"rule_sets"`
	Domains  []string `json:"domains"`
	Subnets  []string `json:"subnets"`
	Outbound string   `json:"outbound"`
}

type GenericNode struct {
	Tag          string `json:"tag"`
	Address      string `json:"address"`
	Port         int    `json:"port"`
	Protocol     string `json:"protocol"`
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
	SourceURL    string `json:"source_url,omitempty"`
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
	Name           string           `json:"name"`
	URL            string           `json:"url"`
	UserAgent      string           `json:"user_agent"`
	HWID           string           `json:"hwid,omitempty"`
	Enabled        bool             `json:"enabled"`
	FilterMode     string           `json:"filter_mode,omitempty"`
	ExcludeRegex   []string         `json:"exclude_regex,omitempty"`
	CompiledRegex  []*regexp.Regexp `json:"-"`
	UpdateInterval string           `json:"update_interval,omitempty"`
}

func (s *SubscriptionConfig) CompileFilters() {
	s.CompiledRegex = make([]*regexp.Regexp, 0, len(s.ExcludeRegex))
	for _, p := range s.ExcludeRegex {
		p = strings.TrimSpace(strings.Trim(p, "'\""))
		if p == "" {
			continue
		}
		if re, err := regexp.Compile("(?i)" + p); err == nil {
			s.CompiledRegex = append(s.CompiledRegex, re)
		}
	}
}

type CustomSRSRule struct {
	Name           string `json:"name"`
	URL            string `json:"url"`
	DownloadDetour string `json:"download_detour"`
	Enabled        bool   `json:"enabled"`
}

type NodeFilterGroup struct {
	Name     string   `json:"name"`
	Regex    []string `json:"regex"`
	Priority int      `json:"priority"`
	Enabled  bool     `json:"enabled"`
}

type CheburConfig struct {
	Engine                string               `json:"engine"`
	RoutingMode           string               `json:"routing_mode"`
	SourceMode            string               `json:"source_mode"`
	ConfigType            string               `json:"config_type"`
	AdaptiveInterval      string               `json:"adaptive_interval"`
	AutoHWID              bool                 `json:"auto_hwid"`
	CustomHWID            string               `json:"custom_hwid"`
	AutoUpdate            bool                 `json:"auto_update"`
	UpdateChannel         string               `json:"update_channel"`
	RulesetUpdateInterval string               `json:"ruleset_update_interval"`
	TProxyPort            int                  `json:"tproxy_port"`
	DNSPort               int                  `json:"dns_port"`
	MixedPort             int                  `json:"mixed_port"`
	SourceIface           string               `json:"source_iface"`
	DNSProtocol           string               `json:"dns_protocol"`
	DNSServer             string               `json:"dns_server"`
	BootstrapDNS          string               `json:"bootstrap_dns"`
	DNSTTL                int                  `json:"dns_ttl"`
	EnableYACD            bool                 `json:"enable_yacd"`
	URLTestInterval       string               `json:"urltest_interval"`
	URLTestTolerance      int                  `json:"urltest_tolerance"`
	URLTestURL            string               `json:"urltest_url"`
	Nodes                 []*GenericNode       `json:"nodes"`
	Groups                []*BalancingGroup    `json:"groups"`
	Subscriptions         []SubscriptionConfig `json:"subscriptions"`
	ManualNodes           []string             `json:"manual_nodes"`
	RuleSets              []string             `json:"rule_sets"`
	CustomSRSRulesets     []CustomSRSRule      `json:"custom_srs_rulesets"`
	CustomDomains         []string             `json:"custom_domains"`
	CustomSubnets         []string             `json:"custom_subnets"`
	CustomPorts           []string             `json:"custom_ports"`
	LocalListFiles        []string             `json:"local_list_files"`
	ClientPolicies        []ClientPolicy       `json:"client_policies"`
	RoutePolicies         []RoutePolicy        `json:"route_policies"`

	NodeGroups         []NodeFilterGroup `json:"node_groups"`
	ActiveGroup        string            `json:"active_group"`
	AutoFallbackLTE    bool              `json:"auto_fallback_lte"`
	ScheduleLTEEnabled bool              `json:"schedule_lte_enabled"`
	ScheduleLTEStart   string            `json:"schedule_lte_start"`
	ScheduleLTEEnd     string            `json:"schedule_lte_end"`

	PublicSubEnabled bool   `json:"public_sub_enabled"`
	PublicSubPort    int    `json:"public_sub_port"`
	PublicSubToken   string `json:"public_sub_token"`
	ClashAPISecret   string `json:"clash_api_secret"`
	APIToken         string `json:"api_token"`
}

func (c *CheburConfig) Clone() *CheburConfig {
	if c == nil {
		return nil
	}
	cp := *c

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
	if c.CustomSRSRulesets != nil {
		cp.CustomSRSRulesets = append([]CustomSRSRule(nil), c.CustomSRSRulesets...)
	}

	if c.Nodes != nil {
		cp.Nodes = make([]*GenericNode, len(c.Nodes))
		for i, n := range c.Nodes {
			if n != nil {
				nodeCopy := *n
				cp.Nodes[i] = &nodeCopy
			}
		}
	}

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

	if c.Subscriptions != nil {
		cp.Subscriptions = make([]SubscriptionConfig, len(c.Subscriptions))
		for i, s := range c.Subscriptions {
			subCopy := s
			if s.ExcludeRegex != nil {
				subCopy.ExcludeRegex = append([]string(nil), s.ExcludeRegex...)
			}
			if s.CompiledRegex != nil {
				subCopy.CompiledRegex = append([]*regexp.Regexp(nil), s.CompiledRegex...)
			}
			cp.Subscriptions[i] = subCopy
		}
	}

	if c.ClientPolicies != nil {
		cp.ClientPolicies = append([]ClientPolicy(nil), c.ClientPolicies...)
	}

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

	if c.NodeGroups != nil {
		cp.NodeGroups = make([]NodeFilterGroup, len(c.NodeGroups))
		for i, ng := range c.NodeGroups {
			ngCopy := ng
			if ng.Regex != nil {
				ngCopy.Regex = append([]string(nil), ng.Regex...)
			}
			cp.NodeGroups[i] = ngCopy
		}
	}

	return &cp
}
