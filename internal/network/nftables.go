package network

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

const (
	TableName = "CheburTable"
	TableMark = "0x00100000"
	SelfMark  = "0x00200000"
)

func ApplyNFTRules(ifaces []string, subnets []string, fullProxyIPs []string, tproxyPort int, isGlobalMode bool) error {
	if len(ifaces) == 0 {
		ifaces = []string{"br-lan"}
	}

	ifaceElements := strings.Join(ifaces, ", ")

	subnetElements := ""
	bypassMangleRule := ""
	bypassOutputRule := ""

	if isGlobalMode {
		bypassMangleRule = fmt.Sprintf("iifname @interfaces ip daddr != @localv4 meta mark set %s counter", TableMark)
		bypassOutputRule = fmt.Sprintf("ip daddr != @localv4 meta mark set %s counter", TableMark)
	} else if len(subnets) > 0 {
		var validSubnets []string
		for _, s := range subnets {
			s = strings.TrimSpace(s)
			if s != "" {
				validSubnets = append(validSubnets, s)
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

	clientSetElements := ""
	clientMangleRule := ""
	if len(fullProxyIPs) > 0 {
		var validClients []string
		for _, ip := range fullProxyIPs {
			ip = strings.TrimSpace(ip)
			if ip != "" {
				validClients = append(validClients, ip)
			}
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

	tpl := `
table inet %s
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

	// 1. Проверка синтаксиса без применения (Dry-run)
	checkCmd := exec.Command("nft", "-c", "-f", "-")
	checkCmd.Stdin = bytes.NewBufferString(rules)
	if out, err := checkCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("nft syntax check failed: %w (output: %s)", err, string(out))
	}

	// 2. Атомарное применение правил
	applyCmd := exec.Command("nft", "-f", "-")
	applyCmd.Stdin = bytes.NewBufferString(rules)
	if out, err := applyCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("nft apply error: %w (output: %s)", err, string(out))
	}

	return nil
}

func FlushNFTRules() error {
	cmd := exec.Command("nft", "delete", "table", "inet", TableName)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "No such file or directory") {
			return nil
		}
		return fmt.Errorf("flush nft rules error: %w (output: %s)", err, string(out))
	}
	return nil
}
