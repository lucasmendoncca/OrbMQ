package server

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/lucasmendoncca/OrbMQ/internal/broker"
	"github.com/lucasmendoncca/OrbMQ/internal/protocol"
	"github.com/lucasmendoncca/OrbMQ/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStart_ShouldAcknowledgeQoS1PublishAndDeliverToSubscriber_WhenSubscriberRequestsQoS1(t *testing.T) {
	srv := startTestServer(t)

	subConn, sessionPresent := connectClient(t, srv, connectOptions{clientID: "sub-qos1", cleanSession: false})
	defer subConn.Close()
	assert.False(t, sessionPresent)
	subscribe(t, subConn, 1, "sensors/+", 1, []byte{1})

	pubConn, sessionPresent := connectClient(t, srv, connectOptions{clientID: "pub-qos1", cleanSession: true})
	defer pubConn.Close()
	assert.False(t, sessionPresent)

	writePublish(t, pubConn, &protocol.PublishPacket{
		Topic:    "sensors/temp",
		Payload:  []byte("25.3"),
		QoS:      1,
		PacketID: 7,
	})

	pubAck := decodeClientPacket(t, pubConn)
	ack, ok := pubAck.(*protocol.PubAckPacket)
	require.True(t, ok)
	assert.Equal(t, uint16(7), ack.PacketID)

	delivered := decodeClientPacket(t, subConn)
	pub, ok := delivered.(*protocol.PublishPacket)
	require.True(t, ok)
	assert.Equal(t, byte(1), pub.QoS)
	assert.NotZero(t, pub.PacketID)
	assert.Equal(t, []byte("25.3"), pub.Payload)
	assert.False(t, pub.DUP)
}

func TestStart_ShouldDowngradeDeliveryQoS_WhenSubscriptionQoSIsLowerThanPublish(t *testing.T) {
	srv := startTestServer(t)

	subConn, sessionPresent := connectClient(t, srv, connectOptions{clientID: "sub-qos0", cleanSession: true})
	defer subConn.Close()
	assert.False(t, sessionPresent)
	subscribe(t, subConn, 1, "sensors/+", 0, []byte{0})

	pubConn, sessionPresent := connectClient(t, srv, connectOptions{clientID: "pub-downgrade", cleanSession: true})
	defer pubConn.Close()
	assert.False(t, sessionPresent)

	writePublish(t, pubConn, &protocol.PublishPacket{
		Topic:    "sensors/temp",
		Payload:  []byte("25.3"),
		QoS:      1,
		PacketID: 11,
	})

	_ = decodeClientPacket(t, pubConn)
	delivered := decodeClientPacket(t, subConn)
	pub, ok := delivered.(*protocol.PublishPacket)
	require.True(t, ok)
	assert.Equal(t, byte(0), pub.QoS)
	assert.Equal(t, uint16(0), pub.PacketID)
}

func TestStart_ShouldReplayInflightPublishWithDup_WhenPersistentSessionReconnects(t *testing.T) {
	srv := startTestServer(t)

	subConn, sessionPresent := connectClient(t, srv, connectOptions{clientID: "persistent-sub", cleanSession: false})
	assert.False(t, sessionPresent)
	subscribe(t, subConn, 1, "sensors/+", 1, []byte{1})

	pubConn, sessionPresent := connectClient(t, srv, connectOptions{clientID: "pub-replay", cleanSession: true})
	defer pubConn.Close()
	assert.False(t, sessionPresent)

	writePublish(t, pubConn, &protocol.PublishPacket{
		Topic:    "sensors/temp",
		Payload:  []byte("31.0"),
		QoS:      1,
		PacketID: 15,
	})

	_ = decodeClientPacket(t, pubConn)
	firstDelivery := decodeClientPacket(t, subConn)
	firstPublish, ok := firstDelivery.(*protocol.PublishPacket)
	require.True(t, ok)
	require.Equal(t, byte(1), firstPublish.QoS)

	_ = subConn.Close()

	reconnected, sessionPresent := connectClient(t, srv, connectOptions{clientID: "persistent-sub", cleanSession: false})
	defer reconnected.Close()
	assert.True(t, sessionPresent)

	replayed := decodeClientPacket(t, reconnected)
	replayedPublish, ok := replayed.(*protocol.PublishPacket)
	require.True(t, ok)
	assert.True(t, replayedPublish.DUP)
	assert.Equal(t, firstPublish.PacketID, replayedPublish.PacketID)
	assert.Equal(t, firstPublish.Payload, replayedPublish.Payload)

	writePubAck(t, reconnected, replayedPublish.PacketID)

	writeDisconnect(t, reconnected)
	_ = reconnected.Close()

	reconnectedAgain, sessionPresent := connectClient(t, srv, connectOptions{clientID: "persistent-sub", cleanSession: false})
	defer reconnectedAgain.Close()
	assert.True(t, sessionPresent)
	assertNoPacket(t, reconnectedAgain)
}

