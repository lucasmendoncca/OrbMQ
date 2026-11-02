package client

import (
	"errors"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

var ErrClientQueueFull = errors.New("client queue is full")

type Client struct {
	id   string
	conn net.Conn

	sendQ        chan queuedWrite
	done         chan struct{}
	writeDone    chan struct{}
	closeOnce    sync.Once
	writeTimeout atomic.Int64
}

type queuedWrite struct {
	data     []byte
	result   chan error
	deadline time.Time
}

func New(id string, conn net.Conn) *Client {
	c := &Client{
		id:        id,
		conn:      conn,
		sendQ:     make(chan queuedWrite, 1024),
		done:      make(chan struct{}),
		writeDone: make(chan struct{}),
	}

	c.writeTimeout.Store(int64(30 * time.Second))
	go c.writeLoop()

	return c
}

func (c *Client) ID() string {
	return c.id
}

// SetWriteTimeout changes the deadline applied to subsequent connection writes.
func (c *Client) SetWriteTimeout(timeout time.Duration) error {
	if timeout <= 0 {
		return errors.New("write timeout must be positive")
	}
	c.writeTimeout.Store(int64(timeout))
	return nil
}

// Done is closed when the connection is closed, including writer failures.
func (c *Client) Done() <-chan struct{} { return c.done }

// Enqueue adds a message to the client's send queue, which is
// written to the underlying connection by the writeLoop goroutine.
// If the send queue is full, the function returns ErrClientQueueFull.
// This is intended to provide backpressure to the caller if the
// client is unable to process messages quickly enough.
func (c *Client) Enqueue(data []byte) error {
	return c.enqueue(queuedWrite{data: data})
}

// Send queues a complete packet and waits for its write to finish. Like Enqueue,
// it returns ErrClientQueueFull immediately if there is no space in the queue.
func (c *Client) Send(data []byte) error {
	return c.SendWithDeadline(data, time.Time{})
}

// SendWithDeadline caps the write at an absolute deadline. Queueing never extends
// that deadline; expired packets fail when the writer reaches them. A zero
// deadline uses the configured per-write timeout.
func (c *Client) SendWithDeadline(data []byte, deadline time.Time) error {
	result := make(chan error, 1)
	if err := c.enqueue(queuedWrite{data: data, result: result, deadline: deadline}); err != nil {
		return err
	}
	select {
	case err := <-result:
		return err
	case <-c.done:
		select {
		case err := <-result:
			return err
		default:
			return net.ErrClosed
		}
	}
}

func (c *Client) enqueue(write queuedWrite) error {
	select {
	case <-c.done:
		return net.ErrClosed
	default:
	}
	select {
	case <-c.done:
		return net.ErrClosed
	case c.sendQ <- write:
		return nil
	default:
		// queue is full - backpressure
		return ErrClientQueueFull
	}
}

// Close closes the client's underlying connection and marks it as done.
// It is safe to call Close from multiple goroutines.
func (c *Client) Close() {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.conn.Close()
	})
}

// Wait waits for the writer to exit. Call Close first to interrupt blocked I/O.
func (c *Client) Wait() {
	<-c.writeDone
}

// writeLoop is a goroutine that writes data from the client's sendQ channel
// to the underlying connection. It will block until the write is complete, and
// will return if an error is encountered during the write. If the client's
// done channel is closed, writeLoop will return.
func (c *Client) writeLoop() {
	defer close(c.writeDone)
	for {
		select {
		case write := <-c.sendQ:
			deadline := time.Now().Add(time.Duration(c.writeTimeout.Load()))
			if !write.deadline.IsZero() && write.deadline.Before(deadline) {
				deadline = write.deadline
			}
			err := c.conn.SetWriteDeadline(deadline)
			var n int
			if err == nil {
				n, err = c.conn.Write(write.data)
			}
			if err == nil && n != len(write.data) {
				err = io.ErrShortWrite
			}
			if write.result != nil {
				write.result <- err
			}
			if err != nil {
				log.Printf("client %s write error: %v", c.id, err)
				c.Close()
				return
			}

		case <-c.done:
			return
		}
	}
}
