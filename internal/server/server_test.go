package server

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/lucasmendoncca/OrbMQ/internal/broker"
	"github.com/lucasmendoncca/OrbMQ/internal/protocol"
	"github.com/lucasmendoncca/OrbMQ/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStart_ShouldEnforcePacketSize_WhenConfiguredLimitIsExceeded(t *testing.T) {
	for _, tc := range []struct {
		name         string
		afterConnect bool
	}{
		{"connect", false}, {"publish", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				_, connections, stop := startPipeTestServer(t, WithMaxPacketSize(64))
				defer stop()
				serverConn, conn := net.Pipe()
				defer conn.Close()
				connections <- serverConn
				setDeadline(t, conn)
				if tc.afterConnect {
					_, err := conn.Write(encodeConnectPacket(t, connectOptions{clientID: "limited", cleanSession: true}))
					require.NoError(t, err)
					readConnAck(t, conn)
				}
				header := byte(0x10)
				if tc.afterConnect {
					header = 0x30
				}
				// No body is sent: the declared size must suffice to reject the packet.
				_, err := conn.Write([]byte{header, 63})
				require.NoError(t, err)
				synctest.Wait()
				_, err = conn.Read(make([]byte, 1))
				require.ErrorIs(t, err, io.EOF)
			})
		})
	}
}

func TestStart_ShouldRejectPacketSizeOption_WhenLimitIsInvalid(t *testing.T) {
	for _, limit := range []int{0, -1, protocol.MaxPacketSize + 1} {
		srv := newTestServer(WithMaxPacketSize(limit), WithListener(func(string, string) (net.Listener, error) {
			t.Fatal("invalid configuration must fail before listening")
			return nil, nil
		}))
		require.Error(t, srv.Start(context.Background()))
	}
}

func TestStart_ShouldMatchRetainedAndLiveMessagesConsistently_WhenFiltersUseWildcards(t *testing.T) {
	for _, tc := range []struct {
		filter string
		topic  string
		match  bool
	}{
		{"a/#", "a", true}, {"a/+", "a/", true}, {"a", "a/", false},
		{"#", "$SYS/status", false}, {"+/status", "$SYS/status", false},
		{"$SYS/#", "$SYS/status", true},
	} {
		t.Run(tc.filter+":"+tc.topic, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				_, connections, stop := startPipeTestServer(t)
				defer stop()
				publisher, _ := connectPipeClient(t, connections, "publisher")
				defer publisher.Close()
				writePublish(t, publisher, &protocol.PublishPacket{Topic: tc.topic, Payload: []byte("retained"), Retain: true, QoS: 1, PacketID: 1})
				assert.Equal(t, []byte{0x40, 2, 0, 1}, readPacketBytes(t, publisher))
				subscriber, _ := connectPipeClient(t, connections, "subscriber")
				defer subscriber.Close()
				_, err := subscriber.Write(encodeSubscribePacket(t, 1, tc.filter, 0))
				require.NoError(t, err)
				if tc.match {
					packet := decodeClientPacket(t, subscriber)
					pub, ok := packet.(*protocol.PublishPacket)
					require.True(t, ok)
					assert.True(t, pub.Retain)
					assert.Equal(t, []byte("retained"), pub.Payload)
				}
				assert.Equal(t, []byte{0x90, 3, 0, 1, 0}, readPacketBytes(t, subscriber))
				writePublish(t, publisher, &protocol.PublishPacket{Topic: tc.topic, Payload: []byte("live"), QoS: 1, PacketID: 2})
				assert.Equal(t, []byte{0x40, 2, 0, 2}, readPacketBytes(t, publisher))
				if tc.match {
					packet := decodeClientPacket(t, subscriber)
					pub, ok := packet.(*protocol.PublishPacket)
					require.True(t, ok)
					assert.Equal(t, []byte("live"), pub.Payload)
				} else {
					assertNoPacket(t, subscriber)
				}
			})
		})
	}
}

