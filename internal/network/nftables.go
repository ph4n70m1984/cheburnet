package network

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

const (
	TableName = "CheburTable"
	TableMark = "0x00100000"
	SelfMark  = "0x00200000"
)

var ifaceRegex = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,16}$`)

func ApplyNFTRules(ifaces []string, subnets []string, fullProxyIPs []string, tproxyPort int, isGlobalMode bool) error {
	// Валидация TProxy порта
	if tproxyPort <= 0 || tproxyPort > 65535 {
		return fmt.Errorf("invalid tproxy port: %d (must be between 1 and 65535)", tproxyPort)
	}

	// 1. Валидация интерфейсов (защита от инъекций в ifname)
	var validIfaces []string
	for _, iface := range ifaces {
		clean := strings.TrimSpace(iface)
		if clean == "" {
			continue
		}
		if !ifaceRegex.MatchString(clean) {
			return fmt.Errorf("invalid interface name (possible injection attempt): %q", clean)
		}
		validIfaces = append(validIfaces, fmt.Sprintf("%q", clean))
	}
	if len(validIfaces) == 0 {
		validIfaces = []string{`"br-lan"`}
	}
	ifaceElements := strings.Join(validIfaces, ", ")

	// 2. Валидация подсетей через строгий парсинг CIDR (только IPv4)
	subnetElements := ""
	bypassMangleRule := ""
	bypassOutputRule := ""

	if isGlobalMode {
		bypassMangleRule = fmt.Sprintf("iifname @interfaces ip daddr != @localv4 meta mark set %s counter", TableMark)
		bypassOutputRule = fmt.Sprintf("ip daddr != @localv4 meta mark set %s counter", TableMark)
	} else if len(subnets) > 0 {
		var validSubnets []string
		for _, s := range subnets {
			clean := strings.TrimSpace(s)
			if clean == "" {
				continue
			}
			_, ipNet, err := net.ParseCIDR(clean)
			if err != nil {
				// Пробуем распарсить как одиночный IPv4 адрес
				ip := net.ParseIP(clean)
				if ip == nil || ip.To4() == nil {
					return fmt.Errorf("invalid bypass subnet/ip: %q", clean)
				}
				validSubnets = append(validSubnets, ip.To4().String()+"/32")
			} else {
				// P1: Защита от попадания IPv6 в set type ipv4_addr
				if ipNet.IP.To4() == nil {
					return fmt.Errorf("IPv6 subnets are not supported in IPv4 bypass set: %q", clean)
				}
				validSubnets = append(validSubnets, ipNet.String())
			}
		}

		if len(validSubnets) > 0 {
			subnetElements = fmt.Sprintf(`
	set bypass_subnets {
		type ipv4_addr
		flags interval
		auto-merge
		elements = { %s }
	}`, strings.Join(validSubnets, ", "))

			bypassMangleRule = fmt.Sprintf("iifname @interfaces ip daddr @bypass_subnets meta mark set %s counter", TableMark)
			bypassOutputRule = fmt.Sprintf("ip daddr @bypass_subnets meta mark set %s counter", TableMark)
		}
	}

	// 3. Валидация full-proxy IP клиентов через строгий net.ParseIP (только IPv4)
	clientSetElements := ""
	clientMangleRule := ""
	if len(fullProxyIPs) > 0 {
		var validClients []string
		for _, rawIP := range fullProxyIPs {
			clean := strings.TrimSpace(rawIP)
			if clean == "" {
				continue
			}
			ip := net.ParseIP(clean)
			if ip == nil || ip.To4() == nil {
				return fmt.Errorf("invalid client IPv4 address: %q", clean)
			}
			validClients = append(validClients, ip.To4().String())
		}

		if len(validClients) > 0 {
			clientSetElements = fmt.Sprintf(`
	set full_proxy_clients {
		type ipv4_addr
		flags interval
		elements = { %s }
	}`, strings.Join(validClients, ", "))

			clientMangleRule = fmt.Sprintf("iifname @interfaces ip saddr @full_proxy_clients ip daddr != @localv4 meta mark set %s counter", TableMark)
		}
	}

	// P0: Возвращаем транзакционную атомарную замену внутри одного batch:
	// 1) table inet X       -> объявление таблицы (если её не было, delete не упадёт)
	// 2) delete table inet X -> удаление старых правил
	// 3) table inet X { ... } -> создание новой конфигурации
	tpl := `table inet %s
delete table inet %s
table inet %s {
	set localv4 {
		type ipv4_addr
		flags interval
		auto-merge
		elements = { 0.0.0.0/8, 10.0.0.0/8, 127.0.0.0/8, 169.254.0.0/16,
		             172.16.0.0/12, 192.0.0.0/24, 192.168.0.0/16, 224.0.0.0/4, 240.0.0.0/4 }
	}

	set interfaces {
		type ifname
		elements = { %s }
	}
%s
%s
	chain mangle {
		type filter hook prerouting priority -150; policy accept;
		ct status dnat return
		udp dport 123 return
		ip daddr @localv4 return
		%s
		%s
		iifname @interfaces ip daddr 198.18.0.0/15 meta l4proto tcp meta mark set %s counter
		iifname @interfaces ip daddr 198.18.0.0/15 meta l4proto udp meta mark set %s counter
	}

	chain proxy {
		type filter hook prerouting priority -100; policy accept;
		meta mark & %s == %s meta l4proto tcp tproxy ip to 127.0.0.1:%d counter
		meta mark & %s == %s meta l4proto udp tproxy ip to 127.0.0.1:%d counter
	}

	chain mangle_output {
		type route hook output priority -150; policy accept;
		ip daddr @localv4 return
		meta mark %s counter return
		%s
		ip daddr 198.18.0.0/15 meta l4proto tcp meta mark set %s counter
		ip daddr 198.18.0.0/15 meta l4proto udp meta mark set %s counter
	}
}
`
	rules := fmt.Sprintf(tpl,
		TableName,
		TableName,
		TableName,
		ifaceElements,
		subnetElements,
		clientSetElements,
		clientMangleRule,
		bypassMangleRule,
		TableMark,
		TableMark,
		TableMark, TableMark, tproxyPort,
		TableMark, TableMark, tproxyPort,
		SelfMark,
		bypassOutputRule,
		TableMark,
		TableMark,
	)

	// Атомарное применение транзакции через stdin
	applyCtx, cancelApply := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelApply()

	applyCmd := exec.CommandContext(applyCtx, "nft", "-f", "-")
	applyCmd.Stdin = bytes.NewBufferString(rules)
	if out, err := applyCmd.CombinedOutput(); err != nil {
		if applyCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("nft apply timed out (netlink socket blocked)")
		}
		return fmt.Errorf("nft apply error: %w (output: %s)", err, string(out))
	}

	return nil
}

func FlushNFTRules() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "nft", "delete", "table", "inet", TableName)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("flush nft rules timed out")
		}
		if strings.Contains(string(out), "No such file or directory") {
			return nil
		}
		return fmt.Errorf("flush nft rules error: %w (output: %s)", err, string(out))
	}
	return nil
}
