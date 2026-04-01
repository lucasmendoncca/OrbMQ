package protocol

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testTopic         = "topic"
	testPayload       = "hello"
	testPacketIDQoS1  = uint16(42)
	testPacketIDAlt   = uint16(7)
	testPacketIDAck   = uint16(9)
	testPacketIDSub   = uint16(12)
	testQoS0          = byte(0)
	testQoS1          = byte(1)
	headerConnAck     = byte(0x20)
	headerPingResp    = byte(0xD0)
	headerPublishQoS0 = byte(0x30)
	headerPublishQoS1 = byte(0x32)
	headerPublishDup1 = byte(0x3B)
	headerPubAck      = byte(0x40)
	headerSubAck      = byte(0x90)
	headerUnsubAck    = byte(0xB0)
)

func TestEncodeRemainingLength_ShouldEncodeSingleByte_WhenLengthFitsInOneByte(t *testing.T) {
	var buf bytes.Buffer

	err := encodeRemainingLength(&buf, 127)
	require.NoError(t, err)

	assert.Equal(t, []byte{0x7F}, buf.Bytes())
}

func TestEncodeRemainingLength_ShouldEncodeMultipleBytes_WhenLengthExceedsOneByte(t *testing.T) {
	var buf bytes.Buffer

	err := encodeRemainingLength(&buf, 321)
	require.NoError(t, err)

	assert.Equal(t, []byte{0xC1, 0x02}, buf.Bytes())
}

func TestEncodeRemainingLength_ShouldReturnError_WhenWriterFails(t *testing.T) {
	err := encodeRemainingLength(&callFailWriter{failOnCall: 1}, 1)
	require.Error(t, err)
}

func TestEncode_ShouldReturnUnsupportedPacketError_WhenPacketTypeIsUnknown(t *testing.T) {
	var buf bytes.Buffer

	err := Encode(&buf, &ConnectPacket{})
	require.ErrorIs(t, err, ErrUnsupportedPacket)
}

func TestEncode_ShouldEncodeConnAckPacket_WhenSessionPresentIsFalse(t *testing.T) {
	var buf bytes.Buffer

	err := Encode(&buf, &ConnAckPacket{
		SessionPresent: false,
		ReturnCode:     ConnAckAccepted,
	})
	require.NoError(t, err)

	assert.Equal(t, expectedConnAckPacket(false), buf.Bytes())
}

func TestEncode_ShouldEncodeConnAckPacket_WhenSessionPresentIsTrue(t *testing.T) {
	var buf bytes.Buffer

	err := Encode(&buf, &ConnAckPacket{
		SessionPresent: true,
		ReturnCode:     ConnAckAccepted,
	})
	require.NoError(t, err)

	assert.Equal(t, expectedConnAckPacket(true), buf.Bytes())
}

func TestEncode_ShouldReturnError_WhenConnAckHeaderWriteFails(t *testing.T) {
	err := Encode(&callFailWriter{failOnCall: 1}, &ConnAckPacket{
		ReturnCode: ConnAckAccepted,
	})
	require.Error(t, err)
}

func TestEncode_ShouldReturnError_WhenConnAckPayloadWriteFails(t *testing.T) {
	err := Encode(&callFailWriter{failOnCall: 2}, &ConnAckPacket{
		ReturnCode: ConnAckAccepted,
	})
	require.Error(t, err)
}

func TestEncode_ShouldEncodePingRespPacket_WhenPacketIsValid(t *testing.T) {
	var buf bytes.Buffer

	err := Encode(&buf, &PingRespPacket{})
	require.NoError(t, err)

	assert.Equal(t, []byte{headerPingResp, 0x00}, buf.Bytes())
}

func TestEncode_ShouldReturnError_WhenPingRespWriteFails(t *testing.T) {
	err := Encode(&callFailWriter{failOnCall: 1}, &PingRespPacket{})
	require.Error(t, err)
}

func TestEncodePublish_ShouldEncodeQoS0Packet_WhenPacketIsValid(t *testing.T) {
	var buf bytes.Buffer

	err := EncodePublish(&buf, &PublishPacket{
		Topic:   testTopic,
		Payload: []byte(testPayload),
		QoS:     testQoS0,
	})
	require.NoError(t, err)

	assert.Equal(t, expectedPublishPacket(headerPublishQoS0, 0, testTopic, []byte(testPayload), 0), buf.Bytes())
}

func TestEncodePublish_ShouldEncodeQoS1Packet_WhenPacketIsValid(t *testing.T) {
	var buf bytes.Buffer

	err := EncodePublish(&buf, &PublishPacket{
		Topic:    testTopic,
		Payload:  []byte(testPayload),
		QoS:      testQoS1,
		PacketID: testPacketIDQoS1,
	})
	require.NoError(t, err)

	assert.Equal(t, expectedPublishPacket(headerPublishQoS1, testPacketIDQoS1, testTopic, []byte(testPayload), 2), buf.Bytes())
}