func TestStart_ShouldDiscardInflightState_WhenPersistentClientReconnectsWithCleanSession(t *testing.T) {
	srv := startTestServer(t)

	subConn, _ := connectClient(t, srv, connectOptions{clientID: "clean-reset", cleanSession: false})
	subscribe(t, subConn, 1, "reset/+", 1, []byte{1})

	pubConn, _ := connectClient(t, srv, connectOptions{clientID: "reset-pub", cleanSession: true})
	defer pubConn.Close()

	writePublish(t, pubConn, &protocol.PublishPacket{
		Topic:    "reset/topic",
		Payload:  []byte("pending"),
		QoS:      1,
		PacketID: 17,
	})

	_ = decodeClientPacket(t, pubConn)
	_ = decodeClientPacket(t, subConn)
	_ = subConn.Close()

	cleanConn, sessionPresent := connectClient(t, srv, connectOptions{clientID: "clean-reset", cleanSession: true})
	defer cleanConn.Close()
	assert.False(t, sessionPresent)
	assertNoPacket(t, cleanConn)
}

func TestStart_ShouldDeliverRetainedPublishWithQoS1_WhenSubscriberRequestsQoS1(t *testing.T) {
	srv := startTestServer(t)

	pubConn, sessionPresent := connectClient(t, srv, connectOptions{clientID: "retained-pub", cleanSession: true})
	defer pubConn.Close()
	assert.False(t, sessionPresent)

	writePublish(t, pubConn, &protocol.PublishPacket{
		Topic:    "retained/topic",
		Payload:  []byte("state"),
		Retain:   true,
		QoS:      1,
		PacketID: 21,
	})
	_ = decodeClientPacket(t, pubConn)

	subConn, sessionPresent := connectClient(t, srv, connectOptions{clientID: "retained-sub", cleanSession: true})
	defer subConn.Close()
	assert.False(t, sessionPresent)

	setDeadline(t, subConn)
	_, err := subConn.Write(encodeSubscribePacket(t, 1, "retained/#", 1))
	require.NoError(t, err)

	pub := readPublishWithinPackets(t, subConn, 2)
	require.NotNil(t, pub)
	assert.True(t, pub.Retain)
	assert.Equal(t, byte(1), pub.QoS)
	assert.NotZero(t, pub.PacketID)
}

func TestStart_ShouldDeliverWillWithQoS1_WhenClientDisconnectsUnexpectedly(t *testing.T) {
	srv := startTestServer(t)

	subConn, sessionPresent := connectClient(t, srv, connectOptions{clientID: "will-sub", cleanSession: true})
	defer subConn.Close()
	assert.False(t, sessionPresent)
	subscribe(t, subConn, 1, "wills/+", 1, []byte{1})

	willConn, sessionPresent := connectClient(t, srv, connectOptions{
		clientID:     "will-pub",
		cleanSession: true,
		will: &protocol.WillMessage{
			Topic:   "wills/test",
			Payload: []byte("bye"),
			QoS:     1,
		},
	})
	assert.False(t, sessionPresent)

	_ = willConn.Close()

	willPacket := decodeClientPacket(t, subConn)
	pub, ok := willPacket.(*protocol.PublishPacket)
	require.True(t, ok)
	assert.Equal(t, "wills/test", pub.Topic)
	assert.Equal(t, []byte("bye"), pub.Payload)
	assert.Equal(t, byte(1), pub.QoS)
}

func TestStart_ShouldReplyWithPingResp_WhenClientSendsPingReq(t *testing.T) {
	srv := startTestServer(t)

	conn, _ := connectClient(t, srv, connectOptions{clientID: "ping-client", cleanSession: true})
	defer conn.Close()

	setDeadline(t, conn)
	_, err := conn.Write([]byte{0xC0, 0x00})
	require.NoError(t, err)

	packet := readPacketBytes(t, conn)
	assert.Equal(t, []byte{0xD0, 0x00}, packet)
}