func TestStart_ShouldWaitForWriterExit_WhenContextIsCancelled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, connections, stop := startPipeTestServer(t)
		serverConn, conn := net.Pipe()
		defer conn.Close()
		writeReturned := make(chan struct{})
		releaseWriter := make(chan struct{})
		unblockWriter := sync.OnceFunc(func() { close(releaseWriter) })
		defer unblockWriter()
		connections <- &delayedWriteExitConn{Conn: serverConn, returned: writeReturned, release: releaseWriter}
		setDeadline(t, conn)
		_, err := conn.Write(encodeConnectPacket(t, connectOptions{clientID: "blocked", cleanSession: true}))
		require.NoError(t, err)
		synctest.Wait()
		stopped := make(chan struct{})
		go func() { stop(); close(stopped) }()
		<-writeReturned
		synctest.Wait()
		select {
		case <-stopped:
			t.Error("server returned while a connection writer was still running")
		default:
		}
		unblockWriter()
		<-stopped
	})
}

func TestStart_ShouldWaitForPreviousWriter_WhenClientIDIsReplaced(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, connections, stop := startPipeTestServer(t)
		defer stop()
		writeReturned := make(chan struct{})
		releaseWriter := make(chan struct{})
		unblockWriter := sync.OnceFunc(func() { close(releaseWriter) })
		defer unblockWriter()
		serverConn, old := net.Pipe()
		defer old.Close()
		connections <- &delayedWriteExitConn{Conn: serverConn, returned: writeReturned, release: releaseWriter}
		setDeadline(t, old)
		_, err := old.Write(encodeConnectPacket(t, connectOptions{clientID: "same-id", cleanSession: true}))
		require.NoError(t, err)
		readConnAck(t, old)
		subscribe(t, old, 1, "takeover/#", 0, []byte{0})
		pub, _ := connectPipeClient(t, connections, "publisher")
		defer pub.Close()
		writePublish(t, pub, &protocol.PublishPacket{Topic: "takeover/value", Payload: []byte("pending"), QoS: 1, PacketID: 7})
		_ = decodeClientPacket(t, pub)
		synctest.Wait()
		newServer, replacement := net.Pipe()
		defer replacement.Close()
		observed := &observedWriteConn{Conn: newServer, writeGate: make(chan struct{}, 1)}
		connections <- observed
		setDeadline(t, replacement)
		_, err = replacement.Write(encodeConnectPacket(t, connectOptions{clientID: "same-id", cleanSession: true}))
		require.NoError(t, err)
		<-writeReturned
		synctest.Wait()
		assert.Zero(t, observed.writing.Load(), "replacement must wait for the previous writer")
		writePublish(t, pub, &protocol.PublishPacket{Topic: "takeover/value", Payload: []byte("during handoff"), QoS: 1, PacketID: 9})
		assert.Equal(t, []byte{0x40, 0x02, 0x00, 0x09}, readPacketBytes(t, pub))
		unblockWriter()
		assert.False(t, readConnAck(t, replacement))
		subscribe(t, replacement, 2, "takeover/#", 0, []byte{0})
		writePublish(t, pub, &protocol.PublishPacket{Topic: "takeover/value", Payload: []byte("new owner"), QoS: 1, PacketID: 8})
		_ = decodeClientPacket(t, pub)
		packet := decodeClientPacket(t, replacement).(*protocol.PublishPacket)
		assert.Equal(t, []byte("new owner"), packet.Payload)
	})
}

func TestStart_ShouldStopAccept_WhenContextIsCancelled(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	acceptStarted := make(chan struct{})
	srv := newTestServer(WithListener(func(string, string) (net.Listener, error) {
		return &stubListener{
			acceptFn: func() (net.Conn, error) { close(acceptStarted); return ln.Accept() },
			closeFn:  ln.Close,
			addrFn:   ln.Addr,
		}, nil
	}))
	result := make(chan error, 1)
	go func() { result <- srv.Start(ctx) }()
	<-acceptStarted
	cancel()
	select {
	case err := <-result:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("server did not interrupt accept on cancellation")
	}
}

func TestStart_ShouldClearHandshakeDeadline_WhenKeepAliveIsZero(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, connections, stop := startPipeTestServer(t, WithHandshakeTimeout(time.Second))
		defer stop()
		conn, _ := connectPipeClient(t, connections, "no-keepalive")
		defer conn.Close()
		time.Sleep(2 * time.Second)
		setDeadline(t, conn)
		_, err := conn.Write([]byte{0xC0, 0x00})
		require.NoError(t, err)
		assert.Equal(t, []byte{0xD0, 0x00}, readPacketBytes(t, conn))
	})
}

