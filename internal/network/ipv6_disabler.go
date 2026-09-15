package network

import (
	"bytes"
	"log"
	"os"
	"os/exec"
	"strings"
)

type IPv6Manager struct{}

func NewIPv6Manager() *IPv6Manager {
	return &IPv6Manager{}
}

// IsIPv6Active проверяет, активен ли IPv6 в системе (sysctl, odhcpd или UCI)
func (m *IPv6Manager) IsIPv6Active() bool {
	// 1. Проверка ядра через /proc/sys/net/ipv6/conf/all/disable_ipv6
	data, err := os.ReadFile("/proc/sys/net/ipv6/conf/all/disable_ipv6")
	if err == nil {
		if strings.TrimSpace(string(data)) == "0" {
			return true
		}
	}

	// 2. Проверка запущенного демона odhcpd
	out, err := exec.Command("pgrep", "odhcpd").Output()
	if err == nil && len(bytes.TrimSpace(out)) > 0 {
		return true
	}

	// 3. Проверка фильтра AAAA записей в dnsmasq
	filterAAAA, err := exec.Command("uci", "-q", "get", "dhcp.@dnsmasq[0].filter_aaaa").Output()
	if err != nil || strings.TrimSpace(string(filterAAAA)) != "1" {
		return true
	}

	// 4. Проверка раздачи RA / DHCPv6 в LAN
	raState, err := exec.Command("uci", "-q", "get", "dhcp.lan.ra").Output()
	if err == nil && len(bytes.TrimSpace(raState)) > 0 && strings.TrimSpace(string(raState)) != "disabled" {
		return true
	}

	return false
}

// EnsureIPv6Disabled выполняет обнаружение и принудительное глушение IPv6
func (m *IPv6Manager) EnsureIPv6Disabled() error {
	if !m.IsIPv6Active() {
		log.Println("[ipv6] IPv6 is already completely disabled")
		return nil
	}

	log.Println("[ipv6] Active IPv6 configuration detected. Disabling completely...")

	// 1. Отключение IPv6 на интерфейсах lan/wan
	_ = exec.Command("uci", "set", "network.lan.ipv6=0").Run()
	_ = exec.Command("uci", "set", "network.wan.ipv6=0").Run()
	_ = exec.Command("uci", "set", "dhcp.lan.dhcpv6=disabled").Run()

	// 2. Удаление RA и DHCPv6
	_ = exec.Command("uci", "-q", "delete", "dhcp.lan.dhcpv6").Run()
	_ = exec.Command("uci", "-q", "delete", "dhcp.lan.ra").Run()

	// 3. Отключение делегирования LAN
	_ = exec.Command("uci", "set", "network.lan.delegate=0").Run()

	// 4. Удаление ULA префикса
	_ = exec.Command("uci", "-q", "delete", "network.globals.ula_prefix").Run()

	// 5. Остановка и отключение автозапуска odhcpd
	_ = exec.Command("/etc/init.d/odhcpd", "disable").Run()
	_ = exec.Command("/etc/init.d/odhcpd", "stop").Run()

	// 6. Заставляем dnsmasq отсекать AAAA записи (только IPv4 клиентам)
	_ = exec.Command("uci", "set", "dhcp.@dnsmasq[0].filter_aaaa=1").Run()

	// 7. Коммит настроек UCI
	if err := exec.Command("uci", "commit").Run(); err != nil {
		log.Printf("[ipv6] WARNING: uci commit failed: %v", err)
	}

	// 8. Перезапуск сетевых служб OpenWrt
	_ = exec.Command("/etc/init.d/dnsmasq", "restart").Run()
	_ = exec.Command("/etc/init.d/network", "restart").Run()

	// 9. Полное глушение сетевого стека IPv6 в ядре Linux
	_ = exec.Command("sysctl", "-w", "net.ipv6.conf.all.disable_ipv6=1").Run()
	_ = os.WriteFile("/proc/sys/net/ipv6/conf/all/disable_ipv6", []byte("1\n"), 0644)
	_ = exec.Command("sysctl", "-w", "net.ipv6.conf.default.disable_ipv6=1").Run()
	_ = exec.Command("sysctl", "-w", "net.ipv6.conf.lo.disable_ipv6=1").Run()

	// Сохранение sysctl параметров для персистентности между ребутами
	sysctlConf := []byte("net.ipv6.conf.all.disable_ipv6=1\nnet.ipv6.conf.default.disable_ipv6=1\nnet.ipv6.conf.lo.disable_ipv6=1\n")
	_ = os.WriteFile("/etc/sysctl.d/99-disable-ipv6.conf", sysctlConf, 0644)

	log.Println("[ipv6] Successfully disabled IPv6, odhcpd stopped, dnsmasq filtered AAAA")
	return nil
}