func TestStart_ShouldGrantQoS1_WhenClientSubscribesWithQoS2(t *testing.T) {
	srv := startTestServer(t)

	conn, _ := connectClient(t, srv, connectOptions{clientID: "sub-grant", cleanSession: true})
	defer conn.Close()

	subscribe(t, conn, 1, "grant/+", 2, []byte{1})
}

func TestStart_ShouldStopDeliveringMessages_WhenClientUnsubscribes(t *testing.T) {
	srv := startTestServer(t)

	subConn, _ := connectClient(t, srv, connectOptions{clientID: "unsub-client", cleanSession: true})
	defer subConn.Close()
	subscribe(t, subConn, 1, "unsub/topic", 1, []byte{1})

	pubConn, _ := connectClient(t, srv, connectOptions{clientID: "unsub-pub", cleanSession: true})
	defer pubConn.Close()

	writePublish(t, pubConn, &protocol.PublishPacket{
		Topic:    "unsub/topic",
		Payload:  []byte("before"),
		QoS:      1,
		PacketID: 23,
	})
	_ = decodeClientPacket(t, pubConn)
	_ = decodeClientPacket(t, subConn)

	setDeadline(t, subConn)
	_, err := subConn.Write(encodeUnsubscribePacketForTest(t, 3, "unsub/topic"))
	require.NoError(t, err)
	packet := readPacketBytes(t, subConn)
	assert.Equal(t, []byte{0xB0, 0x02, 0x00, 0x03}, packet)

	writePublish(t, pubConn, &protocol.PublishPacket{
		Topic:    "unsub/topic",
		Payload:  []byte("after"),
		QoS:      1,
		PacketID: 24,
	})
	_ = decodeClientPacket(t, pubConn)

	assertNoPacket(t, subConn)
}

func TestStart_ShouldCloseConnection_WhenFirstPacketIsNotConnect(t *testing.T) {
	srv := startTestServer(t)

	conn := openClientConn(t, srv)
	defer conn.Close()

	setDeadline(t, conn)
	_, err := conn.Write([]byte{0xC0, 0x00})
	require.NoError(t, err)

	assertNoPacket(t, conn)
}

func TestStart_ShouldCloseConnection_WhenConnectPacketIsMalformed(t *testing.T) {
	srv := startTestServer(t)

	conn := openClientConn(t, srv)
	defer conn.Close()

	setDeadline(t, conn)
	_, err := conn.Write([]byte{0x10, 0x01, 0x00})
	require.NoError(t, err)

	assertNoPacket(t, conn)
}

func TestStart_ShouldAttemptConnAck_WhenAcceptedConnectionWriteFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &scriptConn{
		readChunks: [][]byte{encodeConnectPacket(t, connectOptions{clientID: "connack-fail", cleanSession: true})},
		writeErr:   io.ErrClosedPipe,
	}

	accepted := false
	srv := newTestServer(WithListener(func(network, addr string) (net.Listener, error) {
		return &stubListener{
			acceptFn: func() (net.Conn, error) {
				if accepted {
					<-ctx.Done()
					return nil, errStubNet
				}
				accepted = true
				cancel()
				return conn, nil
			},
			closeFn: func() error { return nil },
			addrFn:  func() net.Addr { return stubAddr("listener") },
		}, nil
	}))

	err := srv.Start(ctx)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return conn.writeCalls >= 1
	}, time.Second, 10*time.Millisecond)
}

func TestStart_ShouldStopConnectionAfterReplayInflightEncodingFails_WhenPersistentSessionReconnects(t *testing.T) {
	srv := startTestServer(t, WithPublishEncoder(func(pub *protocol.PublishPacket) ([]byte, error) {
		return nil, errors.New("encode failed")
	}))

	state, _ := srv.srv.sessions.GetOrCreate("replay-fail")
	_, err := state.TrackOutbound(&protocol.PublishPacket{
		Topic:   "replay/topic",
		Payload: []byte("x"),
		QoS:     1,
	})
	require.NoError(t, err)

	conn, sessionPresent := connectClient(t, srv, connectOptions{clientID: "replay-fail", cleanSession: false})
	defer conn.Close()

	assert.True(t, sessionPresent)
	assertConnectionEventuallyClosed(t, conn, 2*time.Second)
}