func TestStart_ShouldPreserveNewOwnerResources_WhenClientIDIsReplaced(t *testing.T) {
	testCases := []struct {
		name     string
		oldClean bool
		newClean bool
	}{
		{name: "persistent to persistent"},
		{name: "persistent to clean", newClean: true},
		{name: "clean to persistent", oldClean: true},
		{name: "clean to clean", oldClean: true, newClean: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				_, connections, stop := startPipeTestServer(t)
				defer stop()
				oldServer, old := net.Pipe()
				defer old.Close()
				connections <- oldServer
				setDeadline(t, old)
				_, err := old.Write(encodeConnectPacket(t, connectOptions{clientID: "same-id", cleanSession: tc.oldClean}))
				require.NoError(t, err)
				assert.False(t, readConnAck(t, old))
				subscribe(t, old, 1, "takeover/#", 1, []byte{1})
				publisher, _ := connectPipeClient(t, connections, "publisher")
				defer publisher.Close()
				writePublish(t, publisher, &protocol.PublishPacket{Topic: "takeover/value", Payload: []byte("pending"), QoS: 1, PacketID: 7})
				_ = decodeClientPacket(t, publisher)
				pending := decodeClientPacket(t, old)

				newServer, replacement := net.Pipe()
				defer replacement.Close()
				connections <- newServer
				setDeadline(t, replacement)
				_, err = replacement.Write(encodeConnectPacket(t, connectOptions{clientID: "same-id", cleanSession: tc.newClean}))
				require.NoError(t, err)
				resume := !tc.oldClean && !tc.newClean
				assert.Equal(t, resume, readConnAck(t, replacement))
				if resume {
					replayed := decodeClientPacket(t, replacement).(*protocol.PublishPacket)
					assert.Equal(t, pending.(*protocol.PublishPacket).PacketID, replayed.PacketID)
					assert.Equal(t, []byte("pending"), replayed.Payload)
					assert.True(t, replayed.DUP)
				} else {
					subscribe(t, replacement, 2, "takeover/#", 1, []byte{1})
				}
				_, err = old.Read(make([]byte, 1))
				assert.ErrorIs(t, err, io.EOF)
				_ = old.Close()
				synctest.Wait()
				_, err = replacement.Write([]byte{0xC0, 0x00})
				require.NoError(t, err)
				assert.Equal(t, []byte{0xD0, 0x00}, readPacketBytes(t, replacement))
				writePublish(t, publisher, &protocol.PublishPacket{Topic: "takeover/value", Payload: []byte("new owner"), QoS: 1, PacketID: 8})
				_ = decodeClientPacket(t, publisher)
				delivered := decodeClientPacket(t, replacement).(*protocol.PublishPacket)
				assert.Equal(t, []byte("new owner"), delivered.Payload)
				assert.False(t, delivered.DUP)
			})
		})
	}
}

func TestStart_ShouldCloseConnection_WhenHandshakeTimesOut(t *testing.T) {
	testCases := []struct {
		name    string
		request func(*testing.T) []byte
		timeout time.Duration
	}{
		{name: "silent peer", request: func(*testing.T) []byte { return nil }},
		{name: "partial header", request: func(*testing.T) []byte { return []byte{0x10, 0x80} }},
		{name: "partial body", request: func(*testing.T) []byte { return []byte{0x10, 0x0C, 0x00, 0x04} }},
		{name: "blocked connack", request: func(t *testing.T) []byte {
			return encodeConnectPacket(t, connectOptions{clientID: "blocked", cleanSession: true})
		}},
		{name: "configured timeout", timeout: time.Second, request: func(*testing.T) []byte { return nil }},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				timeout := 10 * time.Second
				var opts []Option
				if tc.timeout > 0 {
					timeout = tc.timeout
					opts = append(opts, WithHandshakeTimeout(timeout))
				}
				_, connections, stop := startPipeTestServer(t, opts...)
				defer stop()
				serverConn, conn := net.Pipe()
				defer conn.Close()
				connections <- serverConn
				require.NoError(t, conn.SetDeadline(time.Now().Add(timeout+time.Second)))
				if request := tc.request(t); len(request) > 0 {
					_, err := conn.Write(request)
					require.NoError(t, err)
				}
				time.Sleep(timeout)
				synctest.Wait()
				_, err := conn.Read(make([]byte, 1))
				require.ErrorIs(t, err, io.EOF)
			})
		})
	}
}

