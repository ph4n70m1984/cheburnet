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

	// Запускаем фоновый легковесный сторожевой таймер (Watchdog) для активной ноды
	go w.startActiveNodeWatchdog(ctx)

	// Первичный подбор ноды при старте
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

// startActiveNodeWatchdog непрерывно следит за текущей выбранной нодой каждые 12 секунд.
// Если активный сервер упал (2 сбоя подряд) — мгновенно сбрасывает селектор в 'auto'.
func (w *Worker) startActiveNodeWatchdog(ctx context.Context) {
	ticker := time.NewTicker(12 * time.Second)
	defer ticker.Stop()

	failStreak := 0

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cfg := w.state.Get()
			if strings.ToLower(strings.TrimSpace(cfg.ConfigType)) != "adaptive" {
				failStreak = 0
				continue
			}

			activeNode := w.controller.GetLastActiveNode()
			// Если мы уже в автовыборе — встроенный urltest ядра сам выбирает живой узел
			if activeNode == "" || activeNode == "auto" || strings.HasSuffix(activeNode, "-auto") {
				failStreak = 0
				continue
			}

			// Быстрый замер активной ноды
			isAlive := w.controller.CheckActiveNodeHealth(ctx, activeNode)
			if !isAlive {
				failStreak++
				log.Printf("[adaptive-watchdog] Active node '%s' failed health check (%d/2)", activeNode, failStreak)
			} else {
				failStreak = 0
			}

			// Если активный сервер умер (2 сбоя подряд = ~24 сек) — мгновенный фолбэк на 'auto'
			if failStreak >= 2 {
				log.Printf("[adaptive-watchdog] Node '%s' is DEAD. Emergency fallback to 'auto' urltest!", activeNode)
				if err := w.controller.ResetToAuto(ctx); err == nil {
					failStreak = 0
					// Запускаем внеочередной полный подбор новой рабочей ноды в фоне
					w.Trigger()
				}
			}
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

	probeCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()

	best := w.prober.SelectBestNode(probeCtx, groupNodes)

	// Аварийная защита: если ни один кандидат не признан живым — сбрасываем в auto,
	// чтобы роутер не оставался висеть на мертвом сервере!
	if best == nil || best.Tag == "" || best.HTTPRTT <= 0 {
		log.Printf("[adaptive-worker] No healthy candidate found in group '%s', resetting selector to auto", groupName)
		_ = w.controller.ResetToAuto(ctx)
		return
	}

	// Защита от лишних переключений: если мы уже на этой ноде — ничего не делаем
	currentActive := w.controller.GetLastActiveNode()
	if currentActive == best.Tag {
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
		log.Printf("[adaptive-worker] Failed to switch outbound to '%s': %v, falling back to auto", best.Tag, err)
		_ = w.prober.SwitchOutbound(switchCtx, config.MainSelectorTag, "auto")
		w.controller.SetLastActiveNode("auto")
	} else {
		w.controller.SetLastActiveNode(best.Tag)
		log.Printf("[adaptive-worker] Switched best node: '%s' (RTT: %d ms, Jitter: %d ms)",
			best.Tag, best.HTTPRTT, best.Jitter)
	}
}