func TestStart_ShouldCloseConnectionAfterKeepAliveTimeout_WhenClientStopsSendingPackets(t *testing.T) {
	srv := startTestServer(t)

	conn, sessionPresent := connectClient(t, srv, connectOptions{
		clientID:     "keepalive-timeout",
		cleanSession: true,
		keepAlive:    1,
	})
	defer conn.Close()

	assert.False(t, sessionPresent)
	assertConnectionEventuallyClosed(t, conn, 3*time.Second)
}

func TestStart_ShouldClosePublisherConnectionWithoutPubAck_WhenOutboundEncodingFails(t *testing.T) {
	srv := startTestServer(t, WithPublishEncoder(func(pub *protocol.PublishPacket) ([]byte, error) {
		if pub.Topic == "encode/fail" {
			return nil, errors.New("encode failed")
		}
		return encodePublishPacket(pub)
	}))

	subConn, _ := connectClient(t, srv, connectOptions{clientID: "encode-fail-sub", cleanSession: false})
	defer subConn.Close()
	subscribe(t, subConn, 1, "encode/+", 1, []byte{1})

	pubConn, _ := connectClient(t, srv, connectOptions{clientID: "encode-fail-pub", cleanSession: true})
	defer pubConn.Close()

	writePublish(t, pubConn, &protocol.PublishPacket{
		Topic:    "encode/fail",
		Payload:  []byte("value"),
		QoS:      1,
		PacketID: 31,
	})

	assertConnectionEventuallyClosed(t, pubConn, 2*time.Second)
}

func TestStart_ShouldCloseSubscriberConnectionWithoutSubAck_WhenRetainedDeliveryExhaustsPacketIDs(t *testing.T) {
	srv := startTestServer(t)

	pubConn, _ := connectClient(t, srv, connectOptions{clientID: "retained-source", cleanSession: true})
	defer pubConn.Close()
	writePublish(t, pubConn, &protocol.PublishPacket{
		Topic:    "retained/exhaust",
		Payload:  []byte("value"),
		Retain:   true,
		QoS:      1,
		PacketID: 33,
	})
	_ = decodeClientPacket(t, pubConn)

	subConn, _ := connectClient(t, srv, connectOptions{clientID: "retained-exhaust", cleanSession: false})
	defer subConn.Close()

	state, ok := srv.srv.sessions.Get("retained-exhaust")
	require.True(t, ok)
	exhaustPacketIDs(t, state)

	setDeadline(t, subConn)
	_, err := subConn.Write(encodeSubscribePacket(t, 1, "retained/#", 1))
	require.NoError(t, err)

	assertConnectionEventuallyClosed(t, subConn, 2*time.Second)
}

func TestNew_ShouldApplyFunctionalOptions_WhenProvided(t *testing.T) {
	listenerFn := func(network, addr string) (net.Listener, error) {
		return nil, errors.New("listen override")
	}
	encoderFn := func(pub *protocol.PublishPacket) ([]byte, error) {
		return []byte("encoded"), nil
	}

	srv := newTestServer(
		WithListener(listenerFn),
		WithPublishEncoder(encoderFn),
	)

	assert.NotNil(t, srv)
	_, err := srv.listenFn("tcp", ":0")
	require.EqualError(t, err, "listen override")

	raw, err := srv.encodePublishFn(&protocol.PublishPacket{})
	require.NoError(t, err)
	assert.Equal(t, []byte("encoded"), raw)
}

func TestStart_ShouldReturnError_WhenListenFails(t *testing.T) {
	srv := newTestServer(WithListener(func(network, addr string) (net.Listener, error) {
		return nil, errors.New("listen failed")
	}))

	err := srv.Start(context.Background())
	require.EqualError(t, err, "listen failed")
}

func TestStart_ShouldContinueAfterAcceptError_AndReturnNilWhenContextIsDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	acceptCalls := 0
	srv := newTestServer(WithListener(func(network, addr string) (net.Listener, error) {
		return &stubListener{
			acceptFn: func() (net.Conn, error) {
				acceptCalls++
				if acceptCalls == 1 {
					cancel()
				}
				return nil, errStubNet
			},
			closeFn: func() error { return nil },
			addrFn:  func() net.Addr { return stubAddr("listener") },
		}, nil
	}))

	err := srv.Start(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, acceptCalls)
}

