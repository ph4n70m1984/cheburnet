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
}

func newClient(c *websocket.Conn) *Client {
	return &Client{
		conn: c,
		send: make(chan []byte, queueLimit),
		done: make(chan struct{}),
	}
}

// close корректно и идемпотентно закрывает сессию с отправкой каноничного CloseNormalClosure
func (c *Client) close() {
	c.closeOnce.Do(func() {
		close(c.done)

		// Отправляем штатный Close frame (1000 Normal Closure), чтобы браузер
		// получил событие onclose с флагом wasClean: true вместо TCP RST (1006)
		_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
		_ = c.conn.WriteMessage(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "server shutdown"),
		)

		_ = c.conn.Close()
	})
}

// writePump — единственный писатель в WebSocket сокет с периодическим heartbeat
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
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}

		case <-ticker.C:
			// Heartbeat: держит соединение активным и сбрасывает зависшие полуоткрытые TCP-сессии
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
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
