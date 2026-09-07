package xray

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"cheburnet/internal/config"
)

type Builder struct{}

func NewBuilder() *Builder {
	return &Builder{}
}

func (b *Builder) Build(cfg *config.CheburConfig, outputPath string) error {
	xrayConfig := map[string]interface{}{
		"log": map[string]interface{}{
			"loglevel": "warning",
		},
		"api": map[string]interface{}{
			"tag":      "api",
			"services": []string{"StatsService"},
		},
		"stats": map[string]interface{}{},
		"policy": map[string]interface{}{
			"levels": map[string]interface{}{
				"0": map[string]interface{}{
					"statsUserUplink":   true,
					"statsUserDownlink": true,
				},
			},
			"system": map[string]interface{}{
				"statsInboundUplink":    true,
				"statsInboundDownlink":  true,
				"statsOutboundUplink":   true,
				"statsOutboundDownlink": true,
			},
		},
		"dns": map[string]interface{}{
			"servers": []interface{}{
				cfg.DNSServer,
				cfg.BootstrapDNS,
				"localhost",
			},
			"queryStrategy": "UseIPv4",
		},
	}

	// 1. Inbounds (TProxy + DNS Hijack + Dokodemo API)
	inbounds := []map[string]interface{}{
		{
			"tag":      "api-in",
			"listen":   "127.0.0.1",
			"port":     10085,
			"protocol": "dokodemo-door",
			"settings": map[string]interface{}{
				"address": "127.0.0.1",
			},
		},
		{
			"tag":      "tproxy-in",
			"listen":   "127.0.0.1",
			"port":     cfg.TProxyPort,
			"protocol": "dokodemo-door",
			"settings": map[string]interface{}{
				"network":        "tcp,udp",
				"followRedirect": true,
			},
			"streamSettings": map[string]interface{}{
				"sockopt": map[string]interface{}{
					"tproxy": "tproxy",
				},
			},
			"sniffing": map[string]interface{}{
				"enabled":      true,
				"destOverride": []string{"http", "tls", "quic"},
				"routeOnly":    true,
			},
		},
		{
			"tag":      "dns-in",
			"listen":   "127.0.0.42",
			"port":     cfg.DNSPort,
			"protocol": "dokodemo-door",
			"settings": map[string]interface{}{
				"network": "tcp,udp",
				"address": cfg.DNSServer,
				"port":    53,
			},
		},
	}
	xrayConfig["inbounds"] = inbounds

	// 2. Outbounds (Прямой выход, DNS и ноды)
	outbounds := []map[string]interface{}{
		{
			"tag":      "direct",
			"protocol": "freedom",
			"streamSettings": map[string]interface{}{
				"sockopt": map[string]interface{}{
					"mark": 2097152, // 0x200000 NFT SelfMark
				},
			},
		},
		{
			"tag":      "block",
			"protocol": "blackhole",
			"settings": map[string]interface{}{
				"response": map[string]string{"type": "none"},
			},
		},
		{
			"tag":      "dns-out",
			"protocol": "dns",
			"streamSettings": map[string]interface{}{
				"sockopt": map[string]interface{}{
					"mark": 2097152,
				},
			},
		},
	}

	var primaryProxyTag string
	for _, node := range cfg.Nodes {
		ob, err := b.buildNodeOutbound(node)
		if err == nil {
			outbounds = append(outbounds, ob)
			if primaryProxyTag == "" {
				primaryProxyTag = node.Tag
			}
		}
	}
	if primaryProxyTag == "" {
		primaryProxyTag = "direct"
	}
	xrayConfig["outbounds"] = outbounds

	// 3. Routing Rules
	rules := []map[string]interface{}{
		{
			"type":        "field",
			"inboundTag":  []string{"api-in"},
			"outboundTag": "api",
		},
		{
			"type":        "field",
			"inboundTag":  []string{"dns-in"},
			"outboundTag": "dns-out",
		},
	}

	// Маршрутизация по подсетям (CIDR)
	if len(cfg.CustomSubnets) > 0 && primaryProxyTag != "direct" {
		rules = append(rules, map[string]interface{}{
			"type":        "field",
			"ip":          cfg.CustomSubnets,
			"outboundTag": primaryProxyTag,
		})
	}

	// Маршрутизация по доменам
	if len(cfg.CustomDomains) > 0 && primaryProxyTag != "direct" {
		var domainRules []string
		for _, d := range cfg.CustomDomains {
			domainRules = append(domainRules, "domain:"+d)
		}
		rules = append(rules, map[string]interface{}{
			"type":        "field",
			"domain":      domainRules,
			"outboundTag": primaryProxyTag,
		})
	}

	// Правила по готовым наборам правил
	if len(cfg.RuleSets) > 0 && primaryProxyTag != "direct" {
		var extDomains []string
		for _, rs := range cfg.RuleSets {
			switch rs {
			case "youtube":
				extDomains = append(extDomains, "domain:youtube.com", "domain:googlevideo.com", "domain:ytimg.com")
			case "meta":
				extDomains = append(extDomains, "domain:instagram.com", "domain:facebook.com", "domain:cdninstagram.com")
			case "telegram":
				extDomains = append(extDomains, "domain:t.me", "domain:telegram.org")
			case "discord":
				extDomains = append(extDomains, "domain:discord.com", "domain:discord.gg", "domain:discordapp.com")
			case "twitter":
				extDomains = append(extDomains, "domain:x.com", "domain:twitter.com", "domain:twimg.com")
			}
		}
		if len(extDomains) > 0 {
			rules = append(rules, map[string]interface{}{
				"type":        "field",
				"domain":      extDomains,
				"outboundTag": primaryProxyTag,
			})
		}
	}

	xrayConfig["routing"] = map[string]interface{}{
		"domainStrategy": "IPIfNonMatch",
		"rules":          rules,
	}

	data, err := json.MarshalIndent(xrayConfig, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal xray config: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return err
	}

	return os.WriteFile(outputPath, data, 0644)
}