func TestStart_ShouldCloseAllConnections_WhenContextIsCancelled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, connections, stop := startPipeTestServer(t)
		idleServer, idle := net.Pipe()
		defer idle.Close()
		setDeadline(t, idle)
		connections <- idleServer
		blockedServer, blocked := net.Pipe()
		defer blocked.Close()
		connections <- blockedServer
		setDeadline(t, blocked)
		_, err := blocked.Write(encodeConnectPacket(t, connectOptions{clientID: "blocked", cleanSession: true}))
		require.NoError(t, err)
		connected, _ := connectPipeClient(t, connections, "connected")
		defer connected.Close()
		synctest.Wait()
		stop()
		for _, conn := range []net.Conn{idle, blocked, connected} {
			_, err := conn.Read(make([]byte, 1))
			assert.ErrorIs(t, err, io.EOF)
		}
	})
}

func TestStart_ShouldSerializeResponsesAndDeliveries_WhenSubscriberWriteIsBlocked(t *testing.T) {
	testCases := []struct {
		name     string
		request  func(*testing.T) []byte
		response []byte
	}{
		{name: "ping", request: func(*testing.T) []byte { return []byte{0xC0, 0x00} }, response: []byte{0xD0, 0x00}},
		{name: "subscribe", request: func(t *testing.T) []byte {
			return encodeSubscribePacket(t, 2, "other/#", 0)
		}, response: []byte{0x90, 0x03, 0x00, 0x02, 0x00}},
		{name: "unsubscribe", request: func(t *testing.T) []byte {
			return encodeUnsubscribePacketForTest(t, 3, "sensors/#")
		}, response: []byte{0xB0, 0x02, 0x00, 0x03}},
		{name: "publish", request: func(t *testing.T) []byte {
			var buf bytes.Buffer
			require.NoError(t, protocol.EncodePublish(&buf, &protocol.PublishPacket{
				Topic: "other/value", Payload: []byte("value"), QoS: 1, PacketID: 4,
			}))
			return buf.Bytes()
		}, response: []byte{0x40, 0x02, 0x00, 0x04}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				_, connections, stop := startPipeTestServer(t)
				defer stop()
				sub, observed := connectPipeClient(t, connections, "slow-sub")
				defer sub.Close()
				subscribe(t, sub, 1, "sensors/#", 0, []byte{0})
				pub, _ := connectPipeClient(t, connections, "publisher")
				defer pub.Close()

				writePublish(t, pub, &protocol.PublishPacket{
					Topic: "sensors/temp", Payload: []byte("pending"), QoS: 1, PacketID: 7,
				})
				assert.Equal(t, []byte{0x40, 0x02, 0x00, 0x07}, readPacketBytes(t, pub))
				synctest.Wait()
				require.Equal(t, int32(1), observed.writing.Load())

				_, err := sub.Write(tc.request(t))
				require.NoError(t, err)
				synctest.Wait()
				assert.False(t, observed.concurrent.Load(), "responses must use the same writer as deliveries")

				packet := decodeClientPacket(t, sub)
				delivery, ok := packet.(*protocol.PublishPacket)
				require.True(t, ok)
				assert.Equal(t, []byte("pending"), delivery.Payload)
				assert.Equal(t, tc.response, readPacketBytes(t, sub))
			})
		})
	}
}

