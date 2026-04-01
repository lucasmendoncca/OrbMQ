package protocol

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodePublish_ShouldEncodeQoS1Packet_WhenPacketIsValid(t *testing.T) {
	var buf bytes.Buffer

	err := EncodePublish(&buf, &PublishPacket{
		Topic:    "topic",
		Payload:  []byte("hello"),
		QoS:      1,
		PacketID: 42,
	})
	require.NoError(t, err)

	assert.Equal(t, []byte{
		0x32, 0x0E,
		0x00, 0x05, 't', 'o', 'p', 'i', 'c',
		0x00, 0x2A,
		'h', 'e', 'l', 'l', 'o',
	}, buf.Bytes())
}

func TestEncode_ShouldEncodePubAckPacket_WhenPacketIsValid(t *testing.T) {
	var buf bytes.Buffer

	err := Encode(&buf, &PubAckPacket{PacketID: 42})
	require.NoError(t, err)

	assert.Equal(t, []byte{0x40, 0x02, 0x00, 0x2A}, buf.Bytes())
}

func TestEncodePublish_ShouldEncodeRetainedReplayPacket_WhenQoS1WithDup(t *testing.T) {
	var buf bytes.Buffer

	err := EncodePublish(&buf, &PublishPacket{
		Topic:    "topic",
		Payload:  []byte("hello"),
		Retain:   true,
		DUP:      true,
		QoS:      1,
		PacketID: 7,
	})
	require.NoError(t, err)

	assert.Equal(t, []byte{
		0x3B, 0x0E,
		0x00, 0x05, 't', 'o', 'p', 'i', 'c',
		0x00, 0x07,
		'h', 'e', 'l', 'l', 'o',
	}, buf.Bytes())
}
