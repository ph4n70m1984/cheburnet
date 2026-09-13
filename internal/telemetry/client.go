package telemetry

import (
	"time"

	"github.com/gofiber/websocket/v2"
)

const (
	writeWait  = 2 * time.Second
	queueLimit = 16
)

type Client struct {
	conn *websocket.Conn
	send chan []byte
}

func newClient(c *websocket.Conn) *Client {
	return &Client{
		conn: c,
		send: make(chan []byte, queueLimit),
	}
}

// writePump — единственный писатель в WebSocket сокет.
func (c *Client) writePump() {
	defer func() {
		_ = c.conn.Close()
	}()

	for msg := range c.send {
		_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
		if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			return
		}
	}

	// Канал закрыт хабом — отправляем CloseMessage
	_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
	_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
}

// tryEnqueue неблокирующе помещает сообщение в очередь.
// Для снимков телеметрии вытесняет устаревшее сообщение (drop-oldest).
func (c *Client) tryEnqueue(msg []byte) bool {
	select {
	case c.send <- msg:
		return true
	default:
		// Очередь заполнена медленным клиентом: вытесняем самый старый элемент
		select {
		case <-c.send:
		default:
		}

		// Повторная попытка вставить свежие данные
		select {
		case c.send <- msg:
			return true
		default:
			// Клиент безнадежно отстал
			return false
		}
	}
}
