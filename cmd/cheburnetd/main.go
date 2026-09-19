package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"cheburnet/internal/api"
	"cheburnet/internal/config"
	"cheburnet/internal/diagnostics"
	"cheburnet/internal/engine"
	"cheburnet/internal/network"
	"cheburnet/internal/ruleset"
	"cheburnet/internal/service"
	"cheburnet/internal/subscription"
	"cheburnet/internal/telemetry"
	"cheburnet/internal/updater"
	"cheburnet/pkg/uri"
)

var (
	CheburVersion            = "0.0.1-singbox-dev"
	RuntimeConfigPathSingBox = "/tmp/run/cheburnet/sing-box.json"
	DefaultAPIBind           = "0.0.0.0:8088"
	PIDFile                  = "/var/run/cheburnetd.pid"
)

type diagReporterAdapter struct {
	diag *diagnostics.DiagnosticsEngine
}

func (a *diagReporterAdapter) ReportProblem(id, component, severity, message, action string, recoverable bool) {
	if a.diag == nil {
		return
	}

	var sev diagnostics.Severity
	switch strings.ToLower(severity) {
	case "critical":
		sev = diagnostics.SeverityCritical
	case "warning":
		sev = diagnostics.SeverityWarning
	default:
		sev = diagnostics.SeverityError
	}

	a.diag.Report(diagnostics.CheckResult{
		CheckID:   id,
		Component: component,
		Healthy:   false,
		Severity:  sev,
		Message:   message,
		Action:    action,
	})
}

func (a *diagReporterAdapter) ResolveProblem(id string) {
	if a.diag == nil {
		return
	}

	a.diag.Report(diagnostics.CheckResult{
		CheckID: id,
		Healthy: true,
	})
}

type App struct {
	state         *config.StateManager
	singboxEng    *engine.SingBoxEngine
	activeEng     engine.Engine
	hub           *telemetry.Hub
	server        *api.Server
	rulesLoader   *network.CompressedRulesetLoader
	rulesCron     *network.RulesetCron
	healthTracker *engine.HealthTracker
	diagEngine    *diagnostics.DiagnosticsEngine
	rulesMgr      *ruleset.Manager
	updManager    *updater.Manager
	mu            sync.RWMutex
	engineOpMu    sync.Mutex
}

func showHelp() {
	fmt.Printf("Usage: cheburnetd [COMMAND]\n\n" +
		"Service Management:\n" +
		"    start                   Start cheburnet daemon service (foreground)\n" +
		"    stop                    Stop cheburnet background daemon\n" +
		"    restart                 Restart daemon service via procd\n" +
		"    reload                  Reload configuration without dropping routing\n" +
		"    list_update             Update subscriptions and rulesets\n" +
		"    check_updates           Check component and daemon updates\n" +
		"    upgrade [target]        Run upgrade (target: all | cheburnet | sing-box)\n\n" +
		"Diagnostics & Network:\n" +
		"    check_proxy             Check proxy connectivity through mixed port\n" +
		"    check_nft               Check NFT rules presence\n" +
		"    check_nft_rules         Check NFT mangle/proxy rule counters\n" +
		"    check_engine            Check proxy engine (sing-box) process status\n" +
		"    check_dns_available     Check local and upstream DNS availability\n" +
		"    check_logs              Show journal logs filtered by cheburnet\n" +
		"    global_check            Run end-to-end system diagnostic\n\n" +
		"Inspection & Management:\n" +
		"    show_config             Display parsed UCI configuration\n" +
		"    show_engine_config      Show generated JSON config of sing-box\n" +
		"    show_version            Show Chebur.NET daemon version\n" +
		"    get_status              Get daemon status (JSON)\n" +
		"    get_system_info         Get device and OS specs (JSON)\n")
}

// setupBootstrapResolver конфигурирует net.DefaultResolver для Bootstrap DNS-запросов.
func setupBootstrapResolver(cfg *config.CheburConfig) {
	endpoints := []string{"77.88.8.8:53", "8.8.8.8:53", "1.1.1.1:53"}

	if cfg != nil && strings.TrimSpace(cfg.BootstrapDNS) != "" {
		b := strings.TrimSpace(cfg.BootstrapDNS)
		if !strings.Contains(b, ":") {
			b += ":53"
		}
		endpoints = append([]string{b}, endpoints...)
	}

	directDialer := &net.Dialer{
		Timeout: 3 * time.Second,
	}

	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			var lastErr error
			for _, ep := range endpoints {
				conn, err := directDialer.DialContext(ctx, "udp", ep)
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			return nil, lastErr
		},
	}
}

