package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"cheburnet/internal/diagnostics"
	"cheburnet/internal/engine"

	"github.com/gofiber/websocket/v2"
)

const maxClients = 16

var ErrClientUnavailable = errors.New("telemetry client queue is full or connection closed")

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
	h.mu.Lock()
	// Жесткий лимит клиентов: защита от DoS, утечки дескрипторов и переполнения памяти
	if len(h.clients) >= maxClients {
		h.mu.Unlock()
		_ = c.Close()
		return
	}

	client := newClient(c)
	h.clients[c] = client
	diag := h.diagEngine
	h.mu.Unlock()

	// Запуск единственного писателя с гарантированным удалением из мапы при завершении
	go func() {
		defer h.Unregister(c)
		client.writePump()
	}()

	// Первичный снимок состояния при успешном подключении
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
		client.close()
	}
	h.mu.Unlock()
}

// SendJSON безопасно отправляет JSON конкретному клиенту и сигнализирует о переполнении/разрыве
func (h *Hub) SendJSON(c *websocket.Conn, v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}

	h.mu.RLock()
	client, ok := h.clients[c]
	h.mu.RUnlock()

	if !ok {
		return ErrClientUnavailable
	}

	if !client.tryEnqueue(data) {
		h.Unregister(c)
		return ErrClientUnavailable
	}
	return nil
}

// BroadcastJSON сериализует payload один раз и атомарно очищает отставших клиентов
func (h *Hub) BroadcastJSON(v interface{}) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}

	var deadConns []*websocket.Conn

	h.mu.RLock()
	for conn, client := range h.clients {
		if !client.tryEnqueue(data) {
			deadConns = append(deadConns, conn)
		}
	}
	h.mu.RUnlock()

	if len(deadConns) == 0 {
		return
	}

	// Атомарно удаляем неотвечающих клиентов под Lock
	h.mu.Lock()
	for _, conn := range deadConns {
		if client, ok := h.clients[conn]; ok {
			delete(h.clients, conn)
			client.close()
		}
	}
	h.mu.Unlock()
}

// Run опрашивает задержки нод и транслирует события диагностики без риска busy-loop
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
			h.mu.Lock()
			for conn, client := range h.clients {
				delete(h.clients, conn)
				client.close()
			}
			h.mu.Unlock()
			return

		case ev, ok := <-diagSub:
			if !ok {
				// КРИТИЧНО: отключаем case закрытого канала через nil,
				// исключая 100% CPU busy-loop рантайма
				diagSub = nil
				continue
			}
			h.BroadcastJSON(ev)

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
