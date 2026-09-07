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

func ApplyNFTRules(ifaces []string, subnets []string, tproxyPort int) error {
	if len(ifaces) == 0 {
		ifaces = []string{"br-lan"}
	}

	ifaceElements := strings.Join(ifaces, ", ")

	// Формируем блок подсетей для сета bypass_subnets
	subnetElements := ""
	bypassMangleRule := ""
	bypassOutputRule := ""

	if len(subnets) > 0 {
		subnetElements = fmt.Sprintf(`
	set bypass_subnets {
		type ipv4_addr
		flags interval
		auto-merge
		elements = { %s }
	}`, strings.Join(subnets, ", "))

		bypassMangleRule = fmt.Sprintf("iifname @interfaces ip daddr @bypass_subnets meta mark set %s counter", TableMark)
		bypassOutputRule = fmt.Sprintf("ip daddr @bypass_subnets meta mark set %s counter", TableMark)
	}

	tpl := `
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
	chain mangle {
		type filter hook prerouting priority -150; policy accept;
		ct status dnat return
		udp dport 123 return
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
		ifaceElements,
		subnetElements,
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

	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = bytes.NewBufferString(rules)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("nft error: %w (output: %s)", err, string(out))
	}
	return nil
}

func FlushNFTRules() error {
	_ = exec.Command("nft", "delete", "table", "inet", TableName).Run()
	return nil
}
