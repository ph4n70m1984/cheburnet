package main

import (
	"bufio"
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
	"strings"
	"sync"
	"syscall"
	"time"

	"cheburnet/internal/api"
	"cheburnet/internal/config"
	"cheburnet/internal/diagnostics"
	"cheburnet/internal/engine"
	"cheburnet/internal/network"
	"cheburnet/internal/subscription"
	"cheburnet/internal/telemetry"
	"cheburnet/internal/updater"
	"cheburnet/pkg/uri"
)

var (
	CheburVersion            = "1.0.0-dual"
	RuntimeConfigPathSingBox = "/tmp/run/cheburnet/sing-box.json"
	RuntimeConfigPathXray    = "/tmp/run/cheburnet/xray.json"
	DefaultAPIBind           = "0.0.0.0:8088"
	PIDFile                  = "/var/run/cheburnetd.pid"
)

type App struct {
	state         *config.StateManager
	singboxEng    *engine.SingBoxEngine
	xrayEng       *engine.XrayEngine
	activeEng     engine.Engine
	hub           *telemetry.Hub
	server        *api.Server
	rulesLoader   *network.CompressedRulesetLoader
	rulesCron     *network.RulesetCron
	healthTracker *engine.HealthTracker
	diagEngine    *diagnostics.DiagnosticsEngine
	mu            sync.RWMutex
	engineOpMu    sync.Mutex
}

