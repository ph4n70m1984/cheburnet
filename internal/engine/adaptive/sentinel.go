package adaptive

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cheburnet/internal/config"
)

type CensorshipSentinel struct {
	state      *config.StateManager
	controller *StateController

	directClient *http.Client
	tunnelClient *http.Client

	failuresCount int
	successCount  int

	lastSwitchTime time.Time
	minDwellTime   time.Duration

	flapsWindowStart time.Time
	flapsInWindow    int
	backoffPenalty   int

	mu sync.Mutex
}

func NewCensorshipSentinel(state *config.StateManager, controller *StateController, mixedPort int) *CensorshipSentinel {
	if mixedPort <= 0 {
		mixedPort = 4534
	}

	proxyURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", mixedPort))

	directTransport := &http.Transport{
		DisableKeepAlives: true,
		DialContext: (&net.Dialer{
			Timeout: 2 * time.Second,
		}).DialContext,
	}

	tunnelTransport := &http.Transport{
		Proxy:             http.ProxyURL(proxyURL),
		DisableKeepAlives: true,
		DialContext: (&net.Dialer{
			Timeout: 2500 * time.Millisecond,
		}).DialContext,
	}

	return &CensorshipSentinel{
		state:        state,
		controller:   controller,
		minDwellTime: 2 * time.Minute,
		directClient: &http.Client{
			Transport: directTransport,
			Timeout:   2500 * time.Millisecond,
		},
		tunnelClient: &http.Client{
			Transport: tunnelTransport,
			Timeout:   3000 * time.Millisecond,
		},
	}
}