func TestStart_ShouldSendConnAckThenReplayBeforeLiveDeliveries_WhenSessionReconnects(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		replayStarted := make(chan struct{})
		releaseReplay := make(chan struct{})
		unblockReplay := sync.OnceFunc(func() { close(releaseReplay) })
		srv, connections, stop := startPipeTestServer(t, WithPublishEncoder(func(pub *protocol.PublishPacket) ([]byte, error) {
			if pub.DUP && string(pub.Payload) == "first" {
				close(replayStarted)
				<-releaseReplay
			}
			return encodePublishPacket(pub)
		}))
		defer stop()
		defer unblockReplay()
		state, _ := srv.sessions.GetOrCreate("persistent-sub")
		state.SetSubscription("sensors/#", 1)
		for _, payload := range []string{"first", "second"} {
			_, err := state.TrackOutbound(&protocol.PublishPacket{Topic: "sensors/temp", Payload: []byte(payload), QoS: 1})
			require.NoError(t, err)
		}
		pub, _ := connectPipeClient(t, connections, "publisher")
		defer pub.Close()
		serverConn, sub := net.Pipe()
		defer sub.Close()
		observed := &observedWriteConn{Conn: serverConn, writeGate: make(chan struct{}, 1)}
		connections <- observed
		setDeadline(t, sub)
		_, err := sub.Write(encodeConnectPacket(t, connectOptions{clientID: "persistent-sub"}))
		require.NoError(t, err)
		synctest.Wait()

		writePublish(t, pub, &protocol.PublishPacket{Topic: "sensors/temp", Payload: []byte("during connect"), QoS: 1, PacketID: 7})
		_ = decodeClientPacket(t, pub)
		synctest.Wait()
		assert.False(t, observed.concurrent.Load())
		assert.True(t, readConnAck(t, sub))
		<-replayStarted
		writePublish(t, pub, &protocol.PublishPacket{Topic: "sensors/temp", Payload: []byte("during replay"), QoS: 1, PacketID: 8})
		_ = decodeClientPacket(t, pub)
		unblockReplay()

		for i, payload := range []string{"first", "second"} {
			packet := decodeClientPacket(t, sub)
			replayed, ok := packet.(*protocol.PublishPacket)
			require.True(t, ok)
			assert.Equal(t, payload, string(replayed.Payload))
			assert.Equal(t, uint16(i+1), replayed.PacketID)
			assert.True(t, replayed.DUP)
		}
		_, err = sub.Write([]byte{0xC0, 0x00})
		require.NoError(t, err)
		assert.Equal(t, []byte{0xD0, 0x00}, readPacketBytes(t, sub))
		writePublish(t, pub, &protocol.PublishPacket{Topic: "sensors/temp", Payload: []byte("live"), QoS: 1, PacketID: 9})
		_ = decodeClientPacket(t, pub)
		packet := decodeClientPacket(t, sub)
		live, ok := packet.(*protocol.PublishPacket)
		require.True(t, ok)
		assert.Equal(t, []byte("live"), live.Payload)
		assert.False(t, live.DUP)
	})
}

func TestStart_ShouldCloseConnection_WhenResponseQueueIsFull(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, connections, stop := startPipeTestServer(t)
		defer stop()
		sub, _ := connectPipeClient(t, connections, "slow-sub")
		defer sub.Close()
		subscribe(t, sub, 1, "sensors/#", 0, []byte{0})
		pub, _ := connectPipeClient(t, connections, "publisher")
		defer pub.Close()
		// One delivery blocks the writer; the next 1024 fill the bounded queue.
		for i := range 1025 {
			writePublish(t, pub, &protocol.PublishPacket{
				Topic: "sensors/temp", Payload: []byte("queued"), QoS: 1, PacketID: uint16(i + 1),
			})
			_ = decodeClientPacket(t, pub)
			if i == 0 {
				synctest.Wait()
			}
		}
		_, err := sub.Write([]byte{0xC0, 0x00})
		require.NoError(t, err)
		synctest.Wait()
		_, err = sub.Read(make([]byte, 1))
		require.ErrorIs(t, err, io.EOF)
		_, err = pub.Write([]byte{0xC0, 0x00})
		require.NoError(t, err)
		assert.Equal(t, []byte{0xD0, 0x00}, readPacketBytes(t, pub))
	})
}

func TestStart_ShouldPreservePendingMessages_WhenReplayQueueIsFull(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, connections, stop := startPipeTestServer(t)
		defer stop()
		state, _ := srv.sessions.GetOrCreate("persistent-sub")
		for range 1026 {
			_, err := state.TrackOutbound(&protocol.PublishPacket{Topic: "sensors/temp", Payload: []byte("pending"), QoS: 1})
			require.NoError(t, err)
		}
		serverConn, conn := net.Pipe()
		defer conn.Close()
		connections <- serverConn
		setDeadline(t, conn)
		_, err := conn.Write(encodeConnectPacket(t, connectOptions{clientID: "persistent-sub"}))
		require.NoError(t, err)
		assert.True(t, readConnAck(t, conn))
		synctest.Wait()
		_, err = conn.Read(make([]byte, 1))
		require.ErrorIs(t, err, io.EOF)
		assert.Len(t, state.ReplayPending(), 1026)
	})
}

