//go:build adaptive_probe

package adaptive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"cheburnet/internal/config"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sync/singleflight"
)

const (
	defaultCacheTTL   = 5 * time.Minute
	globalProbeTTL    = 8 * time.Second
	probeUserAgent    = "CheburNET-Probe/1.0"
	l4Timeout         = 450 * time.Millisecond
	l4Attempts        = 2
	l4MaxLossRate     = 0.34
	l4Concurrency     = 25
	l2Concurrency     = 25
	failedNodeCoolTTL = 5 * time.Minute
	speedChunkSize    = 256 * 1024
)

var (
	ErrClashAPINotConfigured = errors.New("clash api endpoint is not configured")

	probePhaseDuration     *prometheus.HistogramVec
	probePhaseDurationOnce sync.Once
)

func IsEnabled() bool {
	return true
}

func observePhase(phase string, dur time.Duration) {
	probePhaseDurationOnce.Do(func() {
		probePhaseDuration = prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "cheburnet_probe_phase_duration_seconds",
				Help:    "Execution time of adaptive probe phases in seconds.",
				Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1.0, 2.0, 4.0, 8.0, 12.0},
			},
			[]string{"phase"},
		)
		_ = prometheus.Register(probePhaseDuration)
	})

	if probePhaseDuration != nil {
		probePhaseDuration.WithLabelValues(phase).Observe(dur.Seconds())
	}
}

type NodeMetrics struct {
	Tag        string        `json:"tag"`
	Address    string        `json:"address"`
	Port       int           `json:"port"`
	RTT        time.Duration `json:"rtt_l4"`
	HTTPRTT    time.Duration `json:"rtt_http"`
	Jitter     time.Duration `json:"jitter"`
	LossRate   float64       `json:"loss_rate"`
	Throughput float64       `json:"throughput"`
	L1Score    float64       `json:"l1_score"`
	L3Score    float64       `json:"l3_score"`
	IsFallback bool          `json:"is_fallback"`
}

type Prober struct {
	clashAPI    string
	secret      string
	probeClient *http.Client
	apiClient   *http.Client
	cacheTTL    time.Duration
	lastResult  *NodeMetrics
	lastAt      time.Time
	latestPool  map[string]*NodeMetrics
	failedPool  map[string]time.Time
	fallbackIdx int
	mu          sync.RWMutex
	sf          singleflight.Group
}

func NewProber(clashAPI string, secret string) *Prober {
	probeTransport := &http.Transport{
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: 2 * time.Second,
		DialContext: (&net.Dialer{
			Timeout: 2 * time.Second,
		}).DialContext,
	}

	apiTransport := &http.Transport{
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: 1500 * time.Millisecond,
		DialContext: (&net.Dialer{
			Timeout: 1500 * time.Millisecond,
		}).DialContext,
	}

	return &Prober{
		clashAPI:   clashAPI,
		secret:     secret,
		cacheTTL:   defaultCacheTTL,
		latestPool: make(map[string]*NodeMetrics),
		failedPool: make(map[string]time.Time),
		probeClient: &http.Client{
			Timeout:   3 * time.Second,
			Transport: probeTransport,
		},
		apiClient: &http.Client{
			Timeout:   2 * time.Second,
			Transport: apiTransport,
		},
	}
}

func (p *Prober) SetSecret(secret string) {
	p.mu.Lock()
	p.secret = secret
	p.mu.Unlock()
}

func (p *Prober) GetNodeMetric(tag string) *NodeMetrics {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if m, ok := p.latestPool[tag]; ok {
		cp := *m
		return &cp
	}
	return nil
}

