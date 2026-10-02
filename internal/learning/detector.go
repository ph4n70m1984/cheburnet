package learning

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"cheburnet/internal/config"
)

var (
	logRstRegex        = regexp.MustCompile(`outbound\/direct\[direct-out\]:.*(?:dial|connect|read).*(?:connection reset by peer|i/o timeout|handshake failure|broken pipe|EOF)`)
	domainExtractRegex = regexp.MustCompile(`\b([a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*\.[a-zA-Z]{2,24})\b`)
	timeDurationRegex  = regexp.MustCompile(`^\d+(\.\d+)?(s|ms|us|ns|m|h)$`)
	numericDotRegex    = regexp.MustCompile(`^\d+\.\d+`)
)

type DomainCandidate struct {
	Domain      string    `json:"domain"`
	FailCount   int       `json:"fail_count"`
	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
	LastClient  string    `json:"last_client"`
	Reason      string    `json:"reason"`
	AutoApplied bool      `json:"auto_applied"`
}

type LogReader interface {
	LastLogs() string
}

type DomainLearner struct {
	mu          sync.RWMutex
	candidates  map[string]*DomainCandidate
	state       *config.StateManager
	client      *http.Client
	logReader   LogReader
	clashSecret string
	autoPromote bool
	threshold   int
}

func NewDomainLearner(state *config.StateManager, clashSecret string, autoPromote bool, logReader LogReader) *DomainLearner {
	return &DomainLearner{
		candidates:  make(map[string]*DomainCandidate),
		state:       state,
		logReader:   logReader,
		clashSecret: clashSecret,
		autoPromote: autoPromote,
		threshold:   3,
		client: &http.Client{
			Timeout: 1500 * time.Millisecond,
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
}

type clashConnectionsResponse struct {
	Connections []clashConnectionItem `json:"connections"`
}

// Проверка, включена ли функция Domain Learning в конфигурации
func (l *DomainLearner) isEnabled() bool {
	if l.state == nil {
		return false
	}
	cfg := l.state.Clone()
	return cfg != nil && cfg.AutoLearnDomains
}

func (l *DomainLearner) StartLoop(ctx context.Context) {
	connTicker := time.NewTicker(1500 * time.Millisecond)
	logTicker := time.NewTicker(2000 * time.Millisecond)
	defer connTicker.Stop()
	defer logTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-connTicker.C:
			// Если функция отключена, запросы на обучение не выполняются[cite: 3]
			if !l.isEnabled() {
				continue
			}
			l.auditActiveConnections(ctx)
		case <-logTicker.C:
			// Если функция отключена, запросы на обучение не выполняются[cite: 3]
			if !l.isEnabled() {
				continue
			}
			l.auditEngineLogs()
		}
	}
}

func isValidFQDN(d string) bool {
	d = strings.TrimSpace(strings.ToLower(d))
	if d == "" || len(d) > 255 {
		return false
	}

	if timeDurationRegex.MatchString(d) || numericDotRegex.MatchString(d) {
		return false
	}

	if net.ParseIP(d) != nil {
		return false
	}

	for _, invalidSuffix := range []string{
		".lan", ".local", ".arpa", ".internal", ".home",
		"direct-out", "sing-box", "tproxy-in", "dns-in", "mixed-in",
	} {
		if strings.HasSuffix(d, invalidSuffix) {
			return false
		}
	}

	parts := strings.Split(d, ".")
	if len(parts) < 2 {
		return false
	}

	tld := parts[len(parts)-1]
	if len(tld) < 2 || len(tld) > 24 {
		return false
	}
	for i := 0; i < len(tld); i++ {
		if tld[i] < 'a' || tld[i] > 'z' {
			return false
		}
	}

	return true
}

func (l *DomainLearner) auditActiveConnections(ctx context.Context) {
	if !l.isEnabled() {
		return
	}

	reqCtx, cancel := context.WithTimeout(ctx, 1200*time.Millisecond)
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
		if !isValidFQDN(host) {
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

		duration := now.Sub(conn.Start)
		if conn.Upload > 30 && conn.Download == 0 && duration > 1200*time.Millisecond {
			l.recordFailure(host, conn.Metadata.ClientIP, "silent_timeout")
		}
	}
}

func (l *DomainLearner) auditEngineLogs() {
	if !l.isEnabled() || l.logReader == nil {
		return
	}

	logs := l.logReader.LastLogs()
	if len(logs) == 0 {
		return
	}

	scanner := bufio.NewScanner(strings.NewReader(logs))
	for scanner.Scan() {
		line := scanner.Text()
		if logRstRegex.MatchString(line) {
			matches := domainExtractRegex.FindAllString(line, -1)
			for _, match := range matches {
				matchLower := strings.ToLower(strings.TrimSpace(match))
				if !isValidFQDN(matchLower) {
					continue
				}
				l.recordFailure(matchLower, "router/local", "tcp_rst_injected")
			}
		}
	}
}

func (l *DomainLearner) recordFailure(rawHost, clientIP, reason string) {
	if !l.isEnabled() {
		return
	}

	host := strings.ToLower(strings.TrimSpace(rawHost))
	if h, _, err := net.SplitHostPort(rawHost); err == nil {
		host = strings.ToLower(strings.TrimSpace(h))
	}

	if !isValidFQDN(host) {
		return
	}

	if strings.Contains(host, "cloudflare") || strings.Contains(host, "gstatic") || strings.Contains(host, "yandex") {
		return
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
			Reason:     reason,
		}
		l.candidates[host] = cand
	} else {
		cand.FailCount++
		cand.LastSeen = time.Now()
		cand.LastClient = clientIP
		cand.Reason = reason
	}

	if l.autoPromote && cand.FailCount >= l.threshold && !cand.AutoApplied {
		cand.AutoApplied = true
		go l.promoteDomainToConfig(host)
	}
}

func (l *DomainLearner) promoteDomainToConfig(domain string) {
	log.Printf("[domain-learning] Promoting blocked domain '%s' into custom_domains...", domain)
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

// Возвращает отсортированный список топ-10 доменов по количеству сбоев[cite: 3]
func (l *DomainLearner) GetCandidates() []*DomainCandidate {
	l.mu.RLock()
	defer l.mu.RUnlock()

	out := make([]*DomainCandidate, 0, len(l.candidates))
	for _, c := range l.candidates {
		cp := *c
		out = append(out, &cp)
	}

	// Сортировка по количеству ошибок (FailCount) по убыванию
	sort.Slice(out, func(i, j int) bool {
		return out[i].FailCount > out[j].FailCount
	})

	// Ограничение до топ-10 элементов[cite: 3]
	if len(out) > 10 {
		out = out[:10]
	}

	return out
}

func (l *DomainLearner) ClearCandidate(domain string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.candidates, strings.ToLower(domain))
}
