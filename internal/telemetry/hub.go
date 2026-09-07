package telemetry

import (
	"context"
	"sync"
	"time"

	"cheburnet/internal/engine"

	"github.com/gofiber/websocket/v2"
)

type Hub struct {
	clients map[*websocket.Conn]bool
	mu      sync.Mutex
}

func NewHub() *Hub {
	return &Hub{
		clients: make(map[*websocket.Conn]bool),
	}
}

func (h *Hub) Register(c *websocket.Conn) {
	h.mu.Lock()
	h.clients[c] = true
	h.mu.Unlock()
}

func (h *Hub) Unregister(c *websocket.Conn) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

func (h *Hub) BroadcastJSON(v interface{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for client := range h.clients {
		_ = client.WriteJSON(v)
	}
}

func (h *Hub) Run(ctx context.Context, eng engine.Engine) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			latencies := make(map[string]int64)

			// Опрашиваем метрики через активное ядро (sing-box или xray)
			if eng != nil {
				metrics, err := eng.CollectMetrics(ctx)
				if err == nil && metrics != nil && metrics.NodeLatencies != nil {
					latencies = metrics.NodeLatencies
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
