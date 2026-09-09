package diagnostics

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"cheburnet/internal/engine"
	"cheburnet/internal/network"
)

// EvaluateSnapshot возвращает 13 базовых проверок здоровья ядра, DNS и трафика
func EvaluateSnapshot(s engine.HealthSnapshot) []CheckResult {
	if !s.Initialized {
		return nil
	}

	var r []CheckResult

	// --- Engine checks (4) ---
	r = append(r, CheckResult{
		CheckID:   "engine.process_down",
		Component: "engine",
		Healthy:   s.Engine.Running,
		Severity:  SeverityCritical,
		Message:   "Ядро проксирования не запущено",
		Action:    "restart_engine",
	})

	r = append(r, CheckResult{
		CheckID:   "engine.process_unstable",
		Component: "engine",
		Healthy:   s.Engine.CrashCount < 3,
		Severity:  SeverityError,
		Message:   fmt.Sprintf("Ядро нестабильно (%d сбоев)", s.Engine.CrashCount),
		Action:    "",
	})

	r = append(r, CheckResult{
		CheckID:   "engine.port_unavailable",
		Component: "engine",
		Healthy:   s.Engine.PortListening,
		Severity:  SeverityCritical,
		Message:   "Порт перехвата TProxy недоступен",
		Action:    "restart_engine",
	})

	r = append(r, CheckResult{
		CheckID:   "engine.config_invalid",
		Component: "engine",
		Healthy:   s.Engine.ConfigValid,
		Severity:  SeverityCritical,
		Message:   "Ошибка конфигурации ядра",
		Action:    "",
	})

	// --- DNS checks (4) ---
	r = append(r, CheckResult{
		CheckID:   "dns.listener_down",
		Component: "dns",
		Healthy:   s.DNS.ListenerAlive,
		Severity:  SeverityCritical,
		Message:   "DNS-listener не отвечает на запросы",
		Action:    "restart_engine",
	})

	r = append(r, CheckResult{
		CheckID:   "dns.proxy_unavailable",
		Component: "dns",
		Healthy:   s.DNS.ProxyDNSWorking,
		Severity:  SeverityCritical,
		Message:   "DNS через proxy-туннель не работает",
		Action:    "restart_engine",
	})

	r = append(r, CheckResult{
		CheckID:   "dns.bootstrap_failed",
		Component: "dns",
		Healthy:   s.DNS.BootstrapAlive,
		Severity:  SeverityError,
		Message:   "Bootstrap DNS сервер недоступен",
		Action:    "",
	})

	r = append(r, CheckResult{
		CheckID:   "dns.high_latency",
		Component: "dns",
		Healthy:   s.DNS.LatencyMs < 600,
		Severity:  SeverityWarning,
		Message:   fmt.Sprintf("Высокая задержка DNS (%d ms)", s.DNS.LatencyMs),
		Action:    "",
	})

	// --- Connectivity checks (2) ---
	r = append(r, CheckResult{
		CheckID:   "connectivity.internet_unreachable",
		Component: "connectivity",
		Healthy:   s.Network.InternetDirect,
		Severity:  SeverityWarning,
		Message:   "Прямое интернет-соединение недоступно",
		Action:    "",
	})

	r = append(r, CheckResult{
		CheckID:   "connectivity.proxy_e2e_failed",
		Component: "connectivity",
		Healthy:   s.Network.E2EProxyWorking,
		Severity:  SeverityError,
		Message:   "HTTP E2E трафик через туннель не проходит",
		Action:    "restart_engine",
	})

	// --- Nodes checks (3) ---
	r = append(r, CheckResult{
		CheckID:   "nodes.no_available",
		Component: "nodes",
		Healthy:   s.Network.AvailableNodes > 0 || s.Network.TotalNodes == 0,
		Severity:  SeverityCritical,
		Message:   "Все узлы подключения недоступны",
		Action:    "restart_engine",
	})

	var partialHealthy = true
	if s.Network.TotalNodes > 1 && s.Network.AvailableNodes < (s.Network.TotalNodes/2) {
		partialHealthy = false
	}
	r = append(r, CheckResult{
		CheckID:   "nodes.partial_unavailable",
		Component: "nodes",
		Healthy:   partialHealthy,
		Severity:  SeverityWarning,
		Message:   fmt.Sprintf("Доступно менее половины узлов (%d/%d)", s.Network.AvailableNodes, s.Network.TotalNodes),
		Action:    "",
	})

	r = append(r, CheckResult{
		CheckID:   "nodes.all_failed",
		Component: "nodes",
		Healthy:   s.Network.AvailableNodes > 0 || s.Network.TotalNodes == 0,
		Severity:  SeverityCritical,
		Message:   "Все серверы подписки упали по таймауту",
		Action:    "restart_engine",
	})

	return r
}

// CheckSystemRouting возвращает 3 системные проверки сетевого стека и фаервола
func CheckSystemRouting(ctx context.Context, expectedTproxyPort int) []CheckResult {
	var r []CheckResult

	// 1. Проверяем ip rule на наличие fwmark (0x100000 / 1048576 или 0x200000 / 2097152)
	out, err := exec.CommandContext(ctx, "ip", "rule", "show").Output()
	outStr := string(out)
	hasFwmarkRule := err == nil && (strings.Contains(outStr, "1048576") || strings.Contains(outStr, "2097152") || strings.Contains(outStr, "0x100000") || strings.Contains(outStr, "0x200000"))

	r = append(r, CheckResult{
		CheckID:   "routing.ip_rule_missing",
		Component: "routing",
		Healthy:   hasFwmarkRule,
		Severity:  SeverityCritical,
		Message:   "Правило ip rule (fwmark) отсутствует",
		Action:    "fix_routing",
	})

	// 2. Проверяем таблицу nftables CheburTable
	nftOut, nftErr := exec.CommandContext(ctx, "nft", "list", "table", "inet", network.TableName).Output()
	nftStr := string(nftOut)
	hasNFT := nftErr == nil && strings.Contains(nftStr, "tproxy")

	r = append(r, CheckResult{
		CheckID:   "routing.nftables_invalid",
		Component: "routing",
		Healthy:   hasNFT,
		Severity:  SeverityCritical,
		Message:   "Таблица nftables cheburnet повреждена или не загружена",
		Action:    "reload_firewall",
	})

	// 3. Проверяем соответствие TProxy порта в правилах
	driftHealthy := true
	if hasNFT && expectedTproxyPort > 0 {
		if !strings.Contains(nftStr, fmt.Sprintf("%d", expectedTproxyPort)) {
			driftHealthy = false
		}
	}
	r = append(r, CheckResult{
		CheckID:   "config.drift",
		Component: "config",
		Healthy:   driftHealthy,
		Severity:  SeverityError,
		Message:   "TProxy порт в nftables отличается от конфигурации",
		Action:    "reload_firewall",
	})

	return r
}