func main() {
	if len(os.Args) < 2 {
		showHelp()
		os.Exit(0)
	}

	cmd := os.Args[1]

	switch cmd {
	case "start":
		runDaemon()

	case "stop":
		stopDaemon()

	case "restart":
		_ = service.RestartAsync()

	case "reload":
		data, err := os.ReadFile(PIDFile)
		if err != nil {
			fmt.Printf("Chebur.NET is not running (cannot read %s): %v\n", PIDFile, err)
			os.Exit(1)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil || pid <= 1 {
			fmt.Printf("Invalid PID found in %s\n", PIDFile)
			os.Exit(1)
		}
		if err := syscall.Kill(pid, syscall.SIGHUP); err != nil {
			fmt.Printf("Failed to send SIGHUP to pid %d: %v\n", pid, err)
			os.Exit(1)
		}
		fmt.Println("Reload signal sent successfully.")

	case "list_update":
		callAPI(http.MethodPost, "/api/v1/subscriptions/update", nil)

	case "check_updates":
		callAPI(http.MethodGet, "/api/v1/updates/check", nil)

	case "upgrade":
		target := "all"
		if len(os.Args) >= 3 {
			target = os.Args[2]
		}
		body := fmt.Sprintf(`{"target":"%s"}`, target)
		callAPI(http.MethodPost, "/api/v1/updates/upgrade", strings.NewReader(body))

	case "check_proxy":
		cliCheckProxy()

	case "check_nft", "check_nft_rules":
		cliCheckNFT()

	case "check_engine":
		cliCheckEngine()

	case "check_dns_available":
		cliCheckDNS()

	case "check_logs":
		out, _ := exec.Command("logread", "-e", "cheburnet").CombinedOutput()
		fmt.Println(string(out))

	case "show_config":
		out, _ := exec.Command("uci", "show", "cheburnet").CombinedOutput()
		fmt.Println(string(out))

	case "show_engine_config":
		cliShowEngineConfig()

	case "show_version":
		fmt.Println("Chebur.NET version:", CheburVersion)

	case "get_status":
		callAPI(http.MethodGet, "/api/v1/status", nil)

	case "get_system_info":
		cliGetSystemInfo()

	case "global_check":
		cliGlobalCheck()

	case "-h", "--help", "help":
		showHelp()

	default:
		fmt.Printf("Unknown command: %s\n\n", cmd)
		showHelp()
		os.Exit(1)
	}
}

func collectAllRuleSets(cfg *config.CheburConfig) []string {
	unique := make(map[string]struct{})

	addClean := func(raw string) {
		clean := strings.TrimSpace(raw)
		clean = strings.Trim(clean, "'\"`")
		clean = strings.ToLower(clean)
		if clean != "" {
			unique[clean] = struct{}{}
		}
	}

	for _, rs := range cfg.RuleSets {
		addClean(rs)
	}
	for _, rp := range cfg.RoutePolicies {
		if rp.Enabled {
			for _, rs := range rp.RuleSets {
				addClean(rs)
			}
		}
	}

	result := make([]string, 0, len(unique))
	for tag := range unique {
		result = append(result, tag)
	}
	return result
}

func loadSubnetsFromCompressedStorage(loader *network.CompressedRulesetLoader, ruleSets []string) []string {
	var subnets []string
	for _, rs := range ruleSets {
		list, err := loader.GetSubnets(rs)
		if err == nil && len(list) > 0 {
			subnets = append(subnets, list...)
		}
	}
	return subnets
}

func extractFullProxyIPs(policies []config.ClientPolicy) []string {
	var ips []string
	leasesMap := loadDHCPLeasesMap()

	for _, p := range policies {
		if !p.Enabled || p.Mode != config.ClientModeFullProxy || p.Target == "" {
			continue
		}
		target := strings.TrimSpace(p.Target)

		if strings.Contains(target, ":") && !strings.Contains(target, ".") {
			if ip, exists := leasesMap[strings.ToLower(target)]; exists {
				target = ip
			}
		}

		cleanIP := strings.Split(target, "/")[0]
		if net.ParseIP(cleanIP) != nil {
			ips = append(ips, cleanIP)
		}
	}
	return ips
}

func loadDHCPLeasesMap() map[string]string {
	leases := make(map[string]string)
	file, err := os.Open("/tmp/dhcp.leases")
	if err != nil {
		return leases
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 3 {
			mac := strings.ToLower(fields[1])
			ip := fields[2]
			leases[mac] = ip
		}
	}
	return leases
}

func getRealActiveNode(defaultTag string) string {
	client := &http.Client{Timeout: 800 * time.Millisecond}
	urls := []string{
		"http://127.0.0.1:9090/proxies",
		"http://192.168.11.1:9090/proxies",
	}

	var resp *http.Response
	var err error

	for _, u := range urls {
		r, e := client.Get(u)
		if e == nil && r != nil && r.StatusCode == http.StatusOK {
			resp = r
			break
		}
		if r != nil {
			_ = r.Body.Close()
		}
	}

	if resp == nil {
		return defaultTag
	}
	defer resp.Body.Close()

	var result struct {
		Proxies map[string]struct {
			Type string `json:"type"`
			Now  string `json:"now"`
		} `json:"proxies"`
	}

	if err = json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return defaultTag
	}

	resolveTarget := func(nodeName string) string {
		curr := nodeName
		visited := make(map[string]bool)
		for i := 0; i < 4; i++ {
			if curr == "" || visited[curr] {
				break
			}
			visited[curr] = true
			if group, exists := result.Proxies[curr]; exists && group.Now != "" && group.Now != curr {
				curr = group.Now
			} else {
				break
			}
		}
		return curr
	}

	for _, name := range []string{"PROXY", "proxy", "auto", "AUTO", "auto-out"} {
		if group, ok := result.Proxies[name]; ok && group.Now != "" {
			resolved := resolveTarget(group.Now)
			if resolved != "" {
				return resolved
			}
		}
	}

	for _, p := range result.Proxies {
		typ := strings.ToLower(p.Type)
		if (typ == "urltest" || typ == "selector") && p.Now != "" {
			resolved := resolveTarget(p.Now)
			if resolved != "" {
				return resolved
			}
		}
	}

	return defaultTag
}