func TestStart_ShouldProcessPacketsInOrder_WhenStreamIsConcatenatedOrFragmented(t *testing.T) {
	testCases := []struct {
		name           string
		afterHandshake bool
		fragmented     bool
	}{
		{name: "single write including connect"},
		{name: "single write after handshake", afterHandshake: true},
		{name: "one byte per write", fragmented: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			srv := startTestServer(t)
			opts := connectOptions{clientID: "stream-client", cleanSession: true}
			var conn net.Conn
			var stream bytes.Buffer
			var responses [][]byte
			if tc.afterHandshake {
				conn, _ = connectClient(t, srv, opts)
			} else {
				conn = openClientConn(t, srv)
				stream.Write(encodeConnectPacket(t, opts))
				responses = append(responses, []byte{0x20, 0x02, 0x00, 0x00})
			}
			defer conn.Close()

			require.NoError(t, protocol.EncodePublish(&stream, &protocol.PublishPacket{
				Topic: "stream/value", Payload: bytes.Repeat([]byte("x"), 128), QoS: 1, PacketID: 7, Retain: true,
			}))
			stream.Write([]byte{0xC0, 0x00})
			require.NoError(t, protocol.EncodePublish(&stream, &protocol.PublishPacket{
				Topic: "stream/value", Payload: []byte("last"), QoS: 1, PacketID: 9, Retain: true,
			}))
			stream.Write([]byte{0xE0, 0x00})
			responses = append(responses, []byte{0x40, 0x02, 0x00, 0x07}, []byte{0xD0, 0x00}, []byte{0x40, 0x02, 0x00, 0x09})

			setDeadline(t, conn)
			if tc.fragmented {
				for _, b := range stream.Bytes() {
					n, err := conn.Write([]byte{b})
					require.NoError(t, err)
					require.Equal(t, 1, n)
				}
			} else {
				n, err := conn.Write(stream.Bytes())
				require.NoError(t, err)
				require.Equal(t, stream.Len(), n)
			}

			for _, expected := range responses {
				assert.Equal(t, expected, readPacketBytes(t, conn))
			}
			retained := srv.srv.broker.RetainedMatching("stream/value")
			require.Len(t, retained, 1)
			assert.Equal(t, []byte("last"), retained[0].Payload)
			assertConnectionEventuallyClosed(t, conn, 2*time.Second)
		})
	}
}

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

	assertConnectionEventuallyClosed(t, conn, time.Second)
}

func TestStart_ShouldCloseConnection_WhenConnectPacketIsMalformed(t *testing.T) {
	srv := startTestServer(t)

	conn := openClientConn(t, srv)
	defer conn.Close()

	setDeadline(t, conn)
	_, err := conn.Write([]byte{0x10, 0x01, 0x00})
	require.NoError(t, err)

	assertConnectionEventuallyClosed(t, conn, time.Second)
}

func TestStart_ShouldAttemptConnAck_WhenAcceptedConnectionWriteFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &scriptConn{
		readChunks: [][]byte{encodeConnectPacket(t, connectOptions{clientID: "connack-fail", cleanSession: true})},
		writeErr:   io.ErrClosedPipe,
		onWrite:    cancel,
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
				return conn, nil
			},
			closeFn: func() error { return nil },
			addrFn:  func() net.Addr { return stubAddr("listener") },
		}, nil
	}))

	err := srv.Start(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, conn.writeCalls.Load(), int32(1))
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

func TestStart_ShouldEncodeQoS0PublishOnlyOnce_WhenMultipleSubscribersMatch(t *testing.T) {
	var encodeCalls atomic.Int32

	srv := startTestServer(t, WithPublishEncoder(func(pub *protocol.PublishPacket) ([]byte, error) {
		encodeCalls.Add(1)
		return encodePublishPacket(pub)
	}))

	subConnA, _ := connectClient(t, srv, connectOptions{clientID: "encode-once-sub-a", cleanSession: true})
	defer subConnA.Close()
	subscribe(t, subConnA, 1, "shared/+", 0, []byte{0})

	subConnB, _ := connectClient(t, srv, connectOptions{clientID: "encode-once-sub-b", cleanSession: true})
	defer subConnB.Close()
	subscribe(t, subConnB, 1, "shared/+", 0, []byte{0})

	pubConn, _ := connectClient(t, srv, connectOptions{clientID: "encode-once-pub", cleanSession: true})
	defer pubConn.Close()

	writePublish(t, pubConn, &protocol.PublishPacket{
		Topic:   "shared/topic",
		Payload: []byte("value"),
	})

	deliveredA := decodeClientPacket(t, subConnA)
	deliveredPubA, ok := deliveredA.(*protocol.PublishPacket)
	require.True(t, ok)
	assert.Equal(t, []byte("value"), deliveredPubA.Payload)
	assert.Equal(t, byte(0), deliveredPubA.QoS)

	deliveredB := decodeClientPacket(t, subConnB)
	deliveredPubB, ok := deliveredB.(*protocol.PublishPacket)
	require.True(t, ok)
	assert.Equal(t, []byte("value"), deliveredPubB.Payload)
	assert.Equal(t, byte(0), deliveredPubB.QoS)

	assert.Equal(t, int32(1), encodeCalls.Load())
}

