package network

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

const (
	TableName = "CheburTable"

	// Строковые представления меток для шаблонизатора nftables
	TableMark           = "0x00100000"
	SelfMark            = "0x00200000"
	EmergencyDirectMark = "0x00300000"

	// Числовые целочисленные константы для системных вызовов syscall.SetsockoptInt
	TableMarkInt           = 0x00100000
	SelfMarkInt            = 0x00200000
	EmergencyDirectMarkInt = 0x00300000
)

var ifaceRegex = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,16}$`)

// ConfigureInterfaceSysctl конфигурирует параметры ядра Linux для TProxy и туннельных интерфейсов
func ConfigureInterfaceSysctl(ifaces []string) {
	// Базовые параметры: включение роутинга и перевод глобального фильтра обратного пути в Loose Mode (2)
	globals := map[string]string{
		"net.ipv4.ip_forward":             "1",
		"net.ipv4.conf.all.rp_filter":     "2",
		"net.ipv4.conf.default.rp_filter": "2",
	}

	for param, val := range globals {
		if err := setSysctl(param, val); err != nil {
			log.Printf("[network-sysctl] warning: failed to set %s=%s: %v", param, val, err)
		}
	}

	// Динамическая настройка для каждого выбранного интерфейса (br-lan, wg0 и др.)
	for _, iface := range ifaces {
		clean := strings.TrimSpace(iface)
		if clean == "" || !ifaceRegex.MatchString(clean) {
			continue
		}

		// 1. Loose Reverse Path Filter (rp_filter=2): предотвращает сброс асимметричного входящего TProxy-трафика
		rpKey := fmt.Sprintf("net.ipv4.conf.%s.rp_filter", clean)
		if err := setSysctl(rpKey, "2"); err != nil {
			log.Printf("[network-sysctl] notice: %s not updated: %v (interface may be offline)", rpKey, err)
		}

		// 2. IP Forwarding (forwarding=1): разрешает пересылку пакетов между локальными и туннельными сетями
		fwdKey := fmt.Sprintf("net.ipv4.conf.%s.forwarding", clean)
		if err := setSysctl(fwdKey, "1"); err != nil {
			log.Printf("[network-sysctl] notice: %s not updated: %v (interface may be offline)", fwdKey, err)
		}
	}
}

func setSysctl(param, val string) error {
	// 1. Быстрая запись напрямую в псевдо-ФС ядра без вызова подпроцессов
	procPath := "/proc/sys/" + strings.ReplaceAll(param, ".", "/")
	if err := os.WriteFile(procPath, []byte(val+"\n"), 0644); err == nil {
		return nil
	}

	// 2. Fallback через стандартную системную утилиту sysctl
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sysctl", "-w", fmt.Sprintf("%s=%s", param, val))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ApplyNFTRules выполняет атомарное применение правил nftables для перехвата сетевого трафика через TProxy
func ApplyNFTRules(ifaces []string, subnets []string, fullProxyIPs []string, tproxyPort int, isGlobalMode bool) error {
	// Валидация TProxy порта
	if tproxyPort <= 0 || tproxyPort > 65535 {
		return fmt.Errorf("invalid tproxy port: %d (must be between 1 and 65535)", tproxyPort)
	}

	// 1. Валидация сетевых интерфейсов и подготовка сырого списка для sysctl
	var validIfaces []string
	var rawIfaces []string
	for _, iface := range ifaces {
		clean := strings.TrimSpace(iface)
		if clean == "" {
			continue
		}
		if !ifaceRegex.MatchString(clean) {
			return fmt.Errorf("invalid interface name (possible injection attempt): %q", clean)
		}
		validIfaces = append(validIfaces, fmt.Sprintf("%q", clean))
		rawIfaces = append(rawIfaces, clean)
	}
	if len(validIfaces) == 0 {
		validIfaces = []string{`"br-lan"`}
		rawIfaces = []string{"br-lan"}
	}
	ifaceElements := strings.Join(validIfaces, ", ")

	// Применяем настройки ядра ОС для интерфейсов перед включением правил
	ConfigureInterfaceSysctl(rawIfaces)

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
				ip := net.ParseIP(clean)
				if ip == nil || ip.To4() == nil {
					return fmt.Errorf("invalid bypass subnet/ip: %q", clean)
				}
				validSubnets = append(validSubnets, ip.To4().String()+"/32")
			} else {
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

	// 3. Валидация IP-адресов клиентов Full-Proxy через строгий net.ParseIP (только IPv4)
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

	// Атомарный Netlink batch[cite: 5]
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
		EmergencyDirectMark,
		bypassOutputRule,
		TableMark,
		TableMark,
	)

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

// FlushNFTRules удаляет таблицу CheburTable из ядра Netfilter
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
