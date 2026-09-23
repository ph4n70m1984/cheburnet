package telemetry

import (
	"sync"
	"time"

	"github.com/gofiber/websocket/v2"
)

const (
	writeWait  = 2 * time.Second
	pingPeriod = 15 * time.Second
	queueLimit = 16
)

type Client struct {
	conn      *websocket.Conn
	send      chan []byte
	closeOnce sync.Once
	done      chan struct{}
	writeMu   sync.Mutex // Защита от concurrent write в websocket
}

func newClient(c *websocket.Conn) *Client {
	return &Client{
		conn: c,
		send: make(chan []byte, queueLimit),
		done: make(chan struct{}),
	}
}

// write потокобезопасно отправляет сообщение в WebSocket с установкой таймаута
func (c *Client) write(messageType int, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
	return c.conn.WriteMessage(messageType, payload)
}

// close корректно и идемпотентно закрывает сессию с отправкой каноничного CloseNormalClosure
func (c *Client) close() {
	c.closeOnce.Do(func() {
		close(c.done)

		// Отправляем Close frame безопасно через мьютекс
		_ = c.write(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "server shutdown"),
		)

		_ = c.conn.Close()
	})
}

// writePump — единственный обработчик исходящей очереди и heartbeat
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.close()
	}()

	for {
		select {
		case <-c.done:
			// Сокет уже закрыт или помечен на закрытие, немедленно выходим
			return

		case msg, ok := <-c.send:
			if !ok {
				_ = c.write(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.write(websocket.TextMessage, msg); err != nil {
				return
			}

		case <-ticker.C:
			// Heartbeat: держит соединение активным и сбрасывает зависшие полуоткрытые TCP-сессии
			if err := c.write(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// tryEnqueue неблокирующе помещает сообщение в очередь с защитой drop-oldest
func (c *Client) tryEnqueue(msg []byte) bool {
	select {
	case <-c.done:
		return false
	default:
	}

	select {
	case c.send <- msg:
		return true
	default:
		// Вытесняем устаревшее сообщение (drop-oldest)
		select {
		case <-c.send:
		default:
		}

		// Повторная попытка вставить свежий срез телеметрии
		select {
		case c.send <- msg:
			return true
		default:
			return false
		}
	}
}