func (b *Builder) buildNodeOutbound(node *config.GenericNode) (map[string]interface{}, error) {
	out := map[string]interface{}{
		"tag":      node.Tag,
		"protocol": node.Protocol,
	}

	sockopt := map[string]interface{}{
		"mark": 2097152, // 0x200000 NFT SelfMark
	}

	switch node.Protocol {
	case "vless":
		vnextUser := map[string]interface{}{
			"id":         node.UUID,
			"encryption": "none",
			"level":      0,
		}
		if node.Flow != "" {
			vnextUser["flow"] = node.Flow
		}

		out["settings"] = map[string]interface{}{
			"vnext": []map[string]interface{}{
				{
					"address": node.Address,
					"port":    node.Port,
					"users":   []map[string]interface{}{vnextUser},
				},
			},
		}

		streamSettings := map[string]interface{}{
			"network":  node.Network,
			"security": node.Security,
			"sockopt":  sockopt,
		}

		if node.Security == "reality" {
			realitySettings := map[string]interface{}{
				"show":        false,
				"fingerprint": node.Fingerprint,
				"serverName":  node.SNI,
				"publicKey":   node.PublicKey,
				"shortId":     node.ShortID,
				"spiderX":     "/",
			}
			streamSettings["realitySettings"] = realitySettings
		} else if node.Security == "tls" {
			streamSettings["tlsSettings"] = map[string]interface{}{
				"serverName":    node.SNI,
				"allowInsecure": node.Insecure,
				"fingerprint":   node.Fingerprint,
			}
		}

		if node.Network == "ws" {
			streamSettings["wsSettings"] = map[string]interface{}{
				"path": node.Path,
				"headers": map[string]string{
					"Host": node.Host,
				},
			}
		} else if node.Network == "grpc" {
			streamSettings["grpcSettings"] = map[string]interface{}{
				"serviceName": node.Path,
				"multiMode":   true,
			}
		}

		out["streamSettings"] = streamSettings

	case "hysteria2":
		out["protocol"] = "hysteria2"
		out["settings"] = map[string]interface{}{
			"servers": []map[string]interface{}{
				{
					"address":  node.Address,
					"port":     node.Port,
					"password": node.Password,
				},
			},
		}
		out["streamSettings"] = map[string]interface{}{
			"network":  "udp",
			"security": "tls",
			"tlsSettings": map[string]interface{}{
				"serverName":    node.SNI,
				"allowInsecure": node.Insecure,
			},
			"sockopt": sockopt,
		}

	case "shadowsocks":
		out["settings"] = map[string]interface{}{
			"servers": []map[string]interface{}{
				{
					"address":  node.Address,
					"port":     node.Port,
					"method":   node.Method,
					"password": node.Password,
				},
			},
		}
		out["streamSettings"] = map[string]interface{}{
			"sockopt": sockopt,
		}

	case "trojan":
		out["settings"] = map[string]interface{}{
			"servers": []map[string]interface{}{
				{
					"address":  node.Address,
					"port":     node.Port,
					"password": node.Password,
				},
			},
		}
		out["streamSettings"] = map[string]interface{}{
			"security": "tls",
			"tlsSettings": map[string]interface{}{
				"serverName":    node.SNI,
				"allowInsecure": node.Insecure,
			},
			"sockopt": sockopt,
		}

	default:
		return nil, fmt.Errorf("unsupported protocol for xray: %s", node.Protocol)
	}

	return out, nil
}
