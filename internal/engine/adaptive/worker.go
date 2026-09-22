package adaptive

import (
	"context"
	"log"
	"strings"
	"time"

	"cheburnet/internal/config"
)

type Worker struct {
	state       *config.StateManager
	prober      *Prober
	controller  *StateController
	triggerChan chan struct{}
}

func NewWorker(state *config.StateManager, prober *Prober, controller *StateController) *Worker {
	return &Worker{
		state:       state,
		prober:      prober,
		controller:  controller,
		triggerChan: make(chan struct{}, 1),
	}
}

func parseInterval(raw string, def time.Duration) time.Duration {
	d, err := time.ParseDuration(raw)
	if err != nil || d < 30*time.Second {
		return def
	}
	return d
}

func (w *Worker) Trigger() {
	select {
	case w.triggerChan <- struct{}{}:
	default:
	}
}

func (w *Worker) Start(ctx context.Context) {
	if !IsEnabled() {
		return
	}

	log.Println("[adaptive-worker] Supervisor started. Waiting 5s for engine warm-up...")
	select {
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Second):
	}

	w.checkAndSwitch(ctx)

	for {
		cfg := w.state.Get()
		interval := parseInterval(cfg.AdaptiveInterval, 3*time.Minute)

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			w.checkAndSwitch(ctx)
		case <-w.triggerChan:
			timer.Stop()
			w.checkAndSwitch(ctx)
		}
	}
}

func (w *Worker) checkAndSwitch(ctx context.Context) {
	if w.prober == nil {
		return
	}

	cfg := w.state.Get()
	if strings.ToLower(strings.TrimSpace(cfg.ConfigType)) != "adaptive" {
		return
	}

	groupName, groupNodes := w.controller.GetActiveGroupNodes()
	if len(groupNodes) == 0 {
		return
	}

	w.prober.SetSecret(cfg.ClashAPISecret)

	// Увеличиваем лимит времени на воронку замеров до 10 секунд
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	best := w.prober.SelectBestNode(probeCtx, groupNodes)
	if best == nil || best.Tag == "" {
		return
	}

	w.controller.groupSwitchMu.Lock()
	defer w.controller.groupSwitchMu.Unlock()

	currentGrp, _ := w.controller.GetActiveGroupNodes()
	if currentGrp != groupName {
		log.Printf("[adaptive-worker] Aborting: group transitioned during probe (%s -> %s)", groupName, currentGrp)
		return
	}

	switchCtx, cancelSwitch := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancelSwitch()

	if err := w.prober.SwitchOutbound(switchCtx, config.MainSelectorTag, best.Tag); err != nil {
		log.Printf("[adaptive-worker] Failed to switch outbound to '%s': %v", best.Tag, err)
	} else {
		w.controller.mu.Lock()
		w.controller.lastActiveNode = best.Tag
		w.controller.mu.Unlock()
	}
}