func acquirePIDLock(pidPath string) (*os.File, error) {
	file, err := os.OpenFile(pidPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open PID file: %w", err)
	}

	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("another daemon instance is already running (PID locked): %w", err)
	}

	if err := file.Truncate(0); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return nil, fmt.Errorf("failed to truncate PID file: %w", err)
	}

	if _, err := file.Seek(0, 0); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return nil, fmt.Errorf("failed to seek PID file: %w", err)
	}

	if _, err := file.WriteString(fmt.Sprintf("%d\n", os.Getpid())); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return nil, fmt.Errorf("failed to write PID: %w", err)
	}

	_ = file.Sync()
	return file, nil
}

func runDaemon() {
	pidLockFile, err := acquirePIDLock(PIDFile)
	if err != nil {
		log.Fatalf("[FATAL] Startup aborted: %v", err)
	}
	defer func() {
		_ = syscall.Flock(int(pidLockFile.Fd()), syscall.LOCK_UN)
		_ = pidLockFile.Close()
		_ = os.Remove(PIDFile)
	}()

	network.CleanupRouting()
	_ = network.FlushNFTRules()
	network.RestoreDnsmasq()

	defer func() {
		if r := recover(); r != nil {
			log.Printf("[PANIC RECOVER] %v", r)
		}
		network.CleanupRouting()
		_ = network.FlushNFTRules()
		network.RestoreDnsmasq()
	}()

	ipv6Mgr := network.NewIPv6Manager()
	if err := ipv6Mgr.EnsureIPv6Disabled(); err != nil {
		log.Printf("[WARN] Failed to configure IPv6 state: %v", err)
	}

	uciStorage := config.NewUCIStorage()
	initialConfig, err := uciStorage.Load()
	if err != nil {
		log.Fatalf("[FATAL] Load config failed: %v", err)
	}

	setupBootstrapResolver(initialConfig)

	initialConfig.Engine = "sing-box"
	if initialConfig.RulesetUpdateInterval == "" {
		initialConfig.RulesetUpdateInterval = "72h"
	}
	if initialConfig.UpdateChannel == "" {
		initialConfig.UpdateChannel = "release"
	}

	state := config.NewStateManager(initialConfig)

	// Быстрый локальный опрос состояния ядра для SmartTransport
	engineAliveChecker := func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		cfg := state.Get()
		return engine.VerifyEngineAlive(ctx, &cfg) == nil
	}

	// Инициализируем subscription.Worker с передачей initialConfig.MixedPort
	subWorker := subscription.NewWorker(initialConfig.AutoHWID, initialConfig.CustomHWID, initialConfig.MixedPort, engineAliveChecker)

	switch initialConfig.SourceMode {
	case "manual":
		for _, raw := range initialConfig.ManualNodes {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			node, err := uri.ParseNodeURI(raw, initialConfig.AutoHWID, initialConfig.CustomHWID)
			if err != nil {
				log.Printf("[ERROR] Failed to parse manual node '%s': %v", raw, err)
				continue
			}
			log.Printf("[INFO] Loaded manual node: %s (%s)", node.Tag, node.Protocol)
			initialConfig.Nodes = append(initialConfig.Nodes, node)
		}

	default:
		for _, sub := range initialConfig.Subscriptions {
			sub.URL = strings.TrimSpace(sub.URL)
			if sub.URL == "" || !sub.Enabled {
				continue
			}
			log.Printf("[INFO] Fetching subscription [%s]: %s", sub.UserAgent, sub.URL)
			nodes, err := subWorker.FetchNodes(context.Background(), sub)
			if err != nil {
				log.Printf("[ERROR] Failed to fetch subscription '%s': %v", sub.URL, err)
				continue
			}
			log.Printf("[INFO] Successfully fetched %d nodes from %s", len(nodes), sub.URL)
			initialConfig.Nodes = append(initialConfig.Nodes, nodes...)
		}
	}

	log.Printf("[INFO] Total active nodes initialized: %d (source mode: %s)", len(initialConfig.Nodes), initialConfig.SourceMode)

	// ВАЖНО: Фиксируем полученные ноды в StateManager перед запуском движка!
	state.Update(func(c *config.CheburConfig) {
		c.Nodes = initialConfig.Nodes
	})

	rulesLoader := network.NewCompressedRulesetLoader()
	allRuleSets := collectAllRuleSets(initialConfig)

	allSubnets := append([]string(nil), initialConfig.CustomSubnets...)
	for _, rp := range initialConfig.RoutePolicies {
		if rp.Enabled && len(rp.Subnets) > 0 {
			allSubnets = append(allSubnets, rp.Subnets...)
		}
	}
	if len(allRuleSets) > 0 {
		log.Printf("[INFO] Loading cached subnets for all rulesets: %v", allRuleSets)
		fetched := loadSubnetsFromCompressedStorage(rulesLoader, allRuleSets)
		allSubnets = append(allSubnets, fetched...)
		log.Printf("[INFO] Total subnets loaded for direct routing: %d", len(allSubnets))
	}

	sbEngine := engine.NewSingBoxEngine()
	hub := telemetry.NewHub()
	healthTracker := engine.NewHealthTracker()
	diagEngine := diagnostics.NewEngine(initialConfig.TProxyPort)
	hub.SetDiagnosticsEngine(diagEngine)

	updManager := updater.NewManager("ph4n70m1984/cheburnet", CheburVersion, initialConfig.UpdateChannel, initialConfig.MixedPort, engineAliveChecker)
	rulesMgr := ruleset.NewManager(&diagReporterAdapter{diag: diagEngine}, initialConfig.MixedPort)

	if len(initialConfig.CustomSRSRulesets) > 0 {
		log.Printf("[INFO] Syncing %d custom SRS rulesets...", len(initialConfig.CustomSRSRulesets))
		rulesMgr.SyncAll(initialConfig.CustomSRSRulesets)
	}

	if len(allRuleSets) > 0 {
		log.Printf("[INFO] Pre-caching %d system SRS files to /tmp/cheburnet/rulesets...", len(allRuleSets))
		for _, rs := range allRuleSets {
			path, srsErr := rulesMgr.FetchSystemRuleSet(rs)
			if srsErr != nil {
				log.Printf("[WARN] Pre-cache failed for %s: %v", rs, srsErr)
			} else {
				log.Printf("[INFO] Pre-cached SRS ready: %s -> %s", rs, path)
			}
		}
	}

	app := &App{
		state:         state,
		singboxEng:    sbEngine,
		activeEng:     sbEngine,
		hub:           hub,
		rulesLoader:   rulesLoader,
		healthTracker: healthTracker,
		diagEngine:    diagEngine,
		rulesMgr:      rulesMgr,
		updManager:    updManager,
	}

	sourceIface := initialConfig.SourceIface
	if sourceIface == "" || sourceIface == "lan" {
		sourceIface = "br-lan"
	}

	isGlobal := initialConfig.RoutingMode == "global"
	fullProxyIPs := extractFullProxyIPs(initialConfig.ClientPolicies)

	log.Printf("[INFO] Setting up nftables and routing (global: %v, full_proxy clients: %v)...", isGlobal, fullProxyIPs)
	if err := network.ApplyNFTRules([]string{sourceIface}, allSubnets, fullProxyIPs, initialConfig.TProxyPort, isGlobal); err != nil {
		log.Fatalf("[FATAL] nftables setup error: %v", err)
	}
	if err := network.SetupRouting(); err != nil {
		log.Fatalf("[FATAL] routing setup error: %v", err)
	}
	_ = network.ConfigureDnsmasq(initialConfig.DNSPort)

	daemonCtx, daemonCancel := context.WithCancel(context.Background())
	defer daemonCancel()

	if err := app.startActiveEngine(daemonCtx); err != nil {
		log.Fatalf("[FATAL] Engine sing-box failed to start: %v", err)
	}

	app.rulesCron = network.NewRulesetCron(
		rulesLoader,
		allRuleSets,
		initialConfig.RulesetUpdateInterval,
		func() error {
			return app.reloadActiveEngine(daemonCtx)
		},
	)
	app.rulesCron.Start(daemonCtx)
	diagEngine.StartBackgroundLoop(daemonCtx)

	updManager.StartAutoUpdateLoop(
		daemonCtx,
		func() bool {
			cfg := app.state.Get()
			return cfg.AutoUpdate
		},
		func() {
			log.Println("[INFO] Restarting daemon via procd ubus after auto-upgrade...")
			_ = service.RestartAsync()
		},
	)

	go hub.Run(daemonCtx, app.getCurrentEngine, func() string {
		cfg := app.state.Get()
		fallback := ""
		if len(cfg.Nodes) > 0 {
			fallback = cfg.Nodes[0].Tag
		}
		return getRealActiveNode(fallback)
	})

	srv := api.NewServer(
		state,
		hub,
		subWorker,
		updManager,
		app.getCurrentEngine,
		func(name string) error {
			return fmt.Errorf("engine switching is disabled: sing-box is the dedicated core")
		},
		app.rulesCron,
		diagEngine,
		func(action string) error {
			switch action {
			case "restart_engine":
				return app.restartActiveEngine(daemonCtx)
			case "reload_firewall":
				return exec.Command("fw4", "reload").Run()
			case "fix_routing":
				if err := network.SetupRouting(); err != nil {
					return fmt.Errorf("setup routing: %w", err)
				}
				cfg := app.state.Get()
				isGlobalMode := cfg.RoutingMode == "global"
				sIface := cfg.SourceIface
				if sIface == "" || sIface == "lan" {
					sIface = "br-lan"
				}
				activeSets := collectAllRuleSets(&cfg)
				subnets := append([]string(nil), cfg.CustomSubnets...)
				for _, rp := range cfg.RoutePolicies {
					if rp.Enabled && len(rp.Subnets) > 0 {
						subnets = append(subnets, rp.Subnets...)
					}
				}
				if len(activeSets) > 0 && app.rulesLoader != nil {
					fetched := loadSubnetsFromCompressedStorage(app.rulesLoader, activeSets)
					subnets = append(subnets, fetched...)
				}
				fpIPs := extractFullProxyIPs(cfg.ClientPolicies)
				return network.ApplyNFTRules([]string{sIface}, subnets, fpIPs, cfg.TProxyPort, isGlobalMode)
			default:
				return fmt.Errorf("action %s is not supported", action)
			}
		},
	)
	app.server = srv

	go func() {
		if err := srv.Listen(DefaultAPIBind); err != nil {
			log.Printf("[INFO] API Server stopped: %v", err)
		}
	}()

	go app.supervisorLoop(daemonCtx)

	sigChan := make(chan os.Signal, 2)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	for sig := range sigChan {
		if sig == syscall.SIGHUP {
			log.Println("[INFO] Received SIGHUP: executing soft reload...")
			go func() {
				if err := app.reloadActiveEngine(daemonCtx); err != nil {
					log.Printf("[ERROR] SIGHUP soft reload failed: %v", err)
				} else {
					log.Println("[INFO] SIGHUP soft reload completed successfully.")
				}
			}()
			continue
		}

		break
	}

	log.Println("[INFO] Shutting down Chebur.NET...")
	_ = srv.Shutdown()
	app.stopActiveEngine()
	network.CleanupRouting()
	_ = network.FlushNFTRules()
	network.RestoreDnsmasq()
	log.Println("[INFO] Stopped.")
}