func (p *Prober) getActiveSelectorOutbounds(ctx context.Context) map[string]struct{} {
	p.mu.RLock()
	clashAPI := p.clashAPI
	sec := p.secret
	p.mu.RUnlock()

	if strings.TrimSpace(clashAPI) == "" {
		return nil
	}

	baseURL := strings.TrimRight(clashAPI, "/")
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "http://" + baseURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/proxies/"+config.MainSelectorTag, nil)
	if err != nil {
		return nil
	}
	if sec != "" {
		req.Header.Set("Authorization", "Bearer "+sec)
	}

	resp, err := p.apiClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return nil
	}
	defer resp.Body.Close()

	var res struct {
		All []string `json:"all"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil || len(res.All) == 0 {
		return nil
	}

	out := make(map[string]struct{}, len(res.All))
	for _, tag := range res.All {
		out[tag] = struct{}{}
	}
	return out
}

func (p *Prober) SelectBestNode(parentCtx context.Context, nodes []*config.GenericNode) *NodeMetrics {
	if len(nodes) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(parentCtx, globalProbeTTL)
	defer cancel()

	availableTags := p.getActiveSelectorOutbounds(ctx)
	validNodes := make([]*config.GenericNode, 0, len(nodes))
	for _, n := range nodes {
		if n == nil {
			continue
		}
		if availableTags != nil {
			if _, exists := availableTags[n.Tag]; !exists {
				continue
			}
		}
		validNodes = append(validNodes, n)
	}

	if len(validNodes) == 0 {
		validNodes = nodes
	}

	if len(validNodes) == 1 {
		return &NodeMetrics{
			Tag:        validNodes[0].Tag,
			Address:    validNodes[0].Address,
			Port:       validNodes[0].Port,
			IsFallback: false,
		}
	}

	if cached := p.getCachedNode(validNodes); cached != nil {
		return cached
	}

	v, _, _ := p.sf.Do("probe_best_node", func() (interface{}, error) {
		if cached := p.getCachedNode(validNodes); cached != nil {
			return cached, nil
		}

		startTotal := time.Now()
		res := p.doProbe(ctx, validNodes)
		observePhase("total", time.Since(startTotal))

		return res, nil
	})

	if res, ok := v.(*NodeMetrics); ok && res != nil {
		return res
	}
	return p.fallbackNode(validNodes)
}

func (p *Prober) doProbe(ctx context.Context, nodes []*config.GenericNode) *NodeMetrics {
	startL1 := time.Now()
	l4Candidates := p.filterLevel1L4(ctx, nodes)
	observePhase("l1", time.Since(startL1))

	if len(l4Candidates) == 0 {
		return p.fallbackNode(nodes)
	}

	startL2 := time.Now()
	topL2 := p.filterLevel2HTTP(ctx, l4Candidates)
	observePhase("l2", time.Since(startL2))

	if len(topL2) == 0 {
		log.Printf("[adaptive] WARN: all candidate nodes failed L2 HTTP delay test, falling back to next node")
		return p.fallbackNode(nodes)
	}

	if len(topL2) == 1 {
		p.updateCache(topL2[0])
		p.logSelection(topL2[0], "L2-single")
		return topL2[0]
	}

	top3 := topL2
	if len(top3) > 3 {
		top3 = top3[:3]
	}

	startL3 := time.Now()
	winner := p.benchmarkLevel3Speed(ctx, top3)
	observePhase("l3", time.Since(startL3))

	p.updateCache(winner)

	stage := "L3-winner"
	if winner.L3Score <= 0 {
		stage = "L2-winner"
	}
	p.logSelection(winner, stage)
	return winner
}

func (p *Prober) logSelection(m *NodeMetrics, stage string) {
	log.Printf("[adaptive] Selected '%s' via %s: L4=%dms, HTTP=%dms, Jitter=%dms, Loss=%.0f%%, Speed=%.1f KB/s, L1Score=%.1f, L3Score=%.1f (fallback=%t)",
		m.Tag, stage, m.RTT.Milliseconds(), m.HTTPRTT.Milliseconds(), m.Jitter.Milliseconds(),
		m.LossRate*100, m.Throughput/1024, m.L1Score, m.L3Score, m.IsFallback)
}

func (p *Prober) SwitchOutbound(ctx context.Context, selector, nodeTag string) error {
	p.mu.RLock()
	clashAPI := p.clashAPI
	secret := p.secret
	p.mu.RUnlock()

	if strings.TrimSpace(clashAPI) == "" {
		return ErrClashAPINotConfigured
	}

	baseURL := strings.TrimSpace(clashAPI)
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "http://" + baseURL
	}
	endpoint := fmt.Sprintf("%s/proxies/%s", strings.TrimRight(baseURL, "/"), url.PathEscape(selector))

	payload, err := json.Marshal(map[string]string{
		"name": nodeTag,
	})
	if err != nil {
		return fmt.Errorf("marshal switch payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create switch request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}

	resp, err := p.apiClient.Do(req)
	if err != nil {
		return fmt.Errorf("switch outbound failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("clash API returned status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func (p *Prober) getCachedNode(nodes []*config.GenericNode) *NodeMetrics {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.lastResult != nil && time.Since(p.lastAt) < p.cacheTTL {
		if failTime, isFailed := p.failedPool[p.lastResult.Tag]; isFailed && time.Since(failTime) < failedNodeCoolTTL {
			return nil
		}
		for _, n := range nodes {
			if n.Tag == p.lastResult.Tag && n.Address == p.lastResult.Address && n.Port == p.lastResult.Port {
				return p.lastResult
			}
		}
	}
	return nil
}

func (p *Prober) fallbackNode(nodes []*config.GenericNode) *NodeMetrics {
	p.mu.Lock()
	defer p.mu.Unlock()

	nLen := len(nodes)
	if nLen == 0 {
		return nil
	}

	now := time.Now()
	for i := 0; i < nLen; i++ {
		idx := (p.fallbackIdx + i) % nLen
		candidate := nodes[idx]

		if failTime, failed := p.failedPool[candidate.Tag]; failed && now.Sub(failTime) < failedNodeCoolTTL {
			continue
		}

		p.fallbackIdx = (idx + 1) % nLen
		res := &NodeMetrics{
			Tag:        candidate.Tag,
			Address:    candidate.Address,
			Port:       candidate.Port,
			IsFallback: true,
		}
		p.logSelection(res, "roundrobin-fallback")
		return res
	}

	p.fallbackIdx = (p.fallbackIdx + 1) % nLen
	fallbackNode := nodes[p.fallbackIdx%nLen]
	res := &NodeMetrics{
		Tag:        fallbackNode.Tag,
		Address:    fallbackNode.Address,
		Port:       fallbackNode.Port,
		IsFallback: true,
	}
	p.logSelection(res, "forced-fallback")
	return res
}

func (p *Prober) updateCache(m *NodeMetrics) {
	p.mu.Lock()
	p.lastResult = m
	p.lastAt = time.Now()
	delete(p.failedPool, m.Tag)
	p.mu.Unlock()
}

func (p *Prober) filterLevel1L4(ctx context.Context, nodes []*config.GenericNode) []*NodeMetrics {
	var wg sync.WaitGroup
	var mu sync.Mutex
	results := make([]*NodeMetrics, 0, len(nodes))
	sem := make(chan struct{}, l4Concurrency)

	now := time.Now()
	p.mu.RLock()
	failedCopy := make(map[string]time.Time, len(p.failedPool))
	for k, v := range p.failedPool {
		failedCopy[k] = v
	}
	p.mu.RUnlock()

	for _, n := range nodes {
		wg.Add(1)
		go func(node *config.GenericNode) {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			if failTime, failed := failedCopy[node.Tag]; failed && now.Sub(failTime) < failedNodeCoolTTL {
				return
			}

			proto := strings.ToLower(node.Protocol)
			if proto == "hysteria2" || proto == "tuic" || proto == "hysteria" {
				m := &NodeMetrics{
					Tag:     node.Tag,
					Address: node.Address,
					Port:    node.Port,
					RTT:     40 * time.Millisecond,
					L1Score: 40.0,
				}
				mu.Lock()
				results = append(results, m)
				p.latestPool[node.Tag] = m
				mu.Unlock()
				return
			}

			metrics := p.probeL4Handshake(ctx, node.Address, node.Port, l4Attempts)
			if metrics == nil {
				return
			}

			metrics.Tag = node.Tag
			metrics.Address = node.Address
			metrics.Port = node.Port

			if metrics.LossRate <= l4MaxLossRate && metrics.RTT > 0 {
				metrics.L1Score = float64(metrics.RTT.Milliseconds()) +
					(2.0 * float64(metrics.Jitter.Milliseconds())) +
					(metrics.LossRate * 150.0)

				p.mu.RLock()
				prevMetric, hadPrev := p.latestPool[node.Tag]
				p.mu.RUnlock()

				if hadPrev && prevMetric != nil && prevMetric.HTTPRTT == 0 {
					metrics.L1Score += 1000.0
				}

				mu.Lock()
				results = append(results, metrics)
				p.latestPool[node.Tag] = metrics
				mu.Unlock()
			}
		}(n)
	}

	wg.Wait()

	sort.Slice(results, func(i, j int) bool {
		return results[i].L1Score < results[j].L1Score
	})

	if len(results) > 35 {
		return results[:35]
	}
	return results
}

func (p *Prober) probeL4Handshake(ctx context.Context, addr string, port int, attempts int) *NodeMetrics {
	target := net.JoinHostPort(addr, fmt.Sprintf("%d", port))
	var latencies []time.Duration
	lost := 0
	dialer := net.Dialer{Timeout: l4Timeout}

	for i := 0; i < attempts; i++ {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		start := time.Now()
		conn, err := dialer.DialContext(ctx, "tcp", target)
		if err != nil {
			lost++
			if float64(lost)/float64(attempts) > l4MaxLossRate {
				break
			}
		} else {
			latencies = append(latencies, time.Since(start))
			_ = conn.Close()
		}

		if i < attempts-1 {
			timer := time.NewTimer(5 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C:
			}
		}
	}

	totalRuns := len(latencies) + lost
	if totalRuns == 0 {
		return nil
	}

	m := &NodeMetrics{
		LossRate: float64(lost) / float64(totalRuns),
	}

	if len(latencies) == 0 {
		return m
	}

	var sum time.Duration
	for _, l := range latencies {
		sum += l
	}
	m.RTT = sum / time.Duration(len(latencies))

	if len(latencies) > 1 {
		diff := latencies[1] - latencies[0]
		if diff < 0 {
			diff = -diff
		}
		m.Jitter = diff
	}

	return m
}

func (p *Prober) filterLevel2HTTP(ctx context.Context, candidates []*NodeMetrics) []*NodeMetrics {
	var wg sync.WaitGroup
	var mu sync.Mutex
	valid := make([]*NodeMetrics, 0, len(candidates))
	sem := make(chan struct{}, l2Concurrency)

	baseURL := strings.TrimRight(p.clashAPI, "/")
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "http://" + baseURL
	}

	for _, cand := range candidates {
		wg.Add(1)
		go func(c *NodeMetrics) {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			testEndpoint := fmt.Sprintf("%s/proxies/%s/delay?url=http://cp.cloudflare.com/generate_204&timeout=1600",
				baseURL, url.PathEscape(c.Tag))

			req, err := http.NewRequestWithContext(ctx, http.MethodGet, testEndpoint, nil)
			if err != nil {
				return
			}

			p.mu.RLock()
			sec := p.secret
			p.mu.RUnlock()
			if sec != "" {
				req.Header.Set("Authorization", "Bearer "+sec)
			}

			resp, err := p.apiClient.Do(req)
			if err != nil {
				c.HTTPRTT = 0
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				c.HTTPRTT = 0
				return
			}

			var res struct {
				Delay int `json:"delay"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&res); err == nil && res.Delay > 0 {
				c.HTTPRTT = time.Duration(res.Delay) * time.Millisecond

				mu.Lock()
				valid = append(valid, c)
				p.latestPool[c.Tag] = c
				mu.Unlock()
			} else {
				c.HTTPRTT = 0
			}
		}(cand)
	}

	wg.Wait()

	sort.Slice(valid, func(i, j int) bool {
		return valid[i].HTTPRTT < valid[j].HTTPRTT
	})

	return valid
}

func (p *Prober) benchmarkLevel3Speed(ctx context.Context, finalists []*NodeMetrics) *NodeMetrics {
	return finalists[0]
}

func (p *Prober) InvalidateNode(tag string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.failedPool[tag] = time.Now()
	if p.lastResult != nil && p.lastResult.Tag == tag {
		p.lastResult = nil
	}
	if m, ok := p.latestPool[tag]; ok {
		m.HTTPRTT = 0
	}
}

// IsNodeFailed проверяет, находится ли нода в кулдауне после сбоя
func (p *Prober) IsNodeFailed(tag string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if failTime, ok := p.failedPool[tag]; ok {
		return time.Since(failTime) < failedNodeCoolTTL
	}
	return false
}