func TestStart_ShouldEncodeQoS1PublishOnlyOnce_WhenMultipleSubscribersMatch(t *testing.T) {
	var encodeCalls atomic.Int32

	srv := startTestServer(t, WithPublishEncoder(func(pub *protocol.PublishPacket) ([]byte, error) {
		encodeCalls.Add(1)
		return encodePublishPacket(pub)
	}))

	subConnA, _ := connectClient(t, srv, connectOptions{clientID: "encode-qos1-sub-a", cleanSession: false})
	defer subConnA.Close()
	subscribe(t, subConnA, 1, "tracked/+", 1, []byte{1})

	subConnB, _ := connectClient(t, srv, connectOptions{clientID: "encode-qos1-sub-b", cleanSession: false})
	defer subConnB.Close()
	subscribe(t, subConnB, 1, "tracked/+", 1, []byte{1})

	pubConn, _ := connectClient(t, srv, connectOptions{clientID: "encode-qos1-pub", cleanSession: true})
	defer pubConn.Close()

	writePublish(t, pubConn, &protocol.PublishPacket{
		Topic:    "tracked/topic",
		Payload:  []byte("value"),
		QoS:      1,
		PacketID: 41,
	})

	_ = decodeClientPacket(t, pubConn)

	deliveredA := decodeClientPacket(t, subConnA)
	deliveredPubA, ok := deliveredA.(*protocol.PublishPacket)
	require.True(t, ok)
	assert.Equal(t, byte(1), deliveredPubA.QoS)
	assert.NotZero(t, deliveredPubA.PacketID)

	deliveredB := decodeClientPacket(t, subConnB)
	deliveredPubB, ok := deliveredB.(*protocol.PublishPacket)
	require.True(t, ok)
	assert.Equal(t, byte(1), deliveredPubB.QoS)
	assert.NotZero(t, deliveredPubB.PacketID)

	assert.Equal(t, int32(1), encodeCalls.Load())
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

func TestStart_ShouldReturnError_WhenHandshakeTimeoutIsNotPositive(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second} {
		t.Run(timeout.String(), func(t *testing.T) {
			srv := newTestServer(WithHandshakeTimeout(timeout), WithListener(func(string, string) (net.Listener, error) {
				t.Fatal("invalid configuration must fail before listening")
				return nil, nil
			}))
			require.EqualError(t, srv.Start(context.Background()), "handshake timeout must be positive")
		})
	}
}

func TestStart_ShouldCloseConnections_WhenAcceptFailsPermanently(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		serverConn, conn := net.Pipe()
		defer conn.Close()
		acceptErr := errors.New("accept failed")
		accepted := false
		srv := newTestServer(WithListener(func(string, string) (net.Listener, error) {
			return &stubListener{
				acceptFn: func() (net.Conn, error) {
					if !accepted {
						accepted = true
						return serverConn, nil
					}
					return nil, acceptErr
				},
				closeFn: func() error { return nil },
				addrFn:  func() net.Addr { return stubAddr("pipe") },
			}, nil
		}))
		require.ErrorIs(t, srv.Start(context.Background()), acceptErr)
		_, err := conn.Read(make([]byte, 1))
		require.ErrorIs(t, err, io.EOF)
	})
}