func TestEncodePublish_ShouldEncodeRetainedReplayPacket_WhenQoS1WithDup(t *testing.T) {
	var buf bytes.Buffer

	err := EncodePublish(&buf, &PublishPacket{
		Topic:    testTopic,
		Payload:  []byte(testPayload),
		Retain:   true,
		DUP:      true,
		QoS:      testQoS1,
		PacketID: testPacketIDAlt,
	})
	require.NoError(t, err)

	assert.Equal(t, expectedPublishPacket(headerPublishDup1, testPacketIDAlt, testTopic, []byte(testPayload), 2), buf.Bytes())
}

func TestEncodePublish_ShouldReturnError_WhenQoSIsUnsupported(t *testing.T) {
	err := EncodePublish(&bytes.Buffer{}, &PublishPacket{QoS: 2})
	require.EqualError(t, err, "unsupported QoS level")
}

func TestEncodePublish_ShouldReturnError_WhenQoS1PacketIDIsMissing(t *testing.T) {
	err := EncodePublish(&bytes.Buffer{}, &PublishPacket{QoS: testQoS1})
	require.EqualError(t, err, "invalid packet identifier")
}

func TestEncodePublish_ShouldReturnError_WhenQoS0UsesDupFlag(t *testing.T) {
	err := EncodePublish(&bytes.Buffer{}, &PublishPacket{QoS: testQoS0, DUP: true})
	require.EqualError(t, err, "invalid DUP flag for QoS 0")
}

func TestEncodePublish_ShouldReturnError_WhenHeaderWriteFails(t *testing.T) {
	err := EncodePublish(&callFailWriter{failOnCall: 1}, &PublishPacket{
		Topic:   testTopic,
		Payload: []byte(testPayload),
	})
	require.Error(t, err)
}

func TestEncodePublish_ShouldReturnError_WhenRemainingLengthWriteFails(t *testing.T) {
	err := EncodePublish(&callFailWriter{failOnCall: 2}, &PublishPacket{
		Topic:   testTopic,
		Payload: []byte(testPayload),
	})
	require.Error(t, err)
}

func TestEncodePublish_ShouldReturnError_WhenTopicLengthWriteFails(t *testing.T) {
	err := EncodePublish(&callFailWriter{failOnCall: 3}, &PublishPacket{
		Topic:   testTopic,
		Payload: []byte(testPayload),
	})
	require.Error(t, err)
}

func TestEncodePublish_ShouldReturnError_WhenTopicWriteFails(t *testing.T) {
	err := EncodePublish(&callFailWriter{failOnCall: 4}, &PublishPacket{
		Topic:   testTopic,
		Payload: []byte(testPayload),
	})
	require.Error(t, err)
}

func TestEncodePublish_ShouldReturnError_WhenPacketIDWriteFails(t *testing.T) {
	err := EncodePublish(&callFailWriter{failOnCall: 5}, &PublishPacket{
		Topic:    testTopic,
		Payload:  []byte(testPayload),
		QoS:      testQoS1,
		PacketID: 1,
	})
	require.Error(t, err)
}

func TestEncodePublish_ShouldReturnError_WhenPayloadWriteFails(t *testing.T) {
	err := EncodePublish(&callFailWriter{failOnCall: 5}, &PublishPacket{
		Topic:   testTopic,
		Payload: []byte(testPayload),
	})
	require.Error(t, err)
}

func TestEncodeRetained_ShouldEncodeQoS0RetainedPublish_WhenCalled(t *testing.T) {
	got := EncodeRetained(testTopic, []byte(testPayload))

	assert.Equal(t, expectedPublishPacket(headerPublishQoS0|0x01, 0, testTopic, []byte(testPayload), 0), got)
}

func TestEncode_ShouldEncodePubAckPacket_WhenPacketIsValid(t *testing.T) {
	var buf bytes.Buffer

	err := Encode(&buf, &PubAckPacket{PacketID: testPacketIDQoS1})
	require.NoError(t, err)

	assert.Equal(t, expectedAckPacket(headerPubAck, testPacketIDQoS1), buf.Bytes())
}

func TestEncode_ShouldReturnError_WhenPubAckPacketIDIsInvalid(t *testing.T) {
	err := Encode(&bytes.Buffer{}, &PubAckPacket{})
	require.EqualError(t, err, "invalid packet identifier")
}

func TestEncode_ShouldReturnError_WhenPubAckHeaderWriteFails(t *testing.T) {
	err := Encode(&callFailWriter{failOnCall: 1}, &PubAckPacket{PacketID: testPacketIDQoS1})
	require.Error(t, err)
}

func TestEncode_ShouldReturnError_WhenPubAckPacketIDWriteFails(t *testing.T) {
	err := Encode(&callFailWriter{failOnCall: 2}, &PubAckPacket{PacketID: testPacketIDQoS1})
	require.Error(t, err)
}

