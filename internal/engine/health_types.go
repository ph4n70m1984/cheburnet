package engine

import (
	"sync"
	"time"
)

type EngineHealth struct {
	Running       bool      `json:"running"`
	PID           int       `json:"pid"`
	PortListening bool      `json:"port_listening"`
	ConfigValid   bool      `json:"config_valid"`
	LastStart     time.Time `json:"last_start"`
	RestartCount  uint32    `json:"restart_count"`
	CrashCount    uint32    `json:"crash_count"`
}

type DNSHealth struct {
	ListenerAlive   bool  `json:"listener_alive"`
	ProxyDNSWorking bool  `json:"proxy_dns_working"`
	BootstrapAlive  bool  `json:"bootstrap_alive"`
	LatencyMs       int64 `json:"latency_ms"`
}

type NetworkHealth struct {
	E2EProxyWorking bool  `json:"e2e_proxy_working"`
	InternetDirect  bool  `json:"internet_direct"`
	LatencyMs       int64 `json:"latency_ms"`
	AvailableNodes  int   `json:"available_nodes"`
	TotalNodes      int   `json:"total_nodes"`
}

type HealthSnapshot struct {
	Timestamp time.Time     `json:"timestamp"`
	Engine    EngineHealth  `json:"engine"`
	DNS       DNSHealth     `json:"dns"`
	Network   NetworkHealth `json:"network"`
}

// HealthTracker хранит последнее известное состояние проверок L1/L2
type HealthTracker struct {
	mu       sync.RWMutex
	snapshot HealthSnapshot
}

func NewHealthTracker() *HealthTracker {
	return &HealthTracker{
		snapshot: HealthSnapshot{
			Timestamp: time.Now(),
			Engine: EngineHealth{
				ConfigValid: true,
			},
			DNS: DNSHealth{
				BootstrapAlive: true,
			},
			Network: NetworkHealth{
				InternetDirect: true,
			},
		},
	}
}

func (ht *HealthTracker) Snapshot() HealthSnapshot {
	ht.mu.RLock()
	defer ht.mu.RUnlock()
	return ht.snapshot
}

func (ht *HealthTracker) UpdateEngine(running bool, pid int, portOk, configOk bool, restartDelta bool, crashDelta bool) {
	ht.mu.Lock()
	defer ht.mu.Unlock()

	ht.snapshot.Timestamp = time.Now()
	ht.snapshot.Engine.Running = running
	ht.snapshot.Engine.PID = pid
	ht.snapshot.Engine.PortListening = portOk
	ht.snapshot.Engine.ConfigValid = configOk

	if restartDelta {
		ht.snapshot.Engine.RestartCount++
	}
	if crashDelta {
		ht.snapshot.Engine.CrashCount++
	} else if running {
		ht.snapshot.Engine.CrashCount = 0
	}
}

func (ht *HealthTracker) UpdateDNS(listenerAlive, proxyDNS, bootstrap bool, latency int64) {
	ht.mu.Lock()
	defer ht.mu.Unlock()

	ht.snapshot.Timestamp = time.Now()
	ht.snapshot.DNS.ListenerAlive = listenerAlive
	ht.snapshot.DNS.ProxyDNSWorking = proxyDNS
	ht.snapshot.DNS.BootstrapAlive = bootstrap
	ht.snapshot.DNS.LatencyMs = latency
}

func (ht *HealthTracker) UpdateNetwork(e2eOk, directOk bool, latency int64, availableNodes, totalNodes int) {
	ht.mu.Lock()
	defer ht.mu.Unlock()

	ht.snapshot.Timestamp = time.Now()
	ht.snapshot.Network.E2EProxyWorking = e2eOk
	ht.snapshot.Network.InternetDirect = directOk
	ht.snapshot.Network.LatencyMs = latency
	ht.snapshot.Network.AvailableNodes = availableNodes
	ht.snapshot.Network.TotalNodes = totalNodes
}
