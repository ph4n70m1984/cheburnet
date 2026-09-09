package diagnostics

import (
	"context"
	"maps"
	"sync"
	"time"

	"cheburnet/internal/engine"
)

type DiagnosticsEngine struct {
	mu             sync.RWMutex
	ready          bool
	trackers       map[string]*StateTracker
	activeProblems map[string]*Problem
	subscribers    map[chan DiagnosticEvent]struct{}
	totalChecks    int
	expectedPort   int
}

func cloneProblem(p *Problem) *Problem {
	if p == nil {
		return nil
	}
	cp := *p
	if p.Details != nil {
		cp.Details = maps.Clone(p.Details)
	}
	if p.Symptoms != nil {
		cp.Symptoms = append([]string(nil), p.Symptoms...)
	}
	return &cp
}

func NewEngine(expectedPort int) *DiagnosticsEngine {
	const totalRealChecks = 15

	e := &DiagnosticsEngine{
		ready:          false,
		trackers:       make(map[string]*StateTracker),
		activeProblems: make(map[string]*Problem),
		subscribers:    make(map[chan DiagnosticEvent]struct{}),
		totalChecks:    totalRealChecks,
		expectedPort:   expectedPort,
	}

	defaultPolicy := HysteresisPolicy{FailuresToOpen: 2, SuccessesToClose: 2}
	strictPolicy := HysteresisPolicy{FailuresToOpen: 1, SuccessesToClose: 2}
	softPolicy := HysteresisPolicy{FailuresToOpen: 4, SuccessesToClose: 3}

	checkPolicies := map[string]HysteresisPolicy{
		"engine.process_down":      strictPolicy,
		"engine.config_invalid":    strictPolicy,
		"routing.ip_rule_missing":  strictPolicy,
		"routing.nftables_invalid": strictPolicy,
		"dns.high_latency":         softPolicy,
	}

	knownChecks := []string{
		"engine.process_down", "engine.process_unstable", "engine.port_unavailable", "engine.config_invalid",
		"dns.listener_down", "dns.proxy_unavailable", "dns.bootstrap_failed", "dns.high_latency",
		"connectivity.internet_unreachable", "connectivity.proxy_e2e_failed",
		"nodes.no_available", "nodes.partial_unavailable",
		"routing.ip_rule_missing", "routing.nftables_invalid", "config.drift",
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

func (e *DiagnosticsEngine) ProcessSnapshot(s engine.HealthSnapshot) {
	if !s.Initialized {
		return
	}

	results := EvaluateSnapshot(s)
	var events []DiagnosticEvent

	e.mu.Lock()
	e.ready = true
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
			Problem: cloneProblem(prob),
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
			if p.Message != res.Message || p.Severity != res.Severity {
				p.Message = res.Message
				p.Severity = res.Severity
				p.Details = res.Details
				return &DiagnosticEvent{
					Type:    "diagnostic.problem_updated",
					Problem: cloneProblem(p),
				}
			}
		}
	}

	return nil
}

func (e *DiagnosticsEngine) buildSnapshotLocked() DiagnosticSnapshot {
	deepCopies := make(map[string]*Problem, len(e.activeProblems))
	for id, p := range e.activeProblems {
		deepCopies[id] = cloneProblem(p)
	}

	correlatedRaw := Correlate(deepCopies)

	// Нормализация среза проблем к []*Problem
	var activeProblems []*Problem
	switch v := any(correlatedRaw).(type) {
	case []*Problem:
		activeProblems = v
	case []Problem:
		activeProblems = make([]*Problem, len(v))
		for i := range v {
			activeProblems[i] = &v[i]
		}
	}

	var crits, errs, warns int
	for _, p := range activeProblems {
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
		Ready:          e.ready,
		Timestamp:      time.Now(),
		Healthy:        e.ready && len(activeProblems) == 0,
		TotalChecks:    e.totalChecks,
		Critical:       crits,
		Errors:         errs,
		Warnings:       warns,
		ActiveProblems: activeProblems,
	}
}

func (e *DiagnosticsEngine) Snapshot() DiagnosticSnapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.buildSnapshotLocked()
}

func (e *DiagnosticsEngine) Subscribe() (<-chan DiagnosticEvent, func()) {
	e.mu.Lock()
	ch := make(chan DiagnosticEvent, 64)

	snap := e.buildSnapshotLocked()
	ch <- DiagnosticEvent{
		Type:     "diagnostic.snapshot",
		Snapshot: &snap,
	}

	e.subscribers[ch] = struct{}{}
	e.mu.Unlock()

	unsubscribe := func() {
		e.mu.Lock()
		delete(e.subscribers, ch)
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
