package client

import (
	"io"
	"net"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew_ShouldReturnClientWithExpectedID_WhenCalled(t *testing.T) {
	conn := newStubConn()
	client := New("client-1", conn)
	t.Cleanup(client.Close)

	require.NotNil(t, client)
	assert.Equal(t, "client-1", client.ID())
}

func TestClientEnqueue_ShouldReturnNil_WhenQueueHasCapacity(t *testing.T) {
	client := &Client{
		sendQ: make(chan queuedWrite, 1),
		done:  make(chan struct{}),
	}

	err := client.Enqueue([]byte("payload"))

	require.NoError(t, err)
	require.Len(t, client.sendQ, 1)
	assert.Equal(t, []byte("payload"), (<-client.sendQ).data)
}

func TestClientEnqueue_ShouldReturnErrClientQueueFull_WhenQueueIsFull(t *testing.T) {
	client := &Client{
		sendQ: make(chan queuedWrite, 1),
		done:  make(chan struct{}),
	}
	client.sendQ <- queuedWrite{data: []byte("existing")}

	err := client.Enqueue([]byte("payload"))

	require.ErrorIs(t, err, ErrClientQueueFull)
	require.Len(t, client.sendQ, 1)
	assert.Equal(t, []byte("existing"), (<-client.sendQ).data)
}

func TestClientEnqueue_ShouldWritePayloadToConnection_WhenClientIsRunning(t *testing.T) {
	conn := newStubConn()
	client := New("client-1", conn)
	t.Cleanup(client.Close)

	err := client.Enqueue([]byte("payload"))

	require.NoError(t, err)
	require.Eventually(t, func() bool {
		conn.mu.Lock()
		defer conn.mu.Unlock()
		return len(conn.writes) == 1
	}, time.Second, 10*time.Millisecond)

	assert.Equal(t, [][]byte{[]byte("payload")}, conn.snapshotWrites())
}

func TestClientEnqueue_ShouldCloseClient_WhenConnectionWriteFails(t *testing.T) {
	conn := newStubConn()
	conn.writeErr = io.ErrClosedPipe
	client := New("client-1", conn)

	err := client.Enqueue([]byte("payload"))

	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return conn.closeCount() == 1
	}, time.Second, 10*time.Millisecond)

	select {
	case <-client.done:
	default:
		t.Fatal("expected done channel to be closed after write failure")
	}
}

func TestClientSend_ShouldPreserveQueueOrder_WhenWriterIsBlocked(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		serverConn, peer := net.Pipe()
		defer peer.Close()
		client := New("client-1", serverConn)
		defer client.Close()
		require.NoError(t, client.Enqueue([]byte("first")))
		result := make(chan error, 1)
		go func() { result <- client.Send([]byte("second")) }()
		synctest.Wait()
		select {
		case <-result:
			t.Fatal("send returned before the packet was written")
		default:
		}
		payload := make([]byte, len("firstsecond"))
		_, err := io.ReadFull(peer, payload)
		require.NoError(t, err)
		assert.Equal(t, "firstsecond", string(payload))
		require.NoError(t, <-result)
	})
}

func TestClientSend_ShouldReturnErrClientQueueFull_WhenQueueIsFull(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		serverConn, peer := net.Pipe()
		defer peer.Close()
		client := New("client-1", serverConn)
		defer client.Close()
		require.NoError(t, client.Enqueue([]byte("blocked")))
		synctest.Wait()
		for range 1024 {
			require.NoError(t, client.Enqueue([]byte("queued")))
		}
		require.ErrorIs(t, client.Send([]byte("response")), ErrClientQueueFull)
	})
}

func TestClientSend_ShouldReturnWriteError_WhenConnectionWriteFails(t *testing.T) {
	testCases := []struct {
		name       string
		writeErr   error
		shortWrite bool
		expected   error
	}{
		{name: "write error", writeErr: io.ErrClosedPipe, expected: io.ErrClosedPipe},
		{name: "short write", shortWrite: true, expected: io.ErrShortWrite},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			conn := newStubConn()
			conn.writeErr = tc.writeErr
			conn.shortWrite = tc.shortWrite
			client := New("client-1", conn)
			defer client.Close()
			require.ErrorIs(t, client.Send([]byte("response")), tc.expected)
			require.Eventually(t, func() bool { return conn.closeCount() == 1 }, time.Second, 10*time.Millisecond)
		})
	}
}

func TestClientSend_ShouldUnblockWithError_WhenClientClosesWithPendingWrites(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		serverConn, peer := net.Pipe()
		defer peer.Close()
		client := New("client-1", serverConn)
		defer client.Close()
		require.NoError(t, client.Enqueue([]byte("blocked")))
		result := make(chan error, 1)
		go func() { result <- client.Send([]byte("queued")) }()
		synctest.Wait()
		client.Close()
		require.ErrorIs(t, <-result, net.ErrClosed)
		require.ErrorIs(t, client.Enqueue([]byte("after close")), net.ErrClosed)
		require.ErrorIs(t, client.Send([]byte("after close")), net.ErrClosed)
	})
}

func TestClientClose_ShouldCloseUnderlyingConnectionOnlyOnce_WhenCalledConcurrently(t *testing.T) {
	conn := newStubConn()
	client := New("client-1", conn)
	var closers sync.WaitGroup
	for range 32 {
		closers.Go(client.Close)
	}
	closers.Wait()
	assert.Equal(t, 1, conn.closeCount())
}

func TestClientClose_ShouldCloseUnderlyingConnectionOnlyOnce_WhenCalledMultipleTimes(t *testing.T) {
	conn := newStubConn()
	client := New("client-1", conn)

	client.Close()
	client.Close()

	require.Eventually(t, func() bool {
		return conn.closeCount() == 1
	}, time.Second, 10*time.Millisecond)

	select {
	case <-client.done:
	default:
		t.Fatal("expected done channel to be closed")
	}
}

type stubConn struct {
	mu         sync.Mutex
	writes     [][]byte
	writeErr   error
	shortWrite bool
	closeCalls int
}

func newStubConn() *stubConn {
	return &stubConn{}
}

func (c *stubConn) Read(_ []byte) (int, error) {
	return 0, io.EOF
}

func (c *stubConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.writeErr != nil {
		return 0, c.writeErr
	}
	if c.shortWrite {
		return len(p) - 1, nil
	}

	copied := append([]byte(nil), p...)
	c.writes = append(c.writes, copied)
	return len(p), nil
}

func (c *stubConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.closeCalls++
	return nil
}

func (c *stubConn) LocalAddr() net.Addr {
	return stubAddr("local")
}

func (c *stubConn) RemoteAddr() net.Addr {
	return stubAddr("remote")
}

func (c *stubConn) SetDeadline(time.Time) error {
	return nil
}

func (c *stubConn) SetReadDeadline(time.Time) error {
	return nil
}

func (c *stubConn) SetWriteDeadline(time.Time) error {
	return nil
}

func (c *stubConn) snapshotWrites() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()

	snapshot := make([][]byte, len(c.writes))
	for i, write := range c.writes {
		snapshot[i] = append([]byte(nil), write...)
	}

	return snapshot
}

func (c *stubConn) closeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.closeCalls
}

type stubAddr string

func (a stubAddr) Network() string {
	return "tcp"
}

func (a stubAddr) String() string {
	return string(a)
}

var _ net.Conn = (*stubConn)(nil)
