package client

import (
	"net"
	"sync"
)

type Client struct {
	id   string
	conn net.Conn
	mu   sync.Mutex
}

func New(id string, conn net.Conn) *Client {
	return &Client{
		id:   id,
		conn: conn,
	}
}

// Send writes the given data to the client's underlying connection.
// It is thread-safe and will block until the write is complete.
// Any errors encountered during the write will be returned.
func (c *Client) Send(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	_, err := c.conn.Write(data)
	return err
}

func (c *Client) ID() string {
	return c.id
}
