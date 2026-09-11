package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"

	"cheburnet/internal/config"
)

const ProbePortBase = 12000

type XrayStatItem struct {
	Name  string `json:"name"`
	Value int64  `json:"value"`
}

type XrayStatsResponse struct {
	Stat []XrayStatItem `json:"stat"`
}

type ClashShimServer struct {
	mu           sync.RWMutex
	server       *http.Server
	cfg          *config.CheburConfig
	selectedNode string
	xrayAPIAddr  string
	latencies    map[string]int
	lastCheck    map[string]time.Time
	cachedStats  map[string]int64
	onSelect     func(nodeTag string) error
	ctx          context.Context
	cancel       context.CancelFunc
}

func NewClashShimServer(cfg *config.CheburConfig, onSelect func(nodeTag string) error) *ClashShimServer {
	current := ""
	if len(cfg.Nodes) > 0 {
		current = cfg.Nodes[0].Tag
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &ClashShimServer{
		cfg:          cfg,
		selectedNode: current,
		xrayAPIAddr:  "127.0.0.1:10085",
		latencies:    make(map[string]int),
		lastCheck:    make(map[string]time.Time),
		cachedStats:  make(map[string]int64),
		onSelect:     onSelect,
		ctx:          ctx,
		cancel:       cancel,
	}
}

func (s *ClashShimServer) UpdateConfig(cfg *config.CheburConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
	if len(cfg.Nodes) > 0 {
		found := false
		for _, n := range cfg.Nodes {
			if n.Tag == s.selectedNode {
				found = true
				break
			}
		}
		if !found {
			s.selectedNode = cfg.Nodes[0].Tag
		}
	}
}

func (s *ClashShimServer) Start(port int) error {
	if port <= 0 {
		port = 9090
	}

	addr := fmt.Sprintf("0.0.0.0:%d", port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("clash shim listen failed on %s: %w", addr, err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/proxies", s.handleProxies)
	mux.HandleFunc("/proxies/", s.handleProxyRoute)
	mux.HandleFunc("/version", s.handleVersion)

	corsWrapper := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, PUT, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		mux.ServeHTTP(w, r)
	})

	s.server = &http.Server{
		Handler:      corsWrapper,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}

	log.Printf("[clash-shim] Started Xray-backed Clash API bridge on %s", addr)

	go func() {
		if err := s.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("[clash-shim] Server error: %v", err)
		}
	}()

	go s.backgroundPingLoop()
	go s.backgroundStatsLoop()

	return nil
}

func (s *ClashShimServer) Stop() error {
	s.cancel()
	if s.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return s.server.Shutdown(ctx)
	}
	return nil
}

func (s *ClashShimServer) backgroundStatsLoop() {
	ticker := time.NewTicker(6 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(s.ctx, 2*time.Second)
			cmd := exec.CommandContext(ctx, "xray", "api", "statsquery", "-s", s.xrayAPIAddr, "-pattern", "outbound>>>")
			var stdout bytes.Buffer
			cmd.Stdout = &stdout

			if err := cmd.Run(); err == nil {
				var resp XrayStatsResponse
				if err := json.Unmarshal(stdout.Bytes(), &resp); err == nil {
					newStats := make(map[string]int64, len(resp.Stat))
					for _, item := range resp.Stat {
						newStats[item.Name] = item.Value
					}
					s.mu.Lock()
					s.cachedStats = newStats
					s.mu.Unlock()
				}
			}
			cancel()
		}
	}
}

func (s *ClashShimServer) backgroundPingLoop() {
	time.Sleep(2 * time.Second)
	s.pingAll()

	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.pingAll()
		}
	}
}

func (s *ClashShimServer) pingAll() {
	s.mu.RLock()
	nodesCount := len(s.cfg.Nodes)
	s.mu.RUnlock()

	if nodesCount == 0 {
		return
	}

	semaphore := make(chan struct{}, 3)
	var wg sync.WaitGroup

	for idx := 0; idx < nodesCount; idx++ {
		s.mu.RLock()
		if idx >= len(s.cfg.Nodes) {
			s.mu.RUnlock()
			break
		}
		node := s.cfg.Nodes[idx]
		s.mu.RUnlock()

		wg.Add(1)
		semaphore <- struct{}{}

		go func(nodeIndex int, tag string) {
			defer wg.Done()
			defer func() { <-semaphore }()

			delay := s.probeNodePort(nodeIndex)

			s.mu.Lock()
			s.latencies[tag] = delay
			s.lastCheck[tag] = time.Now()
			s.mu.Unlock()
		}(idx, node.Tag)
	}

	wg.Wait()
}

func (s *ClashShimServer) probeNodePort(nodeIndex int) int {
	port := ProbePortBase + nodeIndex
	proxyURL, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	if err != nil {
		return 0
	}

	transport := &http.Transport{
		Proxy:             http.ProxyURL(proxyURL),
		DisableKeepAlives: true,
		DialContext: (&net.Dialer{
			Timeout: 1800 * time.Millisecond,
		}).DialContext,
		ResponseHeaderTimeout: 2000 * time.Millisecond,
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   2500 * time.Millisecond,
	}

	start := time.Now()
	req, err := http.NewRequestWithContext(s.ctx, http.MethodGet, "https://www.gstatic.com/generate_204", nil)
	if err != nil {
		return 0
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return 0
	}

	delayMs := int(time.Since(start).Milliseconds())
	if delayMs <= 0 {
		delayMs = 1
	}
	return delayMs
}

