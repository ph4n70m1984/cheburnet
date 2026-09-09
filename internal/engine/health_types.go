package engine

import (
	"sync"
	"time"
)

type EngineHealth struct {
	Running       bool
	CrashCount    int
	PortListening bool
	ConfigValid   bool
	LastRestart   time.Time
}

type DNSHealth struct {
	ListenerAlive   bool
	ProxyDNSWorking bool
	BootstrapAlive  bool
	LatencyMs       int64
}

type NetworkHealth struct {
	InternetDirect  bool
	E2EProxyWorking bool
	LatencyMs       int64
	AvailableNodes  int
	TotalNodes      int
}

type HealthSnapshot struct {
	Initialized bool
	Timestamp   time.Time
	Engine      EngineHealth
	DNS         DNSHealth
	Network     NetworkHealth
}

type HealthTracker struct {
	mu          sync.RWMutex
	initialized bool
	engine      EngineHealth
	dns         DNSHealth
	network     NetworkHealth
}

func NewHealthTracker() *HealthTracker {
	return &HealthTracker{
		initialized: false,
		engine: EngineHealth{
			ConfigValid: true,
		},
		dns: DNSHealth{
			BootstrapAlive: true,
		},
		network: NetworkHealth{
			InternetDirect: true,
		},
	}
}

func (h *HealthTracker) UpdateEngine(running bool, crashes int, portListening bool, cfgValid bool, restartNow bool, isDead bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.initialized = true
	h.engine.Running = running
	h.engine.CrashCount = crashes
	h.engine.PortListening = portListening
	h.engine.ConfigValid = cfgValid
	if restartNow {
		h.engine.LastRestart = time.Now()
	}
}

func (h *HealthTracker) UpdateDNS(listenerAlive, proxyDNSWorking, bootstrapAlive bool, latencyMs int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.initialized = true
	h.dns.ListenerAlive = listenerAlive
	h.dns.ProxyDNSWorking = proxyDNSWorking
	h.dns.BootstrapAlive = bootstrapAlive
	h.dns.LatencyMs = latencyMs
}

func (h *HealthTracker) UpdateNetwork(e2eProxyWorking, internetDirect bool, latencyMs int64, availableNodes, totalNodes int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.initialized = true
	h.network.E2EProxyWorking = e2eProxyWorking
	h.network.InternetDirect = internetDirect
	h.network.LatencyMs = latencyMs
	h.network.AvailableNodes = availableNodes
	h.network.TotalNodes = totalNodes
}

func (h *HealthTracker) Snapshot() HealthSnapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return HealthSnapshot{
		Initialized: h.initialized,
		Timestamp:   time.Now(),
		Engine:      h.engine,
		DNS:         h.dns,
		Network:     h.network,
	}
}