func showHelp() {
	fmt.Printf("Usage: cheburnetd [COMMAND]\n\n" +
		"Service Management:\n" +
		"    start                   Start cheburnet daemon service (foreground)\n" +
		"    stop                    Stop cheburnet background daemon\n" +
		"    restart                 Restart daemon service\n" +
		"    reload                  Reload configuration without dropping routing\n" +
		"    list_update             Update subscriptions and rulesets\n" +
		"    check_updates           Check component and daemon updates\n" +
		"    upgrade [target]        Run upgrade (target: all | cheburnet | cores | sing-box | xray)\n\n" +
		"Diagnostics & Network:\n" +
		"    check_proxy             Check proxy connectivity through mixed port\n" +
		"    check_nft               Check NFT rules presence\n" +
		"    check_nft_rules         Check NFT mangle/proxy rule counters\n" +
		"    check_engine            Check proxy engine (sing-box/xray) process status\n" +
		"    check_dns_available     Check local and upstream DNS availability\n" +
		"    check_logs              Show journal logs filtered by cheburnet\n" +
		"    global_check            Run end-to-end system diagnostic\n\n" +
		"Inspection & Management:\n" +
		"    show_config             Display parsed UCI configuration\n" +
		"    show_engine_config      Show generated JSON config of the active engine\n" +
		"    show_version            Show Chebur.NET daemon version\n" +
		"    get_status              Get daemon status (JSON)\n" +
		"    get_system_info         Get device and OS specs (JSON)\n" +
		"    switch_engine [name]    Switch active engine (sing-box | xray)\n")
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
		stopDaemon()
		time.Sleep(1 * time.Second)
		runDaemon()

	case "reload":
		callAPI(http.MethodPost, "/api/v1/reload", nil)

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

	case "switch_engine":
		if len(os.Args) < 3 {
			fmt.Println("Error: engine name required (sing-box or xray)")
			os.Exit(1)
		}
		body := fmt.Sprintf(`{"engine":"%s"}`, os.Args[2])
		callAPI(http.MethodPost, "/api/v1/engine/switch", strings.NewReader(body))

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
	for _, rs := range cfg.RuleSets {
		norm := strings.ToLower(strings.TrimSpace(rs))
		if norm != "" {
			unique[norm] = struct{}{}
		}
	}
	for _, rp := range cfg.RoutePolicies {
		if rp.Enabled {
			for _, rs := range rp.RuleSets {
				norm := strings.ToLower(strings.TrimSpace(rs))
				if norm != "" {
					unique[norm] = struct{}{}
				}
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
	for _, p := range policies {
		if !p.Enabled || p.Mode != config.ClientModeFullProxy || p.Target == "" {
			continue
		}
		target := strings.TrimSpace(p.Target)

		if strings.Contains(target, ":") && !strings.Contains(target, ".") {
			file, err := os.Open("/tmp/dhcp.leases")
			if err == nil {
				scanner := bufio.NewScanner(file)
				for scanner.Scan() {
					fields := strings.Fields(scanner.Text())
					if len(fields) >= 3 && strings.EqualFold(fields[1], target) {
						target = fields[2]
						break
					}
				}
				file.Close()
			}
		}

		cleanIP := strings.Split(target, "/")[0]
		if net.ParseIP(cleanIP) != nil {
			ips = append(ips, cleanIP)
		}
	}
	return ips
}

func runDaemon() {
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

	_ = os.WriteFile(PIDFile, []byte(fmt.Sprintf("%d", os.Getpid())), 0644)
	defer os.Remove(PIDFile)

	uciStorage := config.NewUCIStorage()
	initialConfig, err := uciStorage.Load()
	if err != nil {
		log.Fatalf("[FATAL] Load config failed: %v", err)
	}

	if initialConfig.RulesetUpdateInterval == "" {
		initialConfig.RulesetUpdateInterval = "72h"
	}

	subWorker := subscription.NewWorker(initialConfig.AutoHWID, initialConfig.CustomHWID)

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

	state := config.NewStateManager(initialConfig)
	sbEngine := engine.NewSingBoxEngine()
	xrEngine := engine.NewXrayEngine()
	hub := telemetry.NewHub()
	healthTracker := engine.NewHealthTracker()
	diagEngine := diagnostics.NewEngine(initialConfig.TProxyPort)
	hub.SetDiagnosticsEngine(diagEngine)

	updManager := updater.NewManager("ph4n70m1984/cheburnet", CheburVersion)

	app := &App{
		state:         state,
		singboxEng:    sbEngine,
		xrayEng:       xrEngine,
		hub:           hub,
		rulesLoader:   rulesLoader,
		healthTracker: healthTracker,
		diagEngine:    diagEngine,
	}

	if initialConfig.Engine == "xray" {
		app.activeEng = xrEngine
	} else {
		app.activeEng = sbEngine
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
		log.Printf("[WARN] Initial proxy engine failed to start: %v", err)
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

	go hub.Run(daemonCtx, app.getCurrentEngine)

	srv := api.NewServer(
		state,
		hub,
		subWorker,
		updManager,
		app.getCurrentEngine,
		func(name string) error {
			_ = uciStorage.SaveEngine(name)
			return app.switchEngine(daemonCtx, name)
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
			case "switch_node":
				if err := app.restartActiveEngine(daemonCtx); err != nil {
					return err
				}
				if app.healthTracker != nil {
					cfg := app.state.Get()
					app.healthTracker.UpdateNetwork(true, true, 50, len(cfg.Nodes), len(cfg.Nodes))
					if app.diagEngine != nil {
						app.diagEngine.ProcessSnapshot(app.healthTracker.Snapshot())
					}
				}
				return nil
			default:
				return nil
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

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

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

func (a *App) getEngineByName(name string) (engine.Engine, string, error) {
	switch name {
	case "xray":
		return a.xrayEng, RuntimeConfigPathXray, nil
	case "sing-box":
		return a.singboxEng, RuntimeConfigPathSingBox, nil
	default:
		return nil, "", fmt.Errorf("unknown engine %s", name)
	}
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

	targetPath := RuntimeConfigPathSingBox
	if eng.Name() == "xray" {
		targetPath = RuntimeConfigPathXray
	}

	allRuleSets := collectAllRuleSets(&cfg)

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
	if eng.Name() == "xray" {
		targetPath = RuntimeConfigPathXray
	}

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
	if eng.Name() == "xray" {
		targetPath = RuntimeConfigPathXray
	}

	if err := eng.BuildConfig(&cfg, targetPath); err != nil {
		return fmt.Errorf("build %s config: %w", eng.Name(), err)
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

func (a *App) switchEngine(ctx context.Context, name string) error {
	newEng, newTargetPath, err := a.getEngineByName(name)
	if err != nil {
		return err
	}

	a.engineOpMu.Lock()
	defer a.engineOpMu.Unlock()

	a.mu.RLock()
	oldEng := a.activeEng
	cfg := a.state.Get()
	a.mu.RUnlock()

	if oldEng != nil && oldEng.Name() == newEng.Name() {
		return nil
	}

	if err := newEng.BuildConfig(&cfg, newTargetPath); err != nil {
		return fmt.Errorf("pre-flight build config failed for %s: %w (active engine kept running)", name, err)
	}

	oldTargetPath := ""
	if oldEng != nil {
		if oldEng.Name() == "xray" {
			oldTargetPath = RuntimeConfigPathXray
		} else {
			oldTargetPath = RuntimeConfigPathSingBox
		}
		_ = oldEng.Stop()
	}

	if err := newEng.Start(ctx, newTargetPath); err != nil {
		log.Printf("[ERROR] Failed to start new engine %s: %v. Initiating rollback...", name, err)

		if oldEng != nil && oldTargetPath != "" {
			if rbErr := oldEng.Start(ctx, oldTargetPath); rbErr != nil {
				log.Printf("[CRITICAL] Rollback failed! Both engines down: %v", rbErr)
				return fmt.Errorf("switch failed: %w; rollback failed: %v", err, rbErr)
			}
			log.Printf("[INFO] Rollback successful: restored previous engine %s", oldEng.Name())
		}

		return fmt.Errorf("failed to start %s, rolled back: %w", name, err)
	}

	a.mu.Lock()
	a.activeEng = newEng
	a.mu.Unlock()

	log.Printf("[INFO] Successfully switched proxy engine to %s", name)
	return nil
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
			if currentEng.Name() == "xray" {
				targetPath = RuntimeConfigPathXray
			}

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
	req, err := http.NewRequest(method, "http://"+DefaultAPIBind+endpoint, body)
	if err != nil {
		fmt.Printf("Error creating request: %v\n", err)
		return
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
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
	xrRunning := exec.Command("pgrep", "-f", "xray").Run() == nil

	res := map[string]interface{}{
		"sing_box_running": sbRunning,
		"xray_running":     xrRunning,
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
	if _, err := os.Stat(RuntimeConfigPathSingBox); err == nil {
		data, _ := os.ReadFile(RuntimeConfigPathSingBox)
		fmt.Printf("--- Active sing-box config (%s) ---\n%s\n", RuntimeConfigPathSingBox, string(data))
		return
	}
	if _, err := os.Stat(RuntimeConfigPathXray); err == nil {
		data, _ := os.ReadFile(RuntimeConfigPathXray)
		fmt.Printf("--- Active Xray config (%s) ---\n%s\n", RuntimeConfigPathXray, string(data))
		return
	}
	fmt.Println("No generated runtime config found in /tmp/run/cheburnet/")
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
