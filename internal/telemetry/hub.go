package telemetry

import (
	"context"
	"sync"
	"time"

	"cheburnet/internal/diagnostics"
	"cheburnet/internal/engine"

	"github.com/gofiber/websocket/v2"
)

type Hub struct {
	clients    map[*websocket.Conn]*sync.Mutex
	mu         sync.RWMutex
	diagEngine *diagnostics.DiagnosticsEngine
}

func NewHub() *Hub {
	return &Hub{
		clients: make(map[*websocket.Conn]*sync.Mutex),
	}
}

func (h *Hub) SetDiagnosticsEngine(d *diagnostics.DiagnosticsEngine) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.diagEngine = d
}

func (h *Hub) Register(c *websocket.Conn) {
	h.mu.Lock()
	h.clients[c] = &sync.Mutex{}
	diag := h.diagEngine
	h.mu.Unlock()

	// При подключении клиента сразу отправляем диагностический снимок
	if diag != nil {
		snap := diag.Snapshot()
		_ = h.SendJSON(c, diagnostics.DiagnosticEvent{
			Type:     "diagnostic.snapshot",
			Snapshot: &snap,
		})
	}
}

func (h *Hub) Unregister(c *websocket.Conn) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

// SendJSON безопасно отправляет JSON конкретному клиенту
func (h *Hub) SendJSON(c *websocket.Conn, v interface{}) error {
	h.mu.RLock()
	writeMu, ok := h.clients[c]
	h.mu.RUnlock()

	if !ok {
		return nil
	}

	writeMu.Lock()
	defer writeMu.Unlock()
	return c.WriteJSON(v)
}

// BroadcastJSON рассылает данные всем активным подключениям
func (h *Hub) BroadcastJSON(v interface{}) {
	h.mu.RLock()
	activeClients := make(map[*websocket.Conn]*sync.Mutex, len(h.clients))
	for client, writeMu := range h.clients {
		activeClients[client] = writeMu
	}
	h.mu.RUnlock()

	for client, writeMu := range activeClients {
		go func(c *websocket.Conn, mu *sync.Mutex) {
			mu.Lock()
			defer mu.Unlock()
			if err := c.WriteJSON(v); err != nil {
				h.Unregister(c)
			}
		}(client, writeMu)
	}
}

// Run опрашивает задержки нод и транслирует события диагностики
func (h *Hub) Run(ctx context.Context, getEngine func() engine.Engine) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	var diagSub <-chan diagnostics.DiagnosticEvent
	var unsub func()

	h.mu.RLock()
	if h.diagEngine != nil {
		diagSub, unsub = h.diagEngine.Subscribe()
	}
	h.mu.RUnlock()

	if unsub != nil {
		defer unsub()
	}

	for {
		select {
		case <-ctx.Done():
			return

		case ev, ok := <-diagSub:
			if ok {
				h.BroadcastJSON(ev)
			}

		case <-ticker.C:
			latencies := make(map[string]int64)

			if getEngine != nil {
				currentEng := getEngine()
				if currentEng != nil {
					queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
					metrics, err := currentEng.CollectMetrics(queryCtx)
					cancel()

					if err == nil && metrics != nil && metrics.NodeLatencies != nil {
						latencies = metrics.NodeLatencies
					}
				}
			}

			payload := map[string]interface{}{
				"node_latencies": latencies,
				"timestamp":      time.Now().Unix(),
			}
			h.BroadcastJSON(payload)
		}
	}
}
