package main

import (
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
	"cheburnet/internal/engine"
	"cheburnet/internal/network"
	"cheburnet/internal/subscription"
	"cheburnet/internal/telemetry"
	"cheburnet/internal/updater"
	"cheburnet/pkg/uri"
)

const (
	CheburVersion            = "1.0.0-dual"
	RuntimeConfigPathSingBox = "/tmp/run/cheburnet/sing-box.json"
	RuntimeConfigPathXray    = "/tmp/run/cheburnet/xray.json"
	DefaultAPIBind           = "0.0.0.0:8088"
	PIDFile                  = "/var/run/cheburnetd.pid"
)

type App struct {
	state       *config.StateManager
	singboxEng  *engine.SingBoxEngine
	xrayEng     *engine.XrayEngine
	activeEng   engine.Engine
	hub         *telemetry.Hub
	server      *api.Server
	rulesLoader *network.CompressedRulesetLoader
	rulesCron   *network.RulesetCron
	mu          sync.Mutex
}

func showHelp() {
	fmt.Printf("Usage: cheburnetd [COMMAND]\n\n" +
		"Service Management:\n" +
		"    start                   Start cheburnet daemon service (foreground)\n" +
		"    stop                    Stop cheburnet background daemon\n" +
		"    restart                 Restart daemon service\n" +
		"    reload                  Reload configuration without dropping routing\n" +
		"    list_update             Update subscriptions and rulesets\n\n" +
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

	default: // "subscription"
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

	allSubnets := initialConfig.CustomSubnets
	if len(initialConfig.RuleSets) > 0 {
		log.Printf("[INFO] Loading cached subnets for rulesets: %v", initialConfig.RuleSets)
		fetched := loadSubnetsFromCompressedStorage(rulesLoader, initialConfig.RuleSets)
		allSubnets = append(allSubnets, fetched...)
		log.Printf("[INFO] Total subnets loaded for direct routing: %d", len(allSubnets))
	}

	state := config.NewStateManager(initialConfig)
	sbEngine := engine.NewSingBoxEngine()
	xrEngine := engine.NewXrayEngine()
	hub := telemetry.NewHub()

	updManager := updater.NewManager("cheburnet/cheburnet", CheburVersion)

	app := &App{
		state:       state,
		singboxEng:  sbEngine,
		xrayEng:     xrEngine,
		hub:         hub,
		rulesLoader: rulesLoader,
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

	log.Println("[INFO] Setting up nftables and routing...")
	if err := network.ApplyNFTRules([]string{sourceIface}, allSubnets, initialConfig.TProxyPort); err != nil {
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
		initialConfig.RuleSets,
		initialConfig.RulesetUpdateInterval,
		func() error {
			return app.reloadActiveEngine(daemonCtx)
		},
	)
	app.rulesCron.Start(daemonCtx)

	go hub.Run(daemonCtx, app.getCurrentEngine())

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
	)
	app.server = srv

	go func() {
		if err := srv.Listen(DefaultAPIBind); err != nil {
			log.Printf("[INFO] API Server stopped: %v", err)
		}
	}()

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
	a.mu.Lock()
	defer a.mu.Unlock()
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
	a.mu.Lock()
	defer a.mu.Unlock()

	cfg := a.state.Get()
	targetPath := RuntimeConfigPathSingBox
	if a.activeEng.Name() == "xray" {
		targetPath = RuntimeConfigPathXray
	}

	return engine.SafeReload(ctx, a.activeEng, &cfg, targetPath)
}

func (a *App) startActiveEngine(ctx context.Context) error {
	cfg := a.state.Get()
	targetPath := RuntimeConfigPathSingBox
	if a.activeEng.Name() == "xray" {
		targetPath = RuntimeConfigPathXray
	}

	if err := a.activeEng.BuildConfig(&cfg, targetPath); err != nil {
		return fmt.Errorf("build %s config: %w", a.activeEng.Name(), err)
	}

	return a.activeEng.Start(ctx, targetPath)
}

func (a *App) stopActiveEngine() {
	if a.activeEng != nil {
		_ = a.activeEng.Stop()
	}
}

func (a *App) switchEngine(ctx context.Context, name string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	// 1. Валидация целевого движка до любых действий
	newEng, newTargetPath, err := a.getEngineByName(name)
	if err != nil {
		return err
	}

	// Если запрошен уже запущенный движок — пропускаем
	if a.activeEng != nil && a.activeEng.Name() == newEng.Name() {
		return nil
	}

	oldEng := a.activeEng
	cfg := a.state.Get()

	// 2. Pre-flight check: собираем и проверяем конфиг ДО остановки текущего сервиса
	if err := newEng.BuildConfig(&cfg, newTargetPath); err != nil {
		return fmt.Errorf("pre-flight build config failed for %s: %w (active engine kept running)", name, err)
	}

	// 3. Конфиг готов: останавливаем старый процесс
	oldTargetPath := ""
	if oldEng != nil {
		if oldEng.Name() == "xray" {
			oldTargetPath = RuntimeConfigPathXray
		} else {
			oldTargetPath = RuntimeConfigPathSingBox
		}
		_ = oldEng.Stop()
	}

	// 4. Запускаем новый движок
	if err := newEng.Start(ctx, newTargetPath); err != nil {
		log.Printf("[ERROR] Failed to start new engine %s: %v. Initiating rollback...", name, err)

		// 5. ROLLBACK: возвращаем старое ядро
		if oldEng != nil && oldTargetPath != "" {
			if rbErr := oldEng.Start(ctx, oldTargetPath); rbErr != nil {
				log.Printf("[CRITICAL] Rollback failed! Both engines down: %v", rbErr)
				return fmt.Errorf("switch failed: %w; rollback failed: %v", err, rbErr)
			}
			log.Printf("[INFO] Rollback successful: restored previous engine %s", oldEng.Name())
		}

		return fmt.Errorf("failed to start %s, rolled back: %w", name, err)
	}

	// 6. Фиксируем активный движок только после успешного старта
	a.activeEng = newEng
	log.Printf("[INFO] Successfully switched proxy engine to %s", name)

	return nil
}

func callAPI(method, endpoint string, body io.Reader) {
	client := &http.Client{Timeout: 5 * time.Second}
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

func cliCheckDNS() {
	uciStorage := config.NewUCIStorage()
	cfg, err := uciStorage.Load()

	upstreamServer := "8.8.8.8"
	dnsInbound := "127.0.0.42"

	if err == nil {
		if cfg.DNSPort > 0 {
			dnsInbound = fmt.Sprintf("127.0.0.42:%d", cfg.DNSPort)
		}

		// Выбираем IP для проверки в зависимости от протокола
		if cfg.DNSProtocol == "doh" || cfg.DNSProtocol == "dot" || strings.HasPrefix(cfg.DNSServer, "https://") {
			// Для зашифрованных протоколов nslookup на 53 порт апстрима бессмыслен,
			// поэтому проверяем доступность bootstrap DNS, через который резолвится сам DoH/DoT адрес
			if cfg.BootstrapDNS != "" {
				upstreamServer = cfg.BootstrapDNS
			} else {
				upstreamServer = "77.88.8.8"
			}
		} else if cfg.DNSServer != "" {
			upstreamServer = cfg.DNSServer
		}
	}

	// 1. Проверка локального входящего DNS прокси (dns-in)
	dnsInHost, dnsInPort, splitErr := net.SplitHostPort(dnsInbound)
	var outLocal []byte
	var errLocal error

	if splitErr == nil && dnsInPort != "53" {
		outLocal, errLocal = exec.Command("nslookup", "-port="+dnsInPort, "google.com", dnsInHost).CombinedOutput()
	} else {
		outLocal, errLocal = exec.Command("nslookup", "google.com", dnsInHost).CombinedOutput()
	}
	localOk := errLocal == nil && strings.Contains(string(outLocal), "Address")

	// 2. Проверка сконфигурированного вышестоящего DNS
	outUpstream, errUpstream := exec.Command("nslookup", "google.com", upstreamServer).CombinedOutput()
	upstreamOk := errUpstream == nil && strings.Contains(string(outUpstream), "Address")

	res := map[string]interface{}{
		"local_inbound": map[string]interface{}{
			"target":  dnsInbound,
			"success": localOk,
		},
		"upstream_dns": map[string]interface{}{
			"target":   upstreamServer,
			"protocol": cfg.DNSProtocol,
			"success":  upstreamOk,
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