func (s *CensorshipSentinel) Start(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

func getRouterLocation() *time.Location {
	if tzBytes, err := os.ReadFile("/etc/TZ"); err == nil {
		tzStr := strings.TrimSpace(string(tzBytes))
		if loc, err := time.LoadLocation(tzStr); err == nil {
			return loc
		}
	}
	return time.Local
}

func isTimeInWindow(now time.Time, startStr, endStr string) bool {
	if startStr == "" || endStr == "" {
		return false
	}
	layout := "15:04"
	startTime, err1 := time.Parse(layout, startStr)
	endTime, err2 := time.Parse(layout, endStr)
	if err1 != nil || err2 != nil {
		return false
	}

	loc := getRouterLocation()
	locNow := now.In(loc)

	currentMinutes := locNow.Hour()*60 + locNow.Minute()
	startMinutes := startTime.Hour()*60 + startTime.Minute()
	endMinutes := endTime.Hour()*60 + endTime.Minute()

	if startMinutes < endMinutes {
		return currentMinutes >= startMinutes && currentMinutes < endMinutes
	}
	return currentMinutes >= startMinutes || currentMinutes < endMinutes
}

func (s *CensorshipSentinel) tick(ctx context.Context) {
	cfg := s.state.Get()

	// Ручной оверрайд из интерфейса
	if cfg.ActiveGroup != "" && cfg.ActiveGroup != "auto" && cfg.ActiveGroup != "all" {
		currentGrp, _ := s.controller.GetActiveGroupNodes()
		if currentGrp != cfg.ActiveGroup {
			s.applySwitch(ctx, cfg.ActiveGroup, "manual_uci_override")
		}
		return
	}

	if !cfg.AutoFallbackLTE && !cfg.ScheduleLTEEnabled {
		return
	}

	now := time.Now()

	// Проверка расписания
	if cfg.ScheduleLTEEnabled && isTimeInWindow(now, cfg.ScheduleLTEStart, cfg.ScheduleLTEEnd) {
		s.applySwitch(ctx, "lte", "schedule_active")
		return
	}

	if !cfg.AutoFallbackLTE {
		return
	}

	var (
		foreignDirectDead   bool
		domesticDirectAlive bool
		tunnelForeignDead   bool
		wg                  sync.WaitGroup
	)

	wg.Add(3)

	// 1. Проверка прямого зарубежного канала
	go func() {
		defer wg.Done()
		start := time.Now()
		endpoints := []string{
			"http://cp.cloudflare.com/generate_204",
			"http://www.gstatic.com/generate_204",
		}
		var fails int32
		var pWg sync.WaitGroup
		for _, ep := range endpoints {
			pWg.Add(1)
			go func(u string) {
				defer pWg.Done()
				tCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
				defer cancel()
				req, _ := http.NewRequestWithContext(tCtx, http.MethodGet, u, nil)
				resp, err := s.directClient.Do(req)
				if err != nil || resp == nil || resp.StatusCode != 204 {
					atomic.AddInt32(&fails, 1)
				}
				if resp != nil {
					_ = resp.Body.Close()
				}
			}(ep)
		}
		pWg.Wait()
		foreignDirectDead = (int(fails) == len(endpoints))

		status := "alive"
		if foreignDirectDead {
			status = "dead"
		}
		sentinelProbesTotal.WithLabelValues("direct_foreign", status).Inc()
		sentinelCheckDuration.WithLabelValues("direct_foreign", status).Observe(time.Since(start).Seconds())
	}()

	// 2. Проверка отечественного сегмента (белый список)
	go func() {
		defer wg.Done()
		start := time.Now()
		domesticHosts := []string{
			"https://ya.ru",
			"https://vk.com",
			"https://www.gosuslugi.ru",
		}
		var successes int32
		var pWg sync.WaitGroup
		for _, host := range domesticHosts {
			pWg.Add(1)
			go func(u string) {
				defer pWg.Done()
				tCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
				defer cancel()
				req, _ := http.NewRequestWithContext(tCtx, http.MethodGet, u, nil)
				resp, err := s.directClient.Do(req)
				if err == nil && resp != nil && resp.StatusCode >= 200 && resp.StatusCode < 400 {
					atomic.AddInt32(&successes, 1)
				}
				if resp != nil {
					_ = resp.Body.Close()
				}
			}(host)
		}
		pWg.Wait()
		domesticDirectAlive = (atomic.LoadInt32(&successes) >= 1)

		status := "dead"
		if domesticDirectAlive {
			status = "alive"
		}
		sentinelProbesTotal.WithLabelValues("direct_domestic", status).Inc()
		sentinelCheckDuration.WithLabelValues("direct_domestic", status).Observe(time.Since(start).Seconds())
	}()

	// 3. Проверка прокси-туннеля через mixed inbound
	go func() {
		defer wg.Done()
		start := time.Now()
		tCtx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
		defer cancel()
		req, _ := http.NewRequestWithContext(tCtx, http.MethodGet, "http://cp.cloudflare.com/generate_204", nil)
		resp, err := s.tunnelClient.Do(req)
		dead := true
		if err == nil && resp != nil {
			dead = (resp.StatusCode != 204)
			_ = resp.Body.Close()
		}
		tunnelForeignDead = dead

		status := "alive"
		if dead {
			status = "dead"
		}
		sentinelProbesTotal.WithLabelValues("tunnel_foreign", status).Inc()
		sentinelCheckDuration.WithLabelValues("tunnel_foreign", status).Observe(time.Since(start).Seconds())
	}()

	wg.Wait()

	isCensored := (foreignDirectDead && domesticDirectAlive) || tunnelForeignDead
	s.evaluateHysteresis(ctx, isCensored)
}

func (s *CensorshipSentinel) evaluateHysteresis(ctx context.Context, isCensored bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	currentGroup, _ := s.controller.GetActiveGroupNodes()

	if isCensored {
		s.failuresCount++
		s.successCount = 0

		if s.failuresCount >= 3 && currentGroup != "lte" {
			s.applySwitchLocked(ctx, "lte", "censorship_detected")
		}
	} else {
		s.successCount++
		s.failuresCount = 0

		if time.Since(s.lastSwitchTime) < s.minDwellTime {
			return
		}

		required := 8 + s.backoffPenalty
		if s.successCount >= required && currentGroup == "lte" {
			s.applySwitchLocked(ctx, "general", "connectivity_restored")
		}
	}
}

func (s *CensorshipSentinel) applySwitch(ctx context.Context, targetGroup, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applySwitchLocked(ctx, targetGroup, reason)
}

func (s *CensorshipSentinel) applySwitchLocked(ctx context.Context, targetGroup, reason string) {
	currentGroup, _ := s.controller.GetActiveGroupNodes()
	if currentGroup == targetGroup {
		return
	}

	now := time.Now()

	if s.flapsWindowStart.IsZero() || now.Sub(s.flapsWindowStart) > 10*time.Minute {
		s.flapsWindowStart = now
		s.flapsInWindow = 0
		s.backoffPenalty = 0
	}

	s.flapsInWindow++
	if s.flapsInWindow > 3 {
		s.backoffPenalty += 5
		log.Printf("[sentinel] WARN: Flap rate high (%d in 10m). Backoff penalty: +%d checks",
			s.flapsInWindow, s.backoffPenalty)
	}

	if err := s.controller.SwitchGroup(ctx, targetGroup, reason); err == nil {
		s.lastSwitchTime = now
		s.failuresCount = 0
		s.successCount = 0
	} else {
		log.Printf("[sentinel] Switch to '%s' failed: %v", targetGroup, err)
	}
}