func stopDaemon() {
	data, err := os.ReadFile(PIDFile)
	if err != nil {
		_ = exec.Command("killall", "cheburnetd").Run()
		return
	}
	pid := strings.TrimSpace(string(data))
	_ = exec.Command("kill", pid).Run()
	_ = os.Remove(PIDFile)
}

func (a *App) getCurrentEngine() engine.Engine {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.activeEng
}

func (a *App) reloadActiveEngine(ctx context.Context) error {
	a.engineOpMu.Lock()
	defer a.engineOpMu.Unlock()

	a.mu.RLock()
	eng := a.activeEng
	cfg := a.state.Get()
	a.mu.RUnlock()

	if eng == nil {
		return fmt.Errorf("no active engine")
	}

	setupBootstrapResolver(&cfg)

	if a.updManager != nil && cfg.UpdateChannel != "" {
		a.updManager.SetUpdateChannel(cfg.UpdateChannel)
	}

	if a.rulesMgr != nil && len(cfg.CustomSRSRulesets) > 0 {
		log.Printf("[INFO] Reload: Syncing %d custom SRS rulesets...", len(cfg.CustomSRSRulesets))
		a.rulesMgr.SyncAll(cfg.CustomSRSRulesets)
	}

	targetPath := RuntimeConfigPathSingBox
	allRuleSets := collectAllRuleSets(&cfg)

	if a.rulesMgr != nil && len(allRuleSets) > 0 {
		for _, rs := range allRuleSets {
			_, _ = a.rulesMgr.FetchSystemRuleSet(rs)
		}
	}

	if a.rulesCron != nil {
		a.rulesCron.UpdateRulesets(allRuleSets)
	}

	isGlobal := cfg.RoutingMode == "global"
	sourceIface := cfg.SourceIface
	if sourceIface == "" || sourceIface == "lan" {
		sourceIface = "br-lan"
	}

	allSubnets := append([]string(nil), cfg.CustomSubnets...)
	for _, rp := range cfg.RoutePolicies {
		if rp.Enabled && len(rp.Subnets) > 0 {
			allSubnets = append(allSubnets, rp.Subnets...)
		}
	}
	if len(allRuleSets) > 0 && a.rulesLoader != nil {
		fetched := loadSubnetsFromCompressedStorage(a.rulesLoader, allRuleSets)
		allSubnets = append(allSubnets, fetched...)
	}

	fullProxyIPs := extractFullProxyIPs(cfg.ClientPolicies)
	if err := network.ApplyNFTRules([]string{sourceIface}, allSubnets, fullProxyIPs, cfg.TProxyPort, isGlobal); err != nil {
		log.Printf("[WARN] Failed to re-apply nftables rules on reload: %v", err)
	}

	return engine.SafeReload(ctx, eng, &cfg, targetPath)
}