func TestStart_ShouldCloseAcceptedConnection_WhenCancellationWinsAccept(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		serverConn, conn := net.Pipe()
		defer conn.Close()
		srv := newTestServer(WithListener(func(string, string) (net.Listener, error) {
			return &stubListener{
				acceptFn: func() (net.Conn, error) { cancel(); return serverConn, nil },
				closeFn:  func() error { return nil },
				addrFn:   func() net.Addr { return stubAddr("pipe") },
			}, nil
		}))
		require.NoError(t, srv.Start(ctx))
		_, err := conn.Read(make([]byte, 1))
		require.ErrorIs(t, err, io.EOF)
	})
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
	conn := &scriptConn{readChunks: [][]byte{{0x00}}, onClose: cancel}
	srv := newTestServer(WithListener(func(network, addr string) (net.Listener, error) {
		return &stubListener{
			acceptFn: func() (net.Conn, error) {
				acceptCalls++
				switch acceptCalls {
				case 1:
					return nil, errStubNet
				case 2:
					return conn, nil
				default:
					<-ctx.Done()
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
	assert.Positive(t, conn.readIndex)
}

func TestStart_ShouldAcceptConnectionAndHandleIt_WhenAcceptSucceeds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := &scriptConn{
		readChunks: [][]byte{{0x00}},
		writeErr:   io.ErrClosedPipe,
		onClose:    cancel,
	}

	acceptCalls := 0
	srv := newTestServer(WithListener(func(network, addr string) (net.Listener, error) {
		return &stubListener{
			acceptFn: func() (net.Conn, error) {
				acceptCalls++
				if acceptCalls == 1 {
					return conn, nil
				}
				<-ctx.Done()
				return nil, errStubNet
			},
			closeFn: func() error { return nil },
			addrFn:  func() net.Addr { return stubAddr("listener") },
		}, nil
	}))

	err := srv.Start(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, acceptCalls)
	assert.Positive(t, conn.readIndex)
}

// Pipe connections make blocked writes and concurrent handlers deterministic in synctest.
func startPipeTestServer(t *testing.T, opts ...Option) (*Server, chan<- net.Conn, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	connections := make(chan net.Conn)
	listener := &stubListener{
		acceptFn: func() (net.Conn, error) {
			select {
			case conn := <-connections:
				return conn, nil
			case <-ctx.Done():
				return nil, net.ErrClosed
			}
		},
		closeFn: func() error { return nil },
		addrFn:  func() net.Addr { return stubAddr("pipe") },
	}
	allOpts := append([]Option{WithListener(func(string, string) (net.Listener, error) { return listener, nil })}, opts...)
	srv := newTestServer(allOpts...)
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start(ctx) }()
	return srv, connections, func() {
		cancel()
		require.NoError(t, <-errCh)
	}
}

func connectPipeClient(t *testing.T, connections chan<- net.Conn, id string) (net.Conn, *observedWriteConn) {
	t.Helper()
	serverConn, conn := net.Pipe()
	observed := &observedWriteConn{Conn: serverConn, writeGate: make(chan struct{}, 1)}
	connections <- observed
	setDeadline(t, conn)
	_, err := conn.Write(encodeConnectPacket(t, connectOptions{clientID: id, cleanSession: true}))
	require.NoError(t, err)
	readConnAck(t, conn)
	return conn, observed
}

// Delay return after network cancellation to verify the server joins its writer.
type delayedWriteExitConn struct {
	net.Conn
	returned chan<- struct{}
	release  <-chan struct{}
}

func (c *delayedWriteExitConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if err != nil {
		close(c.returned)
		<-c.release
	}
	return n, err
}

type observedWriteConn struct {
	net.Conn
	writing    atomic.Int32
	concurrent atomic.Bool
	writeGate  chan struct{}
}

func (c *observedWriteConn) Write(p []byte) (int, error) {
	if c.writing.Add(1) > 1 {
		c.concurrent.Store(true)
	}
	defer c.writing.Add(-1)
	// A channel gate keeps competing writes durably blocked for synctest.Wait.
	c.writeGate <- struct{}{}
	defer func() { <-c.writeGate }()
	return c.Conn.Write(p)
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
	writeCalls     atomic.Int32
	deadlineWasSet bool
	readIndex      int
	onWrite        func()
	onClose        func()
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
	c.writeCalls.Add(1)
	if c.onWrite != nil {
		c.onWrite()
	}
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	return c.writes.Write(p)
}

func (c *scriptConn) Close() error {
	if c.onClose != nil {
		c.onClose()
	}
	return nil
}
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
	require.NoError(t, binary.Write(&variable, binary.BigEndian, opts.keepAlive))

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
