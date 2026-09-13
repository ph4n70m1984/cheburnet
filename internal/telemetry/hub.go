package telemetry

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"cheburnet/internal/diagnostics"
	"cheburnet/internal/engine"

	"github.com/gofiber/websocket/v2"
)

type Hub struct {
	clients    map[*websocket.Conn]*Client
	mu         sync.RWMutex
	diagEngine *diagnostics.DiagnosticsEngine
}

func NewHub() *Hub {
	return &Hub{
		clients: make(map[*websocket.Conn]*Client),
	}
}

func (h *Hub) SetDiagnosticsEngine(d *diagnostics.DiagnosticsEngine) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.diagEngine = d
}

func (h *Hub) Register(c *websocket.Conn) {
	client := newClient(c)

	h.mu.Lock()
	h.clients[c] = client
	diag := h.diagEngine
	h.mu.Unlock()

	// Запускаем единственный writer для этого подключения
	go client.writePump()

	// При подключении клиента отправляем диагностический снимок через защищенную очередь
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
	client, ok := h.clients[c]
	if ok {
		delete(h.clients, c)
		close(client.send)
	}
	h.mu.Unlock()
}

// SendJSON безопасно отправляет JSON конкретному клиенту без блокировок
func (h *Hub) SendJSON(c *websocket.Conn, v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}

	h.mu.RLock()
	client, ok := h.clients[c]
	h.mu.RUnlock()

	if !ok {
		return nil
	}

	if !client.tryEnqueue(data) {
		h.Unregister(c)
		_ = c.Close()
	}
	return nil
}

// BroadcastJSON сериализует payload один раз и распределяет по очередям без создания горутин
func (h *Hub) BroadcastJSON(v interface{}) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}

	h.mu.RLock()
	var deadConnections []*websocket.Conn
	for conn, client := range h.clients {
		if !client.tryEnqueue(data) {
			deadConnections = append(deadConnections, conn)
		}
	}
	h.mu.RUnlock()

	// Отключаем клиентов, не справившихся даже со сбросом старых сообщений
	for _, conn := range deadConnections {
		h.Unregister(conn)
		_ = conn.Close()
	}
}

// Run опрашивает задержки нод и транслирует события диагностики
func (h *Hub) Run(ctx context.Context, getEngine func() engine.Engine, getActiveNode func() string) {
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

			var activeTag string
			if getActiveNode != nil {
				activeTag = getActiveNode()
			}

			payload := map[string]interface{}{
				"node_latencies": latencies,
				"active_node":    activeTag,
				"timestamp":      time.Now().Unix(),
			}
			h.BroadcastJSON(payload)
		}
	}
}