func TestEncode_ShouldEncodeUnsubAckPacket_WhenPacketIsValid(t *testing.T) {
	var buf bytes.Buffer

	err := Encode(&buf, &UnsubAckPacket{PacketID: testPacketIDAck})
	require.NoError(t, err)

	assert.Equal(t, expectedAckPacket(headerUnsubAck, testPacketIDAck), buf.Bytes())
}

func TestEncode_ShouldReturnError_WhenUnsubAckHeaderWriteFails(t *testing.T) {
	err := Encode(&callFailWriter{failOnCall: 1}, &UnsubAckPacket{PacketID: testPacketIDAck})
	require.Error(t, err)
}

func TestEncode_ShouldReturnError_WhenUnsubAckPacketIDWriteFails(t *testing.T) {
	err := Encode(&callFailWriter{failOnCall: 2}, &UnsubAckPacket{PacketID: testPacketIDAck})
	require.Error(t, err)
}

func TestEncode_ShouldEncodeSubAckPacket_WhenPacketIsValid(t *testing.T) {
	var buf bytes.Buffer

	err := Encode(&buf, &SubAckPacket{
		PacketID:    testPacketIDSub,
		ReturnCodes: []byte{0x00, 0x01},
	})
	require.NoError(t, err)

	assert.Equal(t, expectedSubAckPacket(testPacketIDSub, []byte{0x00, 0x01}), buf.Bytes())
}

func TestEncode_ShouldEncodeSubAckPacketWithMultiByteRemainingLength_WhenPayloadIsLarge(t *testing.T) {
	var buf bytes.Buffer

	err := Encode(&buf, &SubAckPacket{
		PacketID:    testPacketIDSub,
		ReturnCodes: bytes.Repeat([]byte{0x01}, 200),
	})
	require.NoError(t, err)

	assert.Equal(t, headerSubAck, buf.Bytes()[0])
	assert.Equal(t, []byte{0xCA, 0x01}, buf.Bytes()[1:3])
}

func TestEncode_ShouldReturnError_WhenSubAckHeaderWriteFails(t *testing.T) {
	err := Encode(&callFailWriter{failOnCall: 1}, &SubAckPacket{
		PacketID:    testPacketIDSub,
		ReturnCodes: []byte{0x00},
	})
	require.Error(t, err)
}

func TestEncode_ShouldReturnError_WhenSubAckRemainingLengthWriteFails(t *testing.T) {
	err := Encode(&callFailWriter{failOnCall: 2}, &SubAckPacket{
		PacketID:    testPacketIDSub,
		ReturnCodes: []byte{0x00},
	})
	require.Error(t, err)
}

func TestEncode_ShouldReturnError_WhenSubAckPacketIDWriteFails(t *testing.T) {
	err := Encode(&callFailWriter{failOnCall: 3}, &SubAckPacket{
		PacketID:    testPacketIDSub,
		ReturnCodes: []byte{0x00},
	})
	require.Error(t, err)
}

func TestEncode_ShouldReturnError_WhenSubAckPayloadWriteFails(t *testing.T) {
	err := Encode(&callFailWriter{failOnCall: 4}, &SubAckPacket{
		PacketID:    testPacketIDSub,
		ReturnCodes: []byte{0x00},
	})
	require.Error(t, err)
}

type callFailWriter struct {
	failOnCall int
	callCount  int
}

func (w *callFailWriter) Write(p []byte) (int, error) {
	w.callCount++
	if w.callCount == w.failOnCall {
		return 0, errors.New("write failed")
	}
	return len(p), nil
}

func expectedConnAckPacket(sessionPresent bool) []byte {
	flags := byte(0x00)
	if sessionPresent {
		flags = 0x01
	}

	return []byte{headerConnAck, 0x02, flags, 0x00}
}

func expectedPublishPacket(header byte, packetID uint16, topic string, payload []byte, packetIDLength int) []byte {
	remainingLength := byte(2 + len(topic) + len(payload) + packetIDLength)

	packet := []byte{
		header,
		remainingLength,
		0x00, byte(len(topic)),
	}
	packet = append(packet, []byte(topic)...)

	if packetIDLength > 0 {
		packet = append(packet, byte(packetID>>8), byte(packetID))
	}

	packet = append(packet, payload...)
	return packet
}

func expectedAckPacket(header byte, packetID uint16) []byte {
	return []byte{header, 0x02, byte(packetID >> 8), byte(packetID)}
}

func expectedSubAckPacket(packetID uint16, returnCodes []byte) []byte {
	packet := []byte{
		headerSubAck,
		byte(2 + len(returnCodes)),
		byte(packetID >> 8),
		byte(packetID),
	}
	packet = append(packet, returnCodes...)
	return packet
}
