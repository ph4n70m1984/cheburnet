package singbox

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
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

// resolveTargetToCIDR проверяет переданное значение (MAC или IP) и возвращает валидный CIDR
func resolveTargetToCIDR(target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}

	// Если передан MAC-адрес (содержит двоеточия и не содержит точки)
	if strings.Contains(target, ":") && !strings.Contains(target, ".") {
		file, err := os.Open("/tmp/dhcp.leases")
		if err == nil {
			defer file.Close()
			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				fields := strings.Fields(scanner.Text())
				// Формат dnsmasq: <timestamp> <mac> <ip> <hostname> <client-id>
				if len(fields) >= 3 {
					if strings.EqualFold(fields[1], target) {
						target = fields[2]
						break
					}
				}
			}
		}
	}

	// Если это IPv4 адрес без маски подсети
	if !strings.Contains(target, "/") && net.ParseIP(target) != nil {
		return target + "/32"
	}

	return target
}

// getRouterLANIP получает первый валидный IPv4-адрес интерфейса br-lan
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

	// FakeIP для пользовательских доменов
	if len(cfg.CustomDomains) > 0 {
		dnsRules = append(dnsRules, map[string]interface{}{
			"action":        "route",
			"server":        "fakeip-dns",
			"domain_suffix": cfg.CustomDomains,
		})
	}

	// FakeIP для наборов правил .srs
	if len(cfg.RuleSets) > 0 {
		dnsRules = append(dnsRules, map[string]interface{}{
			"action":   "route",
			"server":   "fakeip-dns",
			"rule_set": cfg.RuleSets,
		})
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
				"listen":        "127.0.0.1",
				"listen_port":   cfg.TProxyPort,
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

	// 1. Формирование аутбаундов нод
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

	// 2. Сборка балансировочных групп (urltest / selector)
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

	// 3. Таблица правил маршрутизации (Route)
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

	// ============================================================
	// ПРИОРИТЕТ 1: ПОЛИТИКИ КЛИЕНТОВ (CLIENT POLICY)
	// ============================================================
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
		case config.ClientModeRules:
			// Режим "по спискам": трафик проходит ниже к общим правилам
		}
	}

	// Прямой доступ для устройств-исключений
	if len(directClients) > 0 {
		routeRules = append(routeRules, map[string]interface{}{
			"action":         "route",
			"inbound":        []string{"tproxy-in"},
			"source_ip_cidr": directClients,
			"outbound":       "direct-out",
		})
	}

	// Полный туннель для выбранных устройств
	if len(fullProxyClients) > 0 && activeOutboundTag != "direct-out" {
		routeRules = append(routeRules, map[string]interface{}{
			"action":         "route",
			"inbound":        []string{"tproxy-in"},
			"source_ip_cidr": fullProxyClients,
			"outbound":       activeOutboundTag,
		})
	}

	// ============================================================
	// ПРИОРИТЕТ 2: ОБЩИЕ ПРАВИЛА (ПОДСЕТИ, ДОМЕНЫ, RULE-SETS)
	// ============================================================
	if activeOutboundTag != "direct-out" {
		totalSubnets := make([]string, 0, len(cfg.CustomSubnets))
		totalSubnets = append(totalSubnets, cfg.CustomSubnets...)

		// Подгрузка подсетей на лету из .lst.gz архивов
		for _, rs := range cfg.RuleSets {
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

		// Пользовательские домены
		if len(cfg.CustomDomains) > 0 {
			routeRules = append(routeRules, map[string]interface{}{
				"action":        "route",
				"inbound":       []string{"tproxy-in"},
				"domain_suffix": cfg.CustomDomains,
				"outbound":      activeOutboundTag,
			})
		}

		// Готовые списки .srs
		if len(cfg.RuleSets) > 0 {
			routeRules = append(routeRules, map[string]interface{}{
				"action":   "route",
				"inbound":  []string{"tproxy-in"},
				"outbound": activeOutboundTag,
				"rule_set": cfg.RuleSets,
			})
		}
	}

	routeRules = append(routeRules, map[string]interface{}{
		"action":   "route",
		"inbound":  []string{"mixed-in"},
		"outbound": activeOutboundTag,
	})

	var ruleSetObjects []map[string]interface{}
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

	sbConfig["route"] = map[string]interface{}{
		"rules":                   routeRules,
		"rule_set":                ruleSetObjects,
		"final":                   "direct-out",
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