func (a *App) restartActiveEngine(ctx context.Context) error {
	a.engineOpMu.Lock()
	defer a.engineOpMu.Unlock()

	a.mu.RLock()
	eng := a.activeEng
	a.mu.RUnlock()

	if eng == nil {
		return fmt.Errorf("no active engine")
	}

	targetPath := RuntimeConfigPathSingBox
	_ = eng.Stop()
	return eng.Start(ctx, targetPath)
}

func (a *App) startActiveEngine(ctx context.Context) error {
	a.engineOpMu.Lock()
	defer a.engineOpMu.Unlock()

	a.mu.RLock()
	eng := a.activeEng
	cfg := a.state.Get()
	a.mu.RUnlock()

	if eng == nil {
		return fmt.Errorf("no active engine")
	}

	targetPath := RuntimeConfigPathSingBox

	if err := a.singboxEng.EnsureAssets(ctx); err != nil {
		return fmt.Errorf("ensure assets for sing-box failed: %w", err)
	}

	if err := eng.BuildConfig(&cfg, targetPath); err != nil {
		return fmt.Errorf("build %s config failed: %w", eng.Name(), err)
	}

	if err := a.singboxEng.ValidateConfig(ctx, targetPath); err != nil {
		return fmt.Errorf("sing-box validate config failed: %w", err)
	}

	return eng.Start(ctx, targetPath)
}