func TestStart_ShouldContinueAfterAcceptError_WhenContextIsStillActive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	acceptCalls := 0
	conn := &scriptConn{readChunks: [][]byte{{0x00}}}
	srv := newTestServer(WithListener(func(network, addr string) (net.Listener, error) {
		return &stubListener{
			acceptFn: func() (net.Conn, error) {
				acceptCalls++
				switch acceptCalls {
				case 1:
					return nil, errStubNet
				case 2:
					cancel()
					return conn, nil
				default:
					return nil, errStubNet
				}
			},
			closeFn: func() error { return nil },
			addrFn:  func() net.Addr { return stubAddr("listener") },
		}, nil
	}))

	err := srv.Start(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, acceptCalls)
}

func TestStart_ShouldAcceptConnectionAndHandleIt_WhenAcceptSucceeds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &scriptConn{
		readChunks: [][]byte{{0x00}},
		writeErr:   io.ErrClosedPipe,
	}

	acceptCalls := 0
	srv := newTestServer(WithListener(func(network, addr string) (net.Listener, error) {
		return &stubListener{
			acceptFn: func() (net.Conn, error) {
				acceptCalls++
				if acceptCalls == 1 {
					cancel()
					return conn, nil
				}
				return nil, errStubNet
			},
			closeFn: func() error { return nil },
			addrFn:  func() net.Addr { return stubAddr("listener") },
		}, nil
	}))

	err := srv.Start(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, acceptCalls)
}

type stubListener struct {
	acceptFn func() (net.Conn, error)
	closeFn  func() error
	addrFn   func() net.Addr
}

func (l *stubListener) Accept() (net.Conn, error) { return l.acceptFn() }
func (l *stubListener) Close() error              { return l.closeFn() }
func (l *stubListener) Addr() net.Addr            { return l.addrFn() }

type stubAddr string

func (a stubAddr) Network() string { return "tcp" }
func (a stubAddr) String() string  { return string(a) }

type stubNetError struct{}

func (stubNetError) Error() string   { return "temporary accept error" }
func (stubNetError) Timeout() bool   { return false }
func (stubNetError) Temporary() bool { return true }

var errStubNet = stubNetError{}

type scriptConn struct {
	readChunks     [][]byte
	readErr        error
	writes         bytes.Buffer
	writeErr       error
	writeCalls     int
	deadlineWasSet bool
	readIndex      int
}

func (c *scriptConn) Read(p []byte) (int, error) {
	if c.readIndex < len(c.readChunks) {
		chunk := c.readChunks[c.readIndex]
		c.readIndex++
		n := copy(p, chunk)
		return n, nil
	}
	if c.readErr != nil {
		return 0, c.readErr
	}
	return 0, io.EOF
}

func (c *scriptConn) Write(p []byte) (int, error) {
	c.writeCalls++
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	return c.writes.Write(p)
}

func (c *scriptConn) Close() error                     { return nil }
func (c *scriptConn) LocalAddr() net.Addr              { return stubAddr("local") }
func (c *scriptConn) RemoteAddr() net.Addr             { return stubAddr("remote") }
func (c *scriptConn) SetDeadline(time.Time) error      { c.deadlineWasSet = true; return nil }
func (c *scriptConn) SetReadDeadline(time.Time) error  { c.deadlineWasSet = true; return nil }
func (c *scriptConn) SetWriteDeadline(time.Time) error { return nil }

type connectOptions struct {
	clientID     string
	cleanSession bool
	keepAlive    uint16
	will         *protocol.WillMessage
}

type runningServer struct {
	srv    *Server
	addr   string
	ln     net.Listener
	cancel context.CancelFunc
	errCh  chan error
}

func newTestServer(opts ...Option) *Server {
	return New(":0", broker.New(), session.New(), opts...)
}

func startTestServer(t *testing.T, opts ...Option) *runningServer {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	errCh := make(chan error, 1)

	listenerOpt := WithListener(func(network, addr string) (net.Listener, error) {
		return ln, nil
	})

	allOpts := append([]Option{listenerOpt}, opts...)
	rs := &runningServer{
		srv:    New(":0", broker.New(), session.New(), allOpts...),
		addr:   ln.Addr().String(),
		ln:     ln,
		cancel: cancel,
		errCh:  errCh,
	}

	go func() {
		errCh <- rs.srv.Start(ctx)
	}()

	t.Cleanup(func() {
		cancel()
		_ = ln.Close()
		require.NoError(t, <-errCh)
	})

	return rs
}

