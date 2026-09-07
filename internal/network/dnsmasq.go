package network

import (
	"os/exec"
)

func ConfigureDnsmasq(dnsPort int) error {
	_ = exec.Command("uci", "delete", "dhcp.@dnsmasq[0].server").Run()
	_ = exec.Command("uci", "add_list", "dhcp.@dnsmasq[0].server=127.0.0.42#1053").Run()
	_ = exec.Command("uci", "set", "dhcp.@dnsmasq[0].noresolv=1").Run()
	_ = exec.Command("uci", "set", "dhcp.@dnsmasq[0].cachesize=0").Run()
	_ = exec.Command("uci", "set", "dhcp.@dnsmasq[0].rebind_protection=0").Run()
	_ = exec.Command("uci", "commit", "dhcp").Run()

	return exec.Command("/etc/init.d/dnsmasq", "restart").Run()
}

func RestoreDnsmasq() {
	_ = exec.Command("uci", "delete", "dhcp.@dnsmasq[0].server").Run()
	_ = exec.Command("uci", "set", "dhcp.@dnsmasq[0].noresolv=0").Run()
	_ = exec.Command("uci", "set", "dhcp.@dnsmasq[0].cachesize=150").Run()
	_ = exec.Command("uci", "commit", "dhcp").Run()
	_ = exec.Command("/etc/init.d/dnsmasq", "restart").Run()
}
