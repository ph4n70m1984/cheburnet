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
	URL       string `json:"url"`
	UserAgent string `json:"user_agent"`
	HWID      string `json:"hwid,omitempty"`
	Enabled   bool   `json:"enabled"`
}

type CheburConfig struct {
	Engine                string               `json:"engine"`
	RoutingMode           string               `json:"routing_mode"` // "rules" или "global"
	SourceMode            string               `json:"source_mode"`
	AutoHWID              bool                 `json:"auto_hwid"`
	CustomHWID            string               `json:"custom_hwid"`
	AutoUpdate            bool                 `json:"auto_update"`
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
	Nodes                 []*GenericNode       `json:"nodes"`
	Groups                []*BalancingGroup    `json:"groups"`
	Subscriptions         []SubscriptionConfig `json:"subscriptions"`
	ManualNodes           []string             `json:"manual_nodes"`
	RuleSets              []string             `json:"rule_sets"`
	CustomDomains         []string             `json:"custom_domains"`   // Введенные вручную домены
	CustomSubnets         []string             `json:"custom_subnets"`   // Введенные вручную IP/CIDR
	LocalListFiles        []string             `json:"local_list_files"` // Пути к .lst файлам на роутере
	ClientPolicies        []ClientPolicy       `json:"client_policies"`  // Правила маршрутизации по клиентам
}
