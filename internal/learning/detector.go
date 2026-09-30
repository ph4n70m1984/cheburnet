package learning

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"cheburnet/internal/config"
)

type DomainCandidate struct {
	Domain      string    `json:"domain"`
	FailCount   int       `json:"fail_count"`
	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
	LastClient  string    `json:"last_client"`
	AutoApplied bool      `json:"auto_applied"`
}

type DomainLearner struct {
	mu          sync.RWMutex
	candidates  map[string]*DomainCandidate
	state       *config.StateManager
	client      *http.Client
	clashSecret string
	autoPromote bool
	threshold   int
}

func NewDomainLearner(state *config.StateManager, clashSecret string, autoPromote bool) *DomainLearner {
	return &DomainLearner{
		candidates:  make(map[string]*DomainCandidate),
		state:       state,
		clashSecret: clashSecret,
		autoPromote: autoPromote,
		threshold:   3,
		client: &http.Client{
			Timeout: 2 * time.Second,
		},
	}
}

type clashConnectionItem struct {
	ID       string `json:"id"`
	Metadata struct {
		Host        string `json:"host"`
		Destination string `json:"destinationIP"`
		ClientIP    string `json:"sourceIP"`
	} `json:"metadata"`
	Upload   int64     `json:"upload"`
	Download int64     `json:"download"`
	Start    time.Time `json:"start"`
	Chains   []string  `json:"chains"`
	Rule     string    `json:"rule"`
}

type clashConnectionsResponse struct {
	Connections []clashConnectionItem `json:"connections"`
}

func (l *DomainLearner) StartLoop(ctx context.Context) {
	ticker := time.NewTicker(4 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.auditDirectConnections(ctx)
		}
	}
}

func (l *DomainLearner) auditDirectConnections(ctx context.Context) {
	reqCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, "http://127.0.0.1:9090/connections", nil)
	if err != nil {
		return
	}
	if l.clashSecret != "" {
		req.Header.Set("Authorization", "Bearer "+l.clashSecret)
	}

	resp, err := l.client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return
	}
	defer resp.Body.Close()

	var data clashConnectionsResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(&data); err != nil {
		return
	}

	now := time.Now()
	for _, conn := range data.Connections {
		host := strings.TrimSpace(conn.Metadata.Host)
		if host == "" || net.ParseIP(host) != nil {
			continue
		}

		if strings.HasSuffix(host, ".lan") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".arpa") {
			continue
		}

		isDirect := false
		for _, chain := range conn.Chains {
			if strings.EqualFold(chain, "direct-out") || strings.EqualFold(chain, "direct") {
				isDirect = true
				break
			}
		}

		if !isDirect {
			continue
		}

		// Критерий сбоя: клиент послал SYN/ClientHello (Upload > 60 байт), прошло > 2.5 сек, но Download == 0
		duration := now.Sub(conn.Start)
		if conn.Upload > 60 && conn.Download == 0 && duration > 2500*time.Millisecond {
			l.recordFailure(host, conn.Metadata.ClientIP)
		}
	}
}

func (l *DomainLearner) recordFailure(rawHost, clientIP string) {
	host := strings.ToLower(rawHost)
	if h, _, err := net.SplitHostPort(rawHost); err == nil {
		host = strings.ToLower(h)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	cand, exists := l.candidates[host]
	if !exists {
		cand = &DomainCandidate{
			Domain:     host,
			FailCount:  1,
			FirstSeen:  time.Now(),
			LastSeen:   time.Now(),
			LastClient: clientIP,
		}
		l.candidates[host] = cand
	} else {
		cand.FailCount++
		cand.LastSeen = time.Now()
		cand.LastClient = clientIP
	}

	if l.autoPromote && cand.FailCount >= l.threshold && !cand.AutoApplied {
		cand.AutoApplied = true
		go l.promoteDomainToConfig(host)
	}
}

func (l *DomainLearner) promoteDomainToConfig(domain string) {
	log.Printf("[domain-learning] Promoting blocked domain candidate '%s' to custom_domains...", domain)
	candidate := l.state.Clone()

	for _, d := range candidate.CustomDomains {
		if strings.EqualFold(d, domain) {
			return
		}
	}

	candidate.CustomDomains = append(candidate.CustomDomains, domain)

	uci := config.NewUCIStorage()
	_ = uci.SaveCustomDomains(candidate.CustomDomains)

	_, _ = l.state.Commit(candidate, false)
}

func (l *DomainLearner) GetCandidates() []*DomainCandidate {
	l.mu.RLock()
	defer l.mu.RUnlock()

	out := make([]*DomainCandidate, 0, len(l.candidates))
	for _, c := range l.candidates {
		cp := *c
		out = append(out, &cp)
	}
	return out
}

func (l *DomainLearner) ClearCandidate(domain string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.candidates, strings.ToLower(domain))
}