func (a *App) stopActiveEngine() {
	a.engineOpMu.Lock()
	defer a.engineOpMu.Unlock()

	a.mu.RLock()
	eng := a.activeEng
	a.mu.RUnlock()

	if eng != nil {
		_ = eng.Stop()
	}
}

func (a *App) supervisorLoop(ctx context.Context) {
	backoffDelays := []time.Duration{
		0 * time.Second,
		5 * time.Second,
		15 * time.Second,
		30 * time.Second,
		60 * time.Second,
	}
	const faultCooldown = 5 * time.Minute
	const l1Interval = 10 * time.Second
	const l2Interval = 60 * time.Second

	l1Failures := 0
	l2Failures := 0
	restartAttempts := 0
	isInFaultState := false

	l1Ticker := time.NewTicker(l1Interval)
	l2Ticker := time.NewTicker(l2Interval)
	defer l1Ticker.Stop()
	defer l2Ticker.Stop()

	triggerRestart := func(reason string) {
		if restartAttempts >= len(backoffDelays) {
			if !isInFaultState {
				isInFaultState = true
				log.Printf("[supervisor] CRITICAL: Engine reached max restart attempts (%d). Entering ENGINE_FAULT state! Cooldown: %v",
					restartAttempts, faultCooldown)
			}
			time.Sleep(faultCooldown)
			restartAttempts = 0
			return
		}

		delay := backoffDelays[restartAttempts]
		restartAttempts++

		log.Printf("[supervisor] Restarting engine due to [%s] (attempt %d/%d, delay: %v)...",
			reason, restartAttempts, len(backoffDelays), delay)

		if delay > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
		}

		a.engineOpMu.Lock()
		a.mu.RLock()
		currentEng := a.activeEng
		a.mu.RUnlock()

		if currentEng != nil {
			targetPath := RuntimeConfigPathSingBox

			_ = currentEng.Stop()
			if startErr := currentEng.Start(ctx, targetPath); startErr != nil {
				log.Printf("[supervisor] ERROR: Engine %s restart failed: %v", currentEng.Name(), startErr)
				if a.healthTracker != nil {
					a.healthTracker.UpdateEngine(false, 0, false, true, true, true)
				}
			} else {
				log.Printf("[supervisor] INFO: Engine %s successfully restarted", currentEng.Name())
				if a.healthTracker != nil {
					a.healthTracker.UpdateEngine(true, 0, true, true, true, false)
				}
			}
		}
		a.engineOpMu.Unlock()
	}

	for {
		select {
		case <-ctx.Done():
			return

		case <-l1Ticker.C:
			a.mu.RLock()
			eng := a.activeEng
			cfg := a.state.Get()
			a.mu.RUnlock()

			if eng == nil || len(cfg.Nodes) == 0 {
				continue
			}

			healthCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			err := engine.VerifyEngineAlive(healthCtx, &cfg)
			cancel()

			isAlive := (err == nil)
			if a.healthTracker != nil {
				a.healthTracker.UpdateEngine(isAlive, 0, isAlive, true, false, !isAlive)
				a.healthTracker.UpdateDNS(isAlive, isAlive, true, 20)
				if a.diagEngine != nil {
					a.diagEngine.ProcessSnapshot(a.healthTracker.Snapshot())
				}
			}

			if err != nil {
				l1Failures++
				log.Printf("[supervisor] L1 Warning: Engine %s local check failed (%d/3): %v", eng.Name(), l1Failures, err)
				if l1Failures >= 3 {
					l1Failures = 0
					if a.healthTracker != nil {
						a.healthTracker.UpdateEngine(false, 0, false, true, false, true)
						if a.diagEngine != nil {
							a.diagEngine.ProcessSnapshot(a.healthTracker.Snapshot())
						}
					}
					triggerRestart("L1_PROCESS_DEAD")
				}
			} else {
				if l1Failures > 0 {
					log.Printf("[supervisor] L1 INFO: Engine %s local health recovered", eng.Name())
					l1Failures = 0
				}
				if restartAttempts > 0 && l2Failures == 0 {
					restartAttempts = 0
					isInFaultState = false
				}
			}

		case <-l2Ticker.C:
			a.mu.RLock()
			eng := a.activeEng
			cfg := a.state.Get()
			a.mu.RUnlock()

			if eng == nil || len(cfg.Nodes) == 0 {
				continue
			}

			if l1Failures > 0 {
				continue
			}

			trafficCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			startTraffic := time.Now()
			err := engine.VerifyTraffic(trafficCtx, &cfg)
			cancel()

			e2eOk := (err == nil)
			latency := time.Since(startTraffic).Milliseconds()
			if a.healthTracker != nil {
				a.healthTracker.UpdateNetwork(e2eOk, true, latency, len(cfg.Nodes), len(cfg.Nodes))
				if a.diagEngine != nil {
					a.diagEngine.ProcessSnapshot(a.healthTracker.Snapshot())
				}
			}

			if err != nil {
				l2Failures++
				log.Printf("[supervisor] L2 Warning: Proxy traffic test failed (%d/2): %v", l2Failures, err)
				if l2Failures >= 2 {
					l2Failures = 0
					triggerRestart("L2_TRAFFIC_DEAD")
				}
			} else {
				if l2Failures > 0 {
					log.Printf("[supervisor] L2 INFO: Proxy traffic pipeline restored")
					l2Failures = 0
				}
				if restartAttempts > 0 && l1Failures == 0 {
					restartAttempts = 0
					isInFaultState = false
				}
			}
		}
	}
}