func openClientConn(t *testing.T, srv *runningServer) net.Conn {
	t.Helper()

	conn, err := net.Dial("tcp", srv.addr)
	require.NoError(t, err)
	return conn
}

func connectClient(t *testing.T, srv *runningServer, opts connectOptions) (net.Conn, bool) {
	t.Helper()

	clientConn := openClientConn(t, srv)
	setDeadline(t, clientConn)
	_, err := clientConn.Write(encodeConnectPacket(t, opts))
	require.NoError(t, err)
	sessionPresent := readConnAck(t, clientConn)

	return clientConn, sessionPresent
}

func subscribe(t *testing.T, conn net.Conn, packetID uint16, filter string, qos byte, wantCodes []byte) {
	t.Helper()

	setDeadline(t, conn)
	_, err := conn.Write(encodeSubscribePacket(t, packetID, filter, qos))
	require.NoError(t, err)

	packet := readPacketBytes(t, conn)
	require.Equal(t, byte(0x90), packet[0])
	require.Equal(t, byte(packetID>>8), packet[2])
	require.Equal(t, byte(packetID), packet[3])
	assert.Equal(t, wantCodes, packet[4:])
}

func writePublish(t *testing.T, conn net.Conn, pkt *protocol.PublishPacket) {
	t.Helper()
	setDeadline(t, conn)
	require.NoError(t, protocol.EncodePublish(conn, pkt))
}

func writePubAck(t *testing.T, conn net.Conn, packetID uint16) {
	t.Helper()
	setDeadline(t, conn)
	require.NoError(t, protocol.Encode(conn, &protocol.PubAckPacket{PacketID: packetID}))
}

func writeDisconnect(t *testing.T, conn net.Conn) {
	t.Helper()
	setDeadline(t, conn)
	_, err := conn.Write([]byte{0xE0, 0x00})
	require.NoError(t, err)
}

func readConnAck(t *testing.T, conn net.Conn) bool {
	t.Helper()
	packet := readPacketBytes(t, conn)
	require.Equal(t, []byte{0x20, 0x02}, packet[:2])
	require.Equal(t, byte(0x00), packet[3])
	return packet[2]&0x01 == 0x01
}

func decodeClientPacket(t *testing.T, conn net.Conn) protocol.Packet {
	t.Helper()
	packet := readPacketBytes(t, conn)
	pkt, err := protocol.Decode(bytes.NewReader(packet))
	require.NoError(t, err)
	return pkt
}

func readPublishWithinPackets(t *testing.T, conn net.Conn, maxPackets int) *protocol.PublishPacket {
	t.Helper()

	for range maxPackets {
		packet := readPacketBytes(t, conn)
		pkt, err := protocol.Decode(bytes.NewReader(packet))
		if err != nil {
			continue
		}

		pub, ok := pkt.(*protocol.PublishPacket)
		if ok {
			return pub
		}
	}

	return nil
}

func readPacketBytes(t *testing.T, conn net.Conn) []byte {
	t.Helper()

	setDeadline(t, conn)

	header := make([]byte, 1)
	_, err := io.ReadFull(conn, header)
	require.NoError(t, err)

	remainingLength, encoded := readRemainingLength(t, conn)
	payload := make([]byte, remainingLength)
	_, err = io.ReadFull(conn, payload)
	require.NoError(t, err)

	packet := make([]byte, 0, 1+len(encoded)+len(payload))
	packet = append(packet, header[0])
	packet = append(packet, encoded...)
	packet = append(packet, payload...)
	return packet
}

func readRemainingLength(t *testing.T, conn net.Conn) (int, []byte) {
	t.Helper()

	multiplier := 1
	value := 0
	encoded := make([]byte, 0, 4)

	for range 4 {
		b := make([]byte, 1)
		_, err := io.ReadFull(conn, b)
		require.NoError(t, err)

		encoded = append(encoded, b[0])
		digit := int(b[0])
		value += (digit & 127) * multiplier
		if digit&128 == 0 {
			return value, encoded
		}

		multiplier *= 128
	}

	t.Fatal("remaining length exceeded 4 bytes")
	return 0, nil
}

func assertNoPacket(t *testing.T, conn net.Conn) {
	t.Helper()

	_ = conn.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	buf := make([]byte, 1)
	_, err := conn.Read(buf)
	require.Error(t, err)

	netErr, ok := err.(net.Error)
	require.True(t, ok)
	assert.True(t, netErr.Timeout())
}

