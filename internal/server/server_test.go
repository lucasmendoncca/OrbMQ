package server

import (
	"bytes"
	"context"
	"encoding/binary"
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

func TestHandleConn_ShouldDeliverQoS1PublishAndReplyPubAck_WhenSubscriberRequestsQoS1(t *testing.T) {
	srv := newTestServer()

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

func TestHandleConn_ShouldDowngradeDeliveryQoS_WhenSubscriptionQoSIsLowerThanPublish(t *testing.T) {
	srv := newTestServer()

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

func TestHandleConn_ShouldReplayInflightPublishWithDup_WhenPersistentSessionReconnects(t *testing.T) {
	srv := newTestServer()

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

func TestHandleConn_ShouldDeliverRetainedPublishWithQoS1_WhenSubscriberRequestsQoS1(t *testing.T) {
	srv := newTestServer()

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
	subscribe(t, subConn, 1, "retained/#", 1, []byte{1})

	retained := decodeClientPacket(t, subConn)
	pub, ok := retained.(*protocol.PublishPacket)
	require.True(t, ok)
	assert.True(t, pub.Retain)
	assert.Equal(t, byte(1), pub.QoS)
	assert.NotZero(t, pub.PacketID)
}

func TestHandleConn_ShouldDeliverWillWithQoS1_WhenClientDisconnectsUnexpectedly(t *testing.T) {
	srv := newTestServer()

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

type connectOptions struct {
	clientID     string
	cleanSession bool
	will         *protocol.WillMessage
}

func newTestServer() *Server {
	return New(":0", broker.New(), session.New())
}

func connectClient(t *testing.T, srv *Server, opts connectOptions) (net.Conn, bool) {
	t.Helper()

	serverConn, clientConn := net.Pipe()

	go srv.handleConn(context.Background(), serverConn)

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
	require.NoError(t, binary.Write(&variable, binary.BigEndian, uint16(60)))

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