func callAPI(method, endpoint string, body io.Reader) {
	client := &http.Client{Timeout: 30 * time.Second}
	// Запросы шлем строго на петлю 127.0.0.1
	req, err := http.NewRequest(method, "http://127.0.0.1:8088"+endpoint, body)
	if err != nil {
		fmt.Printf("Error creating request: %v\n", err)
		return
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	uciStorage := config.NewUCIStorage()
	if cfg, err := uciStorage.Load(); err == nil && cfg != nil {
		token := strings.TrimSpace(cfg.APIToken)
		if token == "" {
			token = strings.TrimSpace(cfg.ClashAPISecret)
		}
		if token != "" {
			req.Header.Set("X-API-Token", token)
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("Daemon API is not responding (is cheburnetd running?): %v\n", err)
		return
	}
	defer resp.Body.Close()

	out, _ := io.ReadAll(resp.Body)
	fmt.Println(string(out))
}

func cliCheckProxy() {
	fmt.Println("Testing connectivity via Chebur.NET mixed inbound (127.0.0.1:4534)...")
	proxyURL, _ := url.Parse("http://127.0.0.1:4534")
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		},
		Timeout: 7 * time.Second,
	}

	resp, err := client.Get("https://api.ipify.org")
	if err != nil {
		resp, err = client.Get("https://icanhazip.com")
		if err != nil {
			fmt.Printf("Proxy check FAILED: %v\n", err)
			return
		}
	}
	defer resp.Body.Close()

	ip, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		fmt.Printf("Proxy check error (HTTP %d)\n", resp.StatusCode)
		return
	}

	fmt.Printf("Proxy check OK! Outgoing IP: %s\n", strings.TrimSpace(string(ip)))
}

func cliCheckNFT() {
	out, err := exec.Command("nft", "list", "table", "inet", network.TableName).CombinedOutput()
	if err != nil {
		fmt.Printf("NFT Table %s NOT FOUND or error: %v\n", network.TableName, err)
		return
	}
	fmt.Println(string(out))
}

func cliCheckEngine() {
	sbRunning := exec.Command("pgrep", "-f", "sing-box").Run() == nil
	res := map[string]interface{}{
		"sing_box_running": sbRunning,
	}
	data, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(data))
}

