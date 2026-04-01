package protocol

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecode_ShouldDecodeConnectPacket_WhenPacketIsValid(t *testing.T) {
	input := []byte{
		0x10, 0x15,
		0x00, 0x04, 'M', 'Q', 'T', 'T',
		0x04,
		0x02,
		0x00, 0x3C,
		0x00, 0x09, 'c', 'l', 'i', 'e', 'n', 't', '1', '2', '3',
	}

	pkt, err := Decode(bytes.NewReader(input))
	require.NoError(t, err)

	conn, ok := pkt.(*ConnectPacket)
	require.True(t, ok)
	assert.Equal(t, "MQTT", conn.ProtocolName)
	assert.Equal(t, byte(0x04), conn.ProtocolLevel)
	assert.True(t, conn.CleanSession)
	assert.Equal(t, uint16(60), conn.KeepAlive)
	assert.Equal(t, "client123", conn.ClientID)
}

func TestDecode_ShouldReturnError_WhenConnectFlagsAreInvalid(t *testing.T) {
	input := []byte{
		0x11, 0x00,
	}

	_, err := Decode(bytes.NewReader(input))
	require.Error(t, err)
}

func TestDecode_ShouldDecodePublishPacket_WhenQoS1PacketIsValid(t *testing.T) {
	input := []byte{
		0x32, 0x0E,
		0x00, 0x05, 't', 'o', 'p', 'i', 'c',
		0x00, 0x2A,
		'h', 'e', 'l', 'l', 'o',
	}

	pkt, err := Decode(bytes.NewReader(input))
	require.NoError(t, err)

	pub, ok := pkt.(*PublishPacket)
	require.True(t, ok)
	assert.Equal(t, "topic", pub.Topic)
	assert.Equal(t, []byte("hello"), pub.Payload)
	assert.Equal(t, byte(1), pub.QoS)
	assert.Equal(t, uint16(42), pub.PacketID)
	assert.False(t, pub.Retain)
	assert.False(t, pub.DUP)
}

func TestDecode_ShouldReturnError_WhenPublishQoS1PacketIsMissingPacketID(t *testing.T) {
	input := []byte{
		0x32, 0x0E,
		0x00, 0x05, 't', 'o', 'p', 'i', 'c',
		'h', 'e', 'l', 'l', 'o',
	}

	_, err := Decode(bytes.NewReader(input))
	require.Error(t, err)
}

func TestDecode_ShouldReturnError_WhenPublishPacketIDIsZero(t *testing.T) {
	input := []byte{
		0x32, 0x0E,
		0x00, 0x05, 't', 'o', 'p', 'i', 'c',
		0x00, 0x00,
		'h', 'e', 'l', 'l', 'o',
	}

	_, err := Decode(bytes.NewReader(input))
	require.Error(t, err)
}

func TestDecode_ShouldDecodePubAckPacket_WhenPacketIsValid(t *testing.T) {
	input := []byte{0x40, 0x02, 0x00, 0x2A}

	pkt, err := Decode(bytes.NewReader(input))
	require.NoError(t, err)

	puback, ok := pkt.(*PubAckPacket)
	require.True(t, ok)
	assert.Equal(t, uint16(42), puback.PacketID)
}