func (s *ClashShimServer) handleVersion(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"version": "v1.18.0-xray-bridge",
		"premium": true,
	})
}

func (s *ClashShimServer) handleProxies(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	proxies := make(map[string]interface{})
	proxies["DIRECT"] = map[string]interface{}{"name": "DIRECT", "type": "Direct", "history": []interface{}{}}
	proxies["REJECT"] = map[string]interface{}{"name": "REJECT", "type": "Reject", "history": []interface{}{}}

	var allNodeTags []string
	nowStr := time.Now().Format(time.RFC3339)

	for _, n := range s.cfg.Nodes {
		allNodeTags = append(allNodeTags, n.Tag)

		upKey := fmt.Sprintf("outbound>>>%s>>>traffic>>>uplink", n.Tag)
		downKey := fmt.Sprintf("outbound>>>%s>>>traffic>>>downlink", n.Tag)

		var upBytes, downBytes int64
		if s.cachedStats != nil {
			upBytes = s.cachedStats[upKey]
			downBytes = s.cachedStats[downKey]
		}

		delay, hasDelay := s.latencies[n.Tag]
		checkTime := nowStr
		if t, ok := s.lastCheck[n.Tag]; ok {
			checkTime = t.Format(time.RFC3339)
		}

		var history []map[string]interface{}
		if hasDelay {
			history = []map[string]interface{}{
				{
					"time":  checkTime,
					"delay": delay,
				},
			}
		} else {
			history = []map[string]interface{}{}
		}

		proxies[n.Tag] = map[string]interface{}{
			"name":    n.Tag,
			"type":    strings.ToUpper(n.Protocol),
			"udp":     true,
			"history": history,
			"extra": map[string]interface{}{
				"upload":   upBytes,
				"download": downBytes,
			},
		}
	}

	proxies["PROXY"] = map[string]interface{}{
		"name":    "PROXY",
		"type":    "Selector",
		"now":     s.selectedNode,
		"all":     allNodeTags,
		"history": []interface{}{},
	}

	proxies["auto"] = map[string]interface{}{
		"name":    "auto",
		"type":    "URLTest",
		"now":     s.selectedNode,
		"all":     allNodeTags,
		"history": []interface{}{},
	}

	globalAll := append([]string{"PROXY", "DIRECT"}, allNodeTags...)
	proxies["GLOBAL"] = map[string]interface{}{
		"name":    "GLOBAL",
		"type":    "Selector",
		"now":     "PROXY",
		"all":     globalAll,
		"history": []interface{}{},
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"proxies": proxies,
	})
}

func (s *ClashShimServer) handleProxyRoute(w http.ResponseWriter, r *http.Request) {
	subPath := strings.TrimPrefix(r.URL.Path, "/proxies/")

	if strings.HasSuffix(subPath, "/delay") {
		targetName := strings.TrimSuffix(subPath, "/delay")
		if targetName == "auto" || targetName == "PROXY" || targetName == "GLOBAL" {
			s.mu.RLock()
			d := s.latencies[s.selectedNode]
			s.mu.RUnlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"delay": d})
			return
		}
		s.measureNodeDelay(w, r, targetName)
		return
	}

	groupName := subPath
	switch r.Method {
	case http.MethodGet:
		s.mu.RLock()
		defer s.mu.RUnlock()

		if groupName == "PROXY" || groupName == "auto" || groupName == "GLOBAL" {
			var allNodeTags []string
			if groupName == "GLOBAL" {
				allNodeTags = append([]string{"PROXY", "DIRECT"}, allNodeTags...)
			}
			for _, n := range s.cfg.Nodes {
				allNodeTags = append(allNodeTags, n.Tag)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"name": groupName,
				"type": "Selector",
				"now":  s.selectedNode,
				"all":  allNodeTags,
			})
			return
		}

		for _, n := range s.cfg.Nodes {
			if n.Tag == groupName {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"name": n.Tag,
					"type": strings.ToUpper(n.Protocol),
				})
				return
			}
		}

		http.NotFound(w, r)

	case http.MethodPut:
		if groupName != "PROXY" && groupName != "auto" {
			http.Error(w, "group not found or not selectable", http.StatusNotFound)
			return
		}

		var payload struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}

		s.mu.Lock()
		s.selectedNode = payload.Name
		s.mu.Unlock()

		if s.onSelect != nil {
			if err := s.onSelect(payload.Name); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}

		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *ClashShimServer) measureNodeDelay(w http.ResponseWriter, r *http.Request, nodeTag string) {
	s.mu.RLock()
	nodeIndex := -1
	for idx, n := range s.cfg.Nodes {
		if n.Tag == nodeTag {
			nodeIndex = idx
			break
		}
	}
	cachedDelay, hasDelay := s.latencies[nodeTag]
	lastT := s.lastCheck[nodeTag]
	s.mu.RUnlock()

	if nodeIndex == -1 {
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}

	if hasDelay && cachedDelay > 0 && time.Since(lastT) < 10*time.Second {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"delay": cachedDelay,
		})
		return
	}

	delay := s.probeNodePort(nodeIndex)

	s.mu.Lock()
	s.latencies[nodeTag] = delay
	s.lastCheck[nodeTag] = time.Now()
	s.mu.Unlock()

	if delay == 0 {
		http.Error(w, `{"message":"timeout"}`, http.StatusGatewayTimeout)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"delay": delay,
	})
}