func testDNSQuery(ctx context.Context, serverAddr string, isDoH bool) (bool, int64, string) {
	start := time.Now()

	if isDoH {
		targetURL := serverAddr
		if !strings.HasPrefix(targetURL, "http://") && !strings.HasPrefix(targetURL, "https://") {
			targetURL = fmt.Sprintf("https://%s/dns-query", serverAddr)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL+"?name=google.com&type=A", nil)
		if err != nil {
			return false, 0, err.Error()
		}
		req.Header.Set("Accept", "application/dns-json")

		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return false, 0, err.Error()
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			return true, time.Since(start).Milliseconds(), ""
		}
		return false, 0, fmt.Sprintf("HTTP %d", resp.StatusCode)
	}

	dialTarget := serverAddr
	if !strings.Contains(dialTarget, ":") {
		dialTarget += ":53"
	}

	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{Timeout: 2 * time.Second}
			return d.DialContext(ctx, "udp", dialTarget)
		},
	}

	ips, err := r.LookupHost(ctx, "google.com")
	if err != nil || len(ips) == 0 {
		errStr := "no addresses found"
		if err != nil {
			errStr = err.Error()
		}
		return false, 0, errStr
	}

	return true, time.Since(start).Milliseconds(), ""
}

func cliCheckDNS() {
	uciStorage := config.NewUCIStorage()
	cfg, err := uciStorage.Load()

	dnsInbound := "127.0.0.42:53"
	upstreamServer := "8.8.8.8:53"
	protocol := "udp"
	isDoH := false

	if err == nil {
		protocol = cfg.DNSProtocol
		if cfg.DNSPort > 0 {
			dnsInbound = fmt.Sprintf("127.0.0.42:%d", cfg.DNSPort)
		}

		if protocol == "doh" || strings.HasPrefix(cfg.DNSServer, "https://") {
			isDoH = true
			if cfg.DNSServer != "" {
				upstreamServer = cfg.DNSServer
			} else {
				upstreamServer = "https://1.1.1.1/dns-query"
			}
		} else {
			server := cfg.DNSServer
			if server == "" {
				server = "8.8.8.8"
			}
			if !strings.Contains(server, ":") {
				server += ":53"
			}
			upstreamServer = server
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	localOk, localRTT, localErr := testDNSQuery(ctx, dnsInbound, false)
	upstreamOk, upstreamRTT, upstreamErr := testDNSQuery(ctx, upstreamServer, isDoH)

	res := map[string]interface{}{
		"local_inbound": map[string]interface{}{
			"target":  dnsInbound,
			"success": localOk,
			"rtt_ms":  localRTT,
			"error":   localErr,
		},
		"upstream_dns": map[string]interface{}{
			"target":   upstreamServer,
			"protocol": protocol,
			"success":  upstreamOk,
			"rtt_ms":   upstreamRTT,
			"error":    upstreamErr,
		},
	}

	data, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(data))
}

func cliShowEngineConfig() {
	targetPaths := []string{
		RuntimeConfigPathSingBox,
		"/var/etc/cheburnet/sing-box.json",
	}

	var raw []byte
	var resolvedPath string
	var err error

	for _, p := range targetPaths {
		if _, statErr := os.Stat(p); statErr == nil {
			raw, err = os.ReadFile(p)
			if err == nil && len(raw) > 0 {
				resolvedPath = p
				break
			}
		}
	}

	if len(raw) == 0 {
		fmt.Println("No generated runtime config found in /tmp/run/cheburnet/ or /var/etc/cheburnet/")
		return
	}

	var prettyJSON bytes.Buffer
	if err := json.Indent(&prettyJSON, raw, "", "  "); err == nil {
		fmt.Printf("--- Active sing-box config (%s) ---\n%s\n", resolvedPath, prettyJSON.String())
	} else {
		fmt.Printf("--- Active sing-box config (%s) ---\n%s\n", resolvedPath, string(raw))
	}
}

func cliGetSystemInfo() {
	model, _ := os.ReadFile("/tmp/sysinfo/model")
	osRel, _ := os.ReadFile("/etc/os-release")

	res := map[string]string{
		"model":       strings.TrimSpace(string(model)),
		"version":     CheburVersion,
		"openwrt_rel": strings.TrimSpace(string(osRel)),
	}
	data, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(data))
}

func cliGlobalCheck() {
	fmt.Println("=== 1. System Info ===")
	cliGetSystemInfo()
	fmt.Println("\n=== 2. Engine Process Status ===")
	cliCheckEngine()
	fmt.Println("\n=== 3. DNS Availability ===")
	cliCheckDNS()
	fmt.Println("\n=== 4. NFT Rules Status ===")
	cliCheckNFT()
	fmt.Println("\n=== 5. Proxy Outbound Test ===")
	cliCheckProxy()
}
