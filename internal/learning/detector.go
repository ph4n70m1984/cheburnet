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
	"strings"
	"sync"
	"time"

	"cheburnet/internal/config"
)

var (
	// Ловит ошибки сброса TCP и таймауты ядра на прямом выходе direct-out (ТСПУ RST / Blackhole)
	logRstRegex        = regexp.MustCompile(`outbound\/direct\[direct-out\]:.*(?:dial|connect|read).*(?:connection reset by peer|i/o timeout|handshake failure|broken pipe|EOF)`)
	domainExtractRegex = regexp.MustCompile(`([a-zA-Z0-9][-a-zA-Z0-9]*\.[a-zA-Z0-9][-a-zA-Z0-9.]+)`)
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
			l.auditActiveConnections(ctx)
		case <-logTicker.C:
			l.auditEngineLogs()
		}
	}
}

// Контур А: Анализ активных сессий Clash API (Silent Drop / таймаут ответа сервера)
func (l *DomainLearner) auditActiveConnections(ctx context.Context) {
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

		// Запрос отправлен (Upload > 30 байт), прошло > 1.2 с, входящего трафика нет
		duration := now.Sub(conn.Start)
		if conn.Upload > 30 && conn.Download == 0 && duration > 1200*time.Millisecond {
			l.recordFailure(host, conn.Metadata.ClientIP, "silent_timeout")
		}
	}
}

// Контур Б: Анализ мгновенных TCP RST через системный вывод ядра Sing-Box
func (l *DomainLearner) auditEngineLogs() {
	if l.logReader == nil {
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
				matchLower := strings.ToLower(match)
				if !strings.Contains(matchLower, ".") || net.ParseIP(matchLower) != nil {
					continue
				}
				if strings.HasSuffix(matchLower, "direct-out") || strings.HasSuffix(matchLower, "sing-box") {
					continue
				}
				l.recordFailure(matchLower, "router/local", "tcp_rst_injected")
			}
		}
	}
}

func (l *DomainLearner) recordFailure(rawHost, clientIP, reason string) {
	host := strings.ToLower(rawHost)
	if h, _, err := net.SplitHostPort(rawHost); err == nil {
		host = strings.ToLower(h)
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
		log.Printf("[domain-learning] Captured blocked candidate: %s (reason: %s, client: %s)", host, reason, clientIP)
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