func assertConnectionEventuallyClosed(t *testing.T, conn net.Conn, wait time.Duration) {
	t.Helper()

	require.Eventually(t, func() bool {
		_ = conn.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
		buf := make([]byte, 1)
		_, err := conn.Read(buf)
		if err == nil {
			return false
		}

		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return false
		}

		return true
	}, wait, 50*time.Millisecond)
}

func setDeadline(t *testing.T, conn net.Conn) {
	t.Helper()
	require.NoError(t, conn.SetDeadline(time.Now().Add(2*time.Second)))
}

func encodeConnectPacket(t *testing.T, opts connectOptions) []byte {
	t.Helper()

	var payload bytes.Buffer
	writeUTF8(t, &payload, opts.clientID)

	var flags byte
	if opts.cleanSession {
		flags |= 0x02
	}
	if opts.will != nil {
		flags |= 0x04
		flags |= (opts.will.QoS & 0x03) << 3
		if opts.will.Retain {
			flags |= 0x20
		}
		writeUTF8(t, &payload, opts.will.Topic)
		writeBinary(t, &payload, opts.will.Payload)
	}

	var variable bytes.Buffer
	writeUTF8(t, &variable, "MQTT")
	require.NoError(t, variable.WriteByte(0x04))
	require.NoError(t, variable.WriteByte(flags))
	keepAlive := opts.keepAlive
	if keepAlive == 0 {
		keepAlive = 60
	}
	require.NoError(t, binary.Write(&variable, binary.BigEndian, keepAlive))

	remainingLength := variable.Len() + payload.Len()

	var packet bytes.Buffer
	require.NoError(t, packet.WriteByte(0x10))
	writeRemainingLength(t, &packet, remainingLength)
	_, err := packet.Write(variable.Bytes())
	require.NoError(t, err)
	_, err = packet.Write(payload.Bytes())
	require.NoError(t, err)

	return packet.Bytes()
}

func encodeSubscribePacket(t *testing.T, packetID uint16, filter string, qos byte) []byte {
	t.Helper()

	var payload bytes.Buffer
	require.NoError(t, binary.Write(&payload, binary.BigEndian, packetID))
	writeUTF8(t, &payload, filter)
	require.NoError(t, payload.WriteByte(qos))

	var packet bytes.Buffer
	require.NoError(t, packet.WriteByte(0x82))
	writeRemainingLength(t, &packet, payload.Len())
	_, err := packet.Write(payload.Bytes())
	require.NoError(t, err)

	return packet.Bytes()
}

func encodeUnsubscribePacketForTest(t *testing.T, packetID uint16, filter string) []byte {
	t.Helper()

	var payload bytes.Buffer
	require.NoError(t, binary.Write(&payload, binary.BigEndian, packetID))
	writeUTF8(t, &payload, filter)

	var packet bytes.Buffer
	require.NoError(t, packet.WriteByte(0xA2))
	writeRemainingLength(t, &packet, payload.Len())
	_, err := packet.Write(payload.Bytes())
	require.NoError(t, err)

	return packet.Bytes()
}

func writeUTF8(t *testing.T, w io.Writer, value string) {
	t.Helper()
	require.NoError(t, binary.Write(w, binary.BigEndian, uint16(len(value))))
	_, err := w.Write([]byte(value))
	require.NoError(t, err)
}

func writeBinary(t *testing.T, w io.Writer, value []byte) {
	t.Helper()
	require.NoError(t, binary.Write(w, binary.BigEndian, uint16(len(value))))
	_, err := w.Write(value)
	require.NoError(t, err)
}

func writeRemainingLength(t *testing.T, w io.Writer, length int) {
	t.Helper()

	for {
		encodedByte := length % 128
		length /= 128
		if length > 0 {
			encodedByte |= 0x80
		}
		_, err := w.Write([]byte{byte(encodedByte)})
		require.NoError(t, err)
		if length == 0 {
			return
		}
	}
}

func exhaustPacketIDs(t *testing.T, sess *session.State) {
	t.Helper()

	for i := 0; i < 65535; i++ {
		_, err := sess.TrackOutbound(&protocol.PublishPacket{
			Topic:   "exhaust/topic",
			Payload: []byte("x"),
			QoS:     1,
		})
		require.NoError(t, err)
	}
}
