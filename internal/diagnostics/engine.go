package diagnostics

import (
	"context"
	"sync"
	"time"
)

type DiagnosticsEngine struct {
	mu             sync.RWMutex
	trackers       map[string]*StateTracker
	activeProblems map[string]*Problem
	subscribers    map[chan DiagnosticEvent]struct{}
	totalChecks    int
	expectedPort   int
}

func NewEngine(expectedPort int) *DiagnosticsEngine {
	e := &DiagnosticsEngine{
		trackers:       make(map[string]*StateTracker),
		activeProblems: make(map[string]*Problem),
		subscribers:    make(map[chan DiagnosticEvent]struct{}),
		totalChecks:    27,
		expectedPort:   expectedPort,
	}

	defaultPolicy := HysteresisPolicy{FailuresToOpen: 2, SuccessesToClose: 2}
	strictPolicy := HysteresisPolicy{FailuresToOpen: 1, SuccessesToClose: 2}
	softPolicy := HysteresisPolicy{FailuresToOpen: 4, SuccessesToClose: 3}

	checkPolicies := map[string]HysteresisPolicy{
		"engine.process_down":        strictPolicy,
		"engine.config_invalid":      strictPolicy,
		"routing.ip_rule_missing":    strictPolicy,
		"routing.nftables_invalid":   strictPolicy,
		"subscription.update_failed": strictPolicy,
		"dns.high_latency":           softPolicy,
	}

	knownChecks := []string{
		"engine.process_down", "engine.process_unstable", "engine.port_unavailable", "engine.config_invalid",
		"engine.start_failed", "dns.listener_down", "dns.proxy_unavailable", "dns.bootstrap_failed",
		"dns.external_resolution_failed", "dns.high_latency", "routing.ip_rule_missing", "routing.route_missing",
		"routing.mark_broken", "routing.tproxy_unreachable", "routing.nftables_invalid", "connectivity.proxy_failed",
		"connectivity.internet_unreachable", "connectivity.proxy_e2e_failed", "connectivity.high_latency",
		"nodes.no_available", "nodes.partial_unavailable", "nodes.all_failed", "subscription.update_failed",
		"subscription.empty", "subscription.expired", "ruleset.update_failed", "config.drift",
	}

	for _, id := range knownChecks {
		if p, ok := checkPolicies[id]; ok {
			e.trackers[id] = NewStateTracker(p)
		} else {
			e.trackers[id] = NewStateTracker(defaultPolicy)
		}
	}

	return e
}

func (e *DiagnosticsEngine) Report(res CheckResult) {
	e.mu.Lock()
	ev := e.internalProcessResult(res)
	e.mu.Unlock()

	if ev != nil {
		e.broadcast(*ev)
	}
}

func (e *DiagnosticsEngine) ProcessSnapshot(s HealthSnapshot) {
	results := EvaluateSnapshot(s)
	var events []DiagnosticEvent

	e.mu.Lock()
	for _, res := range results {
		if ev := e.internalProcessResult(res); ev != nil {
			events = append(events, *ev)
		}
	}
	e.mu.Unlock()

	for _, ev := range events {
		e.broadcast(ev)
	}
}

// internalProcessResult вызывается строго под e.mu.Lock() и НЕ вызывает broadcast
func (e *DiagnosticsEngine) internalProcessResult(res CheckResult) *DiagnosticEvent {
	tracker, exists := e.trackers[res.CheckID]
	if !exists {
		tracker = NewStateTracker(HysteresisPolicy{FailuresToOpen: 2, SuccessesToClose: 2})
		e.trackers[res.CheckID] = tracker
	}

	transition := tracker.Step(res.Healthy)

	if transition == 1 {
		prob := &Problem{
			ID:          res.CheckID,
			Component:   res.Component,
			Severity:    res.Severity,
			Message:     res.Message,
			FirstSeen:   time.Now(),
			LastSeen:    time.Now(),
			Occurrences: 1,
			Details:     res.Details,
			Recoverable: res.Action != "",
			Action:      res.Action,
		}
		e.activeProblems[res.CheckID] = prob
		return &DiagnosticEvent{
			Type:    "diagnostic.problem_created",
			Problem: prob,
		}
	} else if transition == -1 {
		delete(e.activeProblems, res.CheckID)
		return &DiagnosticEvent{
			Type:      "diagnostic.problem_resolved",
			ProblemID: res.CheckID,
		}
	} else if tracker.State == StateActive && !res.Healthy {
		if p, ok := e.activeProblems[res.CheckID]; ok {
			p.LastSeen = time.Now()
			p.Occurrences++
		}
	}

	return nil
}

func (e *DiagnosticsEngine) Snapshot() DiagnosticSnapshot {
	e.mu.RLock()
	// Копируем активные проблемы под RLock
	shallowCopy := make(map[string]*Problem, len(e.activeProblems))
	for k, v := range e.activeProblems {
		shallowCopy[k] = v
	}
	total := e.totalChecks
	e.mu.RUnlock()

	// Correlate вызывается вне блокировки
	correlated := Correlate(shallowCopy)

	var crits, errs, warns int
	for _, p := range correlated {
		switch p.Severity {
		case SeverityCritical:
			crits++
		case SeverityError:
			errs++
		case SeverityWarning:
			warns++
		}
	}

	return DiagnosticSnapshot{
		Timestamp:      time.Now(),
		Healthy:        len(correlated) == 0,
		TotalChecks:    total,
		Critical:       crits,
		Errors:         errs,
		Warnings:       warns,
		ActiveProblems: correlated,
	}
}

func (e *DiagnosticsEngine) Subscribe() (<-chan DiagnosticEvent, func()) {
	e.mu.Lock()
	ch := make(chan DiagnosticEvent, 64)
	e.subscribers[ch] = struct{}{}
	e.mu.Unlock()

	// Snapshot генерируется без удержания Lock на subscribers
	snap := e.Snapshot()
	ch <- DiagnosticEvent{
		Type:     "diagnostic.snapshot",
		Snapshot: &snap,
	}

	unsubscribe := func() {
		e.mu.Lock()
		delete(e.subscribers, ch)
		close(ch)
		e.mu.Unlock()
	}

	return ch, unsubscribe
}

func (e *DiagnosticsEngine) broadcast(ev DiagnosticEvent) {
	e.mu.RLock()
	subs := make([]chan DiagnosticEvent, 0, len(e.subscribers))
	for ch := range e.subscribers {
		subs = append(subs, ch)
	}
	e.mu.RUnlock()

	for _, ch := range subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (e *DiagnosticsEngine) StartBackgroundLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sysCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
				sysResults := CheckSystemRouting(sysCtx, e.expectedPort)
				cancel()

				var events []DiagnosticEvent
				e.mu.Lock()
				for _, r := range sysResults {
					if ev := e.internalProcessResult(r); ev != nil {
						events = append(events, *ev)
					}
				}
				e.mu.Unlock()

				for _, ev := range events {
					e.broadcast(ev)
				}
			}
		}
	}()
}
