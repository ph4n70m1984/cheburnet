package network

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"
)

// ConfigureDnsmasq перенаправляет запросы dnsmasq на локальный DNS-инбаунд демона (127.0.0.42:dnsPort)
func ConfigureDnsmasq(dnsPort int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if dnsPort <= 0 {
		dnsPort = 1053
	}

	_ = exec.CommandContext(ctx, "uci", "-q", "delete", "dhcp.@dnsmasq[0].server").Run()
	_ = exec.CommandContext(ctx, "uci", "add_list", fmt.Sprintf("dhcp.@dnsmasq[0].server=127.0.0.42#%d", dnsPort)).Run()
	_ = exec.CommandContext(ctx, "uci", "set", "dhcp.@dnsmasq[0].noresolv=1").Run()
	_ = exec.CommandContext(ctx, "uci", "set", "dhcp.@dnsmasq[0].cachesize=0").Run()
	_ = exec.CommandContext(ctx, "uci", "set", "dhcp.@dnsmasq[0].rebind_protection=0").Run()

	if err := exec.CommandContext(ctx, "uci", "commit", "dhcp").Run(); err != nil {
		return fmt.Errorf("uci commit failed: %w", err)
	}

	restartCtx, restartCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer restartCancel()
	return exec.CommandContext(restartCtx, "/etc/init.d/dnsmasq", "restart").Run()
}

// RestoreDnsmasq убирает перенаправление на 127.0.0.42, возвращает системные настройки кэша и резолва,
// а также гарантирует наличие апстримов (8.8.8.8, 77.88.8.8), чтобы сеть оставалась работоспособной.
func RestoreDnsmasq() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. Считываем текущие DNS-серверы из UCI
	out, err := exec.CommandContext(ctx, "uci", "-q", "get", "dhcp.@dnsmasq[0].server").Output()
	rawServers := strings.TrimSpace(string(out))

	var activeServers []string
	if err == nil && rawServers != "" {
		fields := strings.Fields(rawServers)
		for _, s := range fields {
			s = strings.TrimSpace(s)
			// Отфильтровываем локальный адрес демона
			if s != "" && !strings.Contains(s, "127.0.0.42") {
				activeServers = append(activeServers, s)
			}
		}
	}

	// 2. Если серверов не осталось, гарантируем резервные публичные DNS
	if len(activeServers) == 0 {
		activeServers = []string{"8.8.8.8", "77.88.8.8"}
	}

	// 3. Сбрасываем старый список server и восстанавливаем системные параметры
	_ = exec.CommandContext(ctx, "uci", "-q", "delete", "dhcp.@dnsmasq[0].server").Run()
	for _, s := range activeServers {
		_ = exec.CommandContext(ctx, "uci", "add_list", fmt.Sprintf("dhcp.@dnsmasq[0].server=%s", s)).Run()
	}

	_ = exec.CommandContext(ctx, "uci", "set", "dhcp.@dnsmasq[0].noresolv=0").Run()
	_ = exec.CommandContext(ctx, "uci", "set", "dhcp.@dnsmasq[0].cachesize=150").Run()
	_ = exec.CommandContext(ctx, "uci", "set", "dhcp.@dnsmasq[0].rebind_protection=1").Run()

	if err := exec.CommandContext(ctx, "uci", "commit", "dhcp").Run(); err != nil {
		log.Printf("[WARN] Failed to commit dhcp changes on restore: %v", err)
	}

	restartCtx, restartCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer restartCancel()
	if err := exec.CommandContext(restartCtx, "/etc/init.d/dnsmasq", "restart").Run(); err != nil {
		log.Printf("[WARN] Failed to restart dnsmasq on restore: %v", err)
	}

	log.Printf("[INFO] Dnsmasq settings restored. Active upstreams: %v", activeServers)
}
