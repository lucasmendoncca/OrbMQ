package client

import (
	"errors"
	"log"
	"net"
)

var ErrClientQueueFull = errors.New("client queue is full")

type Client struct {
	id   string
	conn net.Conn

	sendQ chan []byte
	done  chan struct{}
}

func New(id string, conn net.Conn) *Client {
	c := &Client{
		id:    id,
		conn:  conn,
		sendQ: make(chan []byte, 1024),
		done:  make(chan struct{}),
	}

	go c.writeLoop()

	return c
}

func (c *Client) ID() string {
	return c.id
}

// Enqueue adds a message to the client's send queue, which is
// written to the underlying connection by the writeLoop goroutine.
// If the send queue is full, the function returns ErrClientQueueFull.
// This is intended to provide backpressure to the caller if the
// client is unable to process messages quickly enough.
func (c *Client) Enqueue(data []byte) error {
	select {
	case c.sendQ <- data:
		return nil
	default:
		// queue is full - backpressure
		return ErrClientQueueFull
	}
}

// Close closes the client's underlying connection and marks it as done.
// It is safe to call Close from multiple goroutines.
func (c *Client) Close() {
	select {
	case <-c.done:
		return
	default:
		close(c.done)
		_ = c.conn.Close()
	}
}

// writeLoop is a goroutine that writes data from the client's sendQ channel
// to the underlying connection. It will block until the write is complete, and
// will return if an error is encountered during the write. If the client's
// done channel is closed, writeLoop will return.
func (c *Client) writeLoop() {
	for {
		select {
		case data := <-c.sendQ:
			if _, err := c.conn.Write(data); err != nil {
				log.Printf("client %s write error: %v", c.id, err)
				c.Close()
				return
			}

		case <-c.done:
			return
		}
	}
}
