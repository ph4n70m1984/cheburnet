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
	triggerChan chan struct{}
}

func NewWorker(state *config.StateManager, prober *Prober) *Worker {
	return &Worker{
		state:       state,
		prober:      prober,
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

// Trigger форсирует немедленный запуск замера (например, после reload из LuCI)
func (w *Worker) Trigger() {
	select {
	case w.triggerChan <- struct{}{}:
	default:
	}
}

// Start запускает цикл супервизора, полностью синхронизированный с состоянием UCI
func (w *Worker) Start(ctx context.Context) {
	if !IsEnabled() {
		return
	}

	// Пауза перед первым прогоном после запуска демона
	select {
	case <-ctx.Done():
		return
	case <-time.After(10 * time.Second):
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
	cfg := w.state.Get()

	// Работаем строго тогда, когда в UCI выбран режим 'adaptive'
	if strings.ToLower(strings.TrimSpace(cfg.ConfigType)) != "adaptive" || len(cfg.Nodes) == 0 {
		return
	}

	best := w.prober.SelectBestNode(ctx, cfg.Nodes)
	if best == nil || best.Tag == "" {
		return
	}

	if err := w.prober.SwitchOutbound(ctx, "PROXY", best.Tag); err != nil {
		log.Printf("[adaptive-worker] Failed to switch outbound to '%s': %v", best.Tag, err)
	}
}
