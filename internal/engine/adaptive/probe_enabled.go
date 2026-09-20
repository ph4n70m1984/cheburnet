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
	defaultCacheTTL = 5 * time.Minute
	globalProbeTTL  = 10 * time.Second
	probeUserAgent  = "CheburNET-Probe/1.0"
	l4Timeout       = 600 * time.Millisecond
	l4Attempts      = 2
	l4MaxLossRate   = 0.34
	l4Concurrency   = 20
	l2Concurrency   = 5
	speedChunkSize  = 256 * 1024 // 256 KB
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
	RTT        time.Duration `json:"rtt_l4"`     // L4 TCP Handshake RTT
	HTTPRTT    time.Duration `json:"rtt_http"`   // L7 HTTP Response RTT
	Jitter     time.Duration `json:"jitter"`     // RFC 3550 Jitter
	LossRate   float64       `json:"loss_rate"`  // 0.0 - 1.0
	Throughput float64       `json:"throughput"` // Байт/сек
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
		ResponseHeaderTimeout: 1 * time.Second,
		DialContext: (&net.Dialer{
			Timeout: 1 * time.Second,
		}).DialContext,
	}

	return &Prober{
		clashAPI:   clashAPI,
		secret:     secret,
		cacheTTL:   defaultCacheTTL,
		latestPool: make(map[string]*NodeMetrics),
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

func (p *Prober) SelectBestNode(parentCtx context.Context, nodes []*config.GenericNode) *NodeMetrics {
	if len(nodes) == 0 {
		return nil
	}
	if len(nodes) == 1 {
		return &NodeMetrics{
			Tag:        nodes[0].Tag,
			Address:    nodes[0].Address,
			Port:       nodes[0].Port,
			IsFallback: false,
		}
	}

	if cached := p.getCachedNode(nodes); cached != nil {
		return cached
	}

	v, _, _ := p.sf.Do("probe_best_node", func() (interface{}, error) {
		if cached := p.getCachedNode(nodes); cached != nil {
			return cached, nil
		}

		ctx, cancel := context.WithTimeout(parentCtx, globalProbeTTL)
		defer cancel()

		startTotal := time.Now()
		res := p.doProbe(ctx, nodes)
		observePhase("total", time.Since(startTotal))

		return res, nil
	})

	if res, ok := v.(*NodeMetrics); ok && res != nil {
		return res
	}
	return p.fallbackNode(nodes)
}

func (p *Prober) doProbe(ctx context.Context, nodes []*config.GenericNode) *NodeMetrics {
	startL1 := time.Now()
	l4Candidates := p.filterLevel1L4(ctx, nodes)
	observePhase("l1", time.Since(startL1))

	if len(l4Candidates) == 0 {
		return p.fallbackNode(nodes)
	}

	startL2 := time.Now()
	top5 := p.filterLevel2HTTP(ctx, l4Candidates)
	observePhase("l2", time.Since(startL2))

	if len(top5) == 0 {
		winner := l4Candidates[0]
		p.logSelection(winner, "L1-fallback")
		return winner
	}
	if len(top5) == 1 {
		p.updateCache(top5[0])
		p.logSelection(top5[0], "L2-single")
		return top5[0]
	}

	top3 := top5
	if len(top3) > 3 {
		top3 = top3[:3]
	}

	startL3 := time.Now()
	winner := p.benchmarkLevel3Speed(ctx, top3)
	observePhase("l3", time.Since(startL3))

	p.updateCache(winner)
	p.logSelection(winner, "L3-winner")
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

	// Корректный JSON маршалинг с сохранением валидного UTF-8 для sing-box (без некорректных \U escape-последовательностей)
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
		for _, n := range nodes {
			if n.Tag == p.lastResult.Tag && n.Address == p.lastResult.Address && n.Port == p.lastResult.Port {
				return p.lastResult
			}
		}
	}
	return nil
}

func (p *Prober) fallbackNode(nodes []*config.GenericNode) *NodeMetrics {
	var result *NodeMetrics
	var stage string

	p.mu.RLock()
	if p.lastResult != nil {
		for _, n := range nodes {
			if n.Tag == p.lastResult.Tag && n.Address == p.lastResult.Address && n.Port == p.lastResult.Port {
				res := *p.lastResult
				res.IsFallback = true
				result = &res
				stage = "cached-fallback"
				break
			}
		}
	}
	p.mu.RUnlock()

	if result == nil {
		result = &NodeMetrics{
			Tag:        nodes[0].Tag,
			Address:    nodes[0].Address,
			Port:       nodes[0].Port,
			IsFallback: true,
		}
		stage = "zero-fallback"
	}

	p.logSelection(result, stage)
	return result
}

func (p *Prober) updateCache(m *NodeMetrics) {
	p.mu.Lock()
	p.lastResult = m
	p.lastAt = time.Now()
	p.mu.Unlock()
}

func (p *Prober) filterLevel1L4(ctx context.Context, nodes []*config.GenericNode) []*NodeMetrics {
	var wg sync.WaitGroup
	var mu sync.Mutex
	results := make([]*NodeMetrics, 0, len(nodes))
	sem := make(chan struct{}, l4Concurrency)

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

	if len(results) > 5 {
		return results[:5]
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

			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://cp.cloudflare.com/generate_204", nil)
			if err != nil {
				return
			}
			req.Header.Set("User-Agent", probeUserAgent)

			start := time.Now()
			resp, err := p.probeClient.Do(req)
			if err == nil && (resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent) {
				_ = resp.Body.Close()
				c.HTTPRTT = time.Since(start)

				mu.Lock()
				valid = append(valid, c)
				p.latestPool[c.Tag] = c
				mu.Unlock()
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
	var wg sync.WaitGroup
	var mu sync.Mutex

	bestNode := finalists[0]
	maxL3Score := -1.0

	for _, cand := range finalists {
		wg.Add(1)
		go func(c *NodeMetrics) {
			defer wg.Done()

			tCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
			defer cancel()

			req, err := http.NewRequestWithContext(tCtx, http.MethodGet, "https://speed.cloudflare.com/__down?bytes=262144", nil)
			if err != nil {
				return
			}
			req.Header.Set("User-Agent", probeUserAgent)

			start := time.Now()
			resp, err := p.probeClient.Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()

			lr := io.LimitReader(resp.Body, speedChunkSize)
			n, copyErr := io.Copy(io.Discard, lr)
			if copyErr != nil && copyErr != io.EOF {
				return
			}

			dur := time.Since(start).Seconds()
			if dur > 0 && n > 0 {
				c.Throughput = float64(n) / dur

				latencyPenalty := 1.0 + (float64(c.HTTPRTT.Milliseconds()) / 100.0)
				c.L3Score = c.Throughput / latencyPenalty

				mu.Lock()
				p.latestPool[c.Tag] = c
				if c.L3Score > maxL3Score {
					maxL3Score = c.L3Score
					bestNode = c
				}
				mu.Unlock()
			}
		}(cand)
	}

	wg.Wait()

	if maxL3Score <= 0 {
		log.Printf("[adaptive] WARN: all L3 speed tests failed or timed out, keeping best L2 node '%s'", bestNode.Tag)
	}

	return bestNode
}
