package telemetry

import (
	"context"
	"sync"
	"time"

	"cheburnet/internal/engine"

	"github.com/gofiber/websocket/v2"
)

type Hub struct {
	clients map[*websocket.Conn]*sync.Mutex
	mu      sync.RWMutex
}

func NewHub() *Hub {
	return &Hub{
		clients: make(map[*websocket.Conn]*sync.Mutex),
	}
}

func (h *Hub) Register(c *websocket.Conn) {
	h.mu.Lock()
	h.clients[c] = &sync.Mutex{}
	h.mu.Unlock()
}

func (h *Hub) Unregister(c *websocket.Conn) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

// SendJSON безопасно отправляет JSON конкретному клиенту, избегая concurrent write
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

// BroadcastJSON рассылает телеметрию всем подключённым сессиям
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

// Run динамически опрашивает метрики активного ядра каждые 3 секунды
func (h *Hub) Run(ctx context.Context, getEngine func() engine.Engine) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
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
