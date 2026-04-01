package protocol

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	decoderClientID     = "client123"
	decoderUsername     = "user"
	decoderPassword     = "pass"
	decoderWillTopic    = "will/topic"
	decoderTopicFilter  = "sensors/+"
	decoderTopicName    = "topic"
	decoderTopicNameAlt = "topic/alt"
	decoderPayloadValue = "hello"
	decoderKeepAlive    = uint16(60)
	decoderPacketID     = uint16(42)
	decoderPacketIDAlt  = uint16(7)
	connectHeader       = byte(0x10)
	pingReqHeader       = byte(0xC0)
	publishQoS0Header   = byte(0x30)
	publishQoS1Header   = byte(0x32)
	publishDupHeader    = byte(0x3B)
	pubAckHeader        = byte(0x40)
	subscribeHeader     = byte(0x82)
	unsubscribeHeader   = byte(0xA2)
	disconnectHeader    = byte(0xE0)
)

func TestDecode_ShouldDecodeConnectPacket_WhenPacketIsValid(t *testing.T) {
	input := buildConnectPacket(connectPacketOptions{
		cleanSession: true,
		clientID:     decoderClientID,
	})

	pkt, err := Decode(bytes.NewReader(input))
	require.NoError(t, err)

	conn, ok := pkt.(*ConnectPacket)
	require.True(t, ok)
	assert.Equal(t, "MQTT", conn.ProtocolName)
	assert.Equal(t, byte(0x04), conn.ProtocolLevel)
	assert.True(t, conn.CleanSession)
	assert.Equal(t, decoderKeepAlive, conn.KeepAlive)
	assert.Equal(t, decoderClientID, conn.ClientID)
	assert.Nil(t, conn.Will)
	assert.Nil(t, conn.Username)
	assert.Nil(t, conn.Password)
}

func TestDecode_ShouldDecodeConnectPacketWithWillAndCredentials_WhenPacketIsValid(t *testing.T) {
	input := buildConnectPacket(connectPacketOptions{
		cleanSession: false,
		clientID:     decoderClientID,
		will: &WillMessage{
			Topic:   decoderWillTopic,
			Payload: []byte(decoderPayloadValue),
			QoS:     1,
			Retain:  true,
		},
		username: stringPtr(decoderUsername),
		password: stringPtr(decoderPassword),
	})

	pkt, err := Decode(bytes.NewReader(input))
	require.NoError(t, err)

	conn, ok := pkt.(*ConnectPacket)
	require.True(t, ok)
	require.NotNil(t, conn.Will)
	require.NotNil(t, conn.Username)
	require.NotNil(t, conn.Password)
	assert.False(t, conn.CleanSession)
	assert.Equal(t, decoderWillTopic, conn.Will.Topic)
	assert.Equal(t, []byte(decoderPayloadValue), conn.Will.Payload)
	assert.Equal(t, byte(1), conn.Will.QoS)
	assert.True(t, conn.Will.Retain)
	assert.Equal(t, decoderUsername, *conn.Username)
	assert.Equal(t, decoderPassword, *conn.Password)
}

func TestDecode_ShouldReturnError_WhenReaderFailsOnFirstByte(t *testing.T) {
	_, err := Decode(errReader{err: io.ErrUnexpectedEOF})
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestDecode_ShouldReturnError_WhenRemainingLengthIsTruncated(t *testing.T) {
	_, err := Decode(bytes.NewReader([]byte{connectHeader, 0x80, 0x80, 0x80, 0x80}))
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestDecode_ShouldReturnError_WhenConnectFlagsAreInvalid(t *testing.T) {
	_, err := Decode(bytes.NewReader([]byte{0x11, 0x00}))
	require.EqualError(t, err, "invalid CONNECT flags")
}

func TestDecode_ShouldReturnError_WhenConnectReservedFlagIsSet(t *testing.T) {
	input := buildConnectPacket(connectPacketOptions{
		cleanSession: true,
		clientID:     decoderClientID,
		rawFlags:     0x01,
	})

	_, err := Decode(bytes.NewReader(input))
	require.EqualError(t, err, "reserved connect flag must be 0")
}

func TestDecode_ShouldReturnError_WhenConnectWillQoSIsSetWithoutWillFlag(t *testing.T) {
	input := buildConnectPacket(connectPacketOptions{
		cleanSession: true,
		clientID:     decoderClientID,
		rawFlags:     0x08,
	})

	_, err := Decode(bytes.NewReader(input))
	require.EqualError(t, err, "will QoS and retain must be 0 when will flag is not set")
}

func TestDecode_ShouldReturnError_WhenConnectPasswordFlagRequiresUsernameFlag(t *testing.T) {
	input := buildConnectPacket(connectPacketOptions{
		cleanSession: true,
		clientID:     decoderClientID,
		rawFlags:     0x40 | 0x02,
		password:     stringPtr(decoderPassword),
	})

	_, err := Decode(bytes.NewReader(input))
	require.EqualError(t, err, "password flag requires username flag")
}

func TestDecode_ShouldReturnError_WhenConnectProtocolNameIsInvalid(t *testing.T) {
	input := buildConnectPacket(connectPacketOptions{
		cleanSession: true,
		clientID:     decoderClientID,
		protocolName: "MQTs",
	})

	_, err := Decode(bytes.NewReader(input))
	require.EqualError(t, err, "invalid protocol name")
}

func TestDecode_ShouldReturnError_WhenConnectProtocolNameCannotBeRead(t *testing.T) {
	_, err := decodeConnect(errReader{err: io.ErrUnexpectedEOF}, 10)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestDecode_ShouldReturnError_WhenConnectProtocolLevelCannotBeRead(t *testing.T) {
	body := utf8Bytes("MQTT")

	_, err := decodeConnect(bytes.NewReader(body), len(body))
	require.ErrorIs(t, err, io.EOF)
}

func TestDecode_ShouldReturnError_WhenConnectProtocolLevelIsUnsupported(t *testing.T) {
	input := buildConnectPacket(connectPacketOptions{
		cleanSession:  true,
		clientID:      decoderClientID,
		protocolLevel: 0x05,
	})

	_, err := Decode(bytes.NewReader(input))
	require.EqualError(t, err, "unsupported protocol level")
}

func TestDecode_ShouldReturnError_WhenConnectFlagsCannotBeRead(t *testing.T) {
	body := connectVariableHeaderBody("MQTT", 0x04)

	_, err := decodeConnect(bytes.NewReader(body), len(body))
	require.Error(t, err)
}

func TestDecode_ShouldReturnError_WhenConnectKeepAliveCannotBeRead(t *testing.T) {
	var body bytes.Buffer
	writeUTF8String(&body, "MQTT")
	body.WriteByte(0x04)
	body.WriteByte(0x02)
	body.WriteByte(0x00)

	_, err := decodeConnect(bytes.NewReader(body.Bytes()), body.Len())
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestDecode_ShouldReturnError_WhenConnectClientIDCannotBeRead(t *testing.T) {
	var body bytes.Buffer
	writeUTF8String(&body, "MQTT")
	body.WriteByte(0x04)
	body.WriteByte(0x02)
	_ = binary.Write(&body, binary.BigEndian, decoderKeepAlive)
	body.WriteByte(0x00)

	_, err := decodeConnect(bytes.NewReader(body.Bytes()), body.Len())
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestDecode_ShouldReturnError_WhenPersistentSessionHasEmptyClientID(t *testing.T) {
	input := buildConnectPacket(connectPacketOptions{
		cleanSession: false,
		clientID:     "",
	})

	_, err := Decode(bytes.NewReader(input))
	require.EqualError(t, err, "clientID must be present if clean session is false")
}

func TestDecode_ShouldReturnError_WhenConnectContainsExtraBytes(t *testing.T) {
	input := append(buildConnectPacket(connectPacketOptions{
		cleanSession: true,
		clientID:     "a",
	}), 0xFF)
	input[1]++

	_, err := Decode(bytes.NewReader(input))
	require.EqualError(t, err, "malformed CONNECT packet: extra bytes")
}

func TestDecode_ShouldReturnError_WhenConnectWillTopicCannotBeRead(t *testing.T) {
	body := connectBodyWithWill([]byte{0x00})

	_, err := decodeConnect(bytes.NewReader(body), len(body))
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestDecode_ShouldReturnError_WhenConnectWillPayloadCannotBeRead(t *testing.T) {
	body := connectBodyWithWill(append(utf8Bytes(decoderWillTopic), 0x00))

	_, err := decodeConnect(bytes.NewReader(body), len(body))
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestDecode_ShouldReturnError_WhenConnectUsernameCannotBeRead(t *testing.T) {
	body := connectBodyWithCredentials(true, false, []byte{0x00}, nil)

	_, err := decodeConnect(bytes.NewReader(body), len(body))
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestDecode_ShouldReturnError_WhenConnectPasswordCannotBeRead(t *testing.T) {
	body := connectBodyWithCredentials(true, true, utf8Bytes(decoderUsername), []byte{0x00})

	_, err := decodeConnect(bytes.NewReader(body), len(body))
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestDecode_ShouldDecodePingReqPacket_WhenPacketIsValid(t *testing.T) {
	pkt, err := Decode(bytes.NewReader([]byte{pingReqHeader, 0x00}))
	require.NoError(t, err)
	_, ok := pkt.(*PingReqPacket)
	require.True(t, ok)
}

func TestDecode_ShouldReturnError_WhenPingReqPacketIsInvalid(t *testing.T) {
	_, err := Decode(bytes.NewReader([]byte{pingReqHeader | 0x01, 0x00}))
	require.EqualError(t, err, "invalid PINGREQ packet")
}

func TestDecode_ShouldDecodePublishPacket_WhenQoS0PacketIsValid(t *testing.T) {
	input := buildPublishPacket(publishQoS0Header, 0, decoderTopicName, []byte(decoderPayloadValue))

	pkt, err := Decode(bytes.NewReader(input))
	require.NoError(t, err)

	pub, ok := pkt.(*PublishPacket)
	require.True(t, ok)
	assert.Equal(t, decoderTopicName, pub.Topic)
	assert.Equal(t, []byte(decoderPayloadValue), pub.Payload)
	assert.Equal(t, byte(0), pub.QoS)
	assert.Equal(t, uint16(0), pub.PacketID)
	assert.False(t, pub.Retain)
	assert.False(t, pub.DUP)
}

func TestDecode_ShouldDecodePublishPacket_WhenQoS1PacketIsValid(t *testing.T) {
	input := buildPublishPacket(publishQoS1Header, decoderPacketID, decoderTopicName, []byte(decoderPayloadValue))

	pkt, err := Decode(bytes.NewReader(input))
	require.NoError(t, err)

	pub, ok := pkt.(*PublishPacket)
	require.True(t, ok)
	assert.Equal(t, decoderTopicName, pub.Topic)
	assert.Equal(t, []byte(decoderPayloadValue), pub.Payload)
	assert.Equal(t, byte(1), pub.QoS)
	assert.Equal(t, decoderPacketID, pub.PacketID)
	assert.False(t, pub.Retain)
	assert.False(t, pub.DUP)
}

func TestDecode_ShouldDecodePublishPacket_WhenDupAndRetainAreSetForQoS1(t *testing.T) {
	input := buildPublishPacket(publishDupHeader, decoderPacketIDAlt, decoderTopicName, []byte(decoderPayloadValue))

	pkt, err := Decode(bytes.NewReader(input))
	require.NoError(t, err)

	pub, ok := pkt.(*PublishPacket)
	require.True(t, ok)
	assert.True(t, pub.DUP)
	assert.True(t, pub.Retain)
	assert.Equal(t, byte(1), pub.QoS)
}

func TestDecode_ShouldReturnError_WhenPublishHasInvalidQoSLevel(t *testing.T) {
	_, err := Decode(bytes.NewReader([]byte{0x36, 0x00}))
	require.EqualError(t, err, "invalid QoS level")
}

func TestDecode_ShouldReturnError_WhenPublishQoS0UsesDupFlag(t *testing.T) {
	_, err := Decode(bytes.NewReader([]byte{0x38, 0x00}))
	require.EqualError(t, err, "invalid DUP flag for QoS 0")
}

func TestDecode_ShouldReturnError_WhenPublishQoS1PacketIsMissingPacketID(t *testing.T) {
	input := []byte{
		publishQoS1Header, 0x0E,
		0x00, 0x05, 't', 'o', 'p', 'i', 'c',
		'h', 'e', 'l', 'l', 'o',
	}

	_, err := Decode(bytes.NewReader(input))
	require.Error(t, err)
}

func TestDecode_ShouldReturnError_WhenPublishPacketIDIsZero(t *testing.T) {
	input := buildPublishPacket(publishQoS1Header, 0, decoderTopicName, []byte(decoderPayloadValue))

	_, err := Decode(bytes.NewReader(input))
	require.EqualError(t, err, "invalid packet identifier")
}

func TestDecode_ShouldReturnError_WhenPublishTopicCannotBeRead(t *testing.T) {
	_, err := decodePublish(bytes.NewReader([]byte{0x00}), 1, 0, false, false)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestDecode_ShouldReturnError_WhenPublishPacketIDCannotBeRead(t *testing.T) {
	var body bytes.Buffer
	writeUTF8String(&body, decoderTopicName)
	body.WriteByte(0x00)

	_, err := decodePublish(bytes.NewReader(body.Bytes()), body.Len(), 1, false, false)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestDecode_ShouldDecodePubAckPacket_WhenPacketIsValid(t *testing.T) {
	pkt, err := Decode(bytes.NewReader([]byte{pubAckHeader, 0x02, 0x00, 0x2A}))
	require.NoError(t, err)

	puback, ok := pkt.(*PubAckPacket)
	require.True(t, ok)
	assert.Equal(t, decoderPacketID, puback.PacketID)
}

func TestDecode_ShouldReturnError_WhenPubAckFlagsAreInvalid(t *testing.T) {
	_, err := Decode(bytes.NewReader([]byte{pubAckHeader | 0x01, 0x02, 0x00, 0x2A}))
	require.EqualError(t, err, "invalid PUBACK flags")
}

func TestDecode_ShouldReturnError_WhenPubAckLengthIsInvalid(t *testing.T) {
	_, err := Decode(bytes.NewReader([]byte{pubAckHeader, 0x01, 0x00}))
	require.EqualError(t, err, "invalid PUBACK packet")
}

func TestDecode_ShouldReturnError_WhenPubAckPacketIDIsZero(t *testing.T) {
	_, err := Decode(bytes.NewReader([]byte{pubAckHeader, 0x02, 0x00, 0x00}))
	require.EqualError(t, err, "invalid packet identifier")
}

func TestDecode_ShouldReturnError_WhenPubAckPacketIDCannotBeRead(t *testing.T) {
	_, err := decodePubAck(bytes.NewReader([]byte{0x00}), 2)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestDecode_ShouldDecodeSubscribePacket_WhenPacketIsValid(t *testing.T) {
	input := buildSubscribePacket(decoderPacketID, []subscriptionInput{
		{topic: decoderTopicFilter, qos: 1},
		{topic: decoderTopicNameAlt, qos: 0},
	})

	pkt, err := Decode(bytes.NewReader(input))
	require.NoError(t, err)

	sub, ok := pkt.(*SubscribePacket)
	require.True(t, ok)
	assert.Equal(t, decoderPacketID, sub.PacketID)
	require.Len(t, sub.Subscriptions, 2)
	assert.Equal(t, decoderTopicFilter, sub.Subscriptions[0].Topic)
	assert.Equal(t, byte(1), sub.Subscriptions[0].QoS)
	assert.Equal(t, decoderTopicNameAlt, sub.Subscriptions[1].Topic)
	assert.Equal(t, byte(0), sub.Subscriptions[1].QoS)
}

func TestDecode_ShouldReturnError_WhenSubscribeFlagsAreInvalid(t *testing.T) {
	input := buildSubscribePacket(decoderPacketID, []subscriptionInput{{topic: decoderTopicFilter, qos: 1}})
	input[0] = 0x80

	_, err := Decode(bytes.NewReader(input))
	require.EqualError(t, err, "invalid SUBSCRIBE flags")
}

func TestDecode_ShouldReturnError_WhenSubscribePacketIDIsZero(t *testing.T) {
	input := buildSubscribePacket(0, []subscriptionInput{{topic: decoderTopicFilter, qos: 1}})

	_, err := Decode(bytes.NewReader(input))
	require.EqualError(t, err, "invalid packet identifier")
}

func TestDecode_ShouldReturnError_WhenSubscribeQoSIsInvalid(t *testing.T) {
	input := buildSubscribePacket(decoderPacketID, []subscriptionInput{{topic: decoderTopicFilter, qos: 3}})

	_, err := Decode(bytes.NewReader(input))
	require.EqualError(t, err, "invalid QoS level")
}

func TestDecode_ShouldReturnError_WhenSubscribeContainsNoTopics(t *testing.T) {
	_, err := Decode(bytes.NewReader([]byte{subscribeHeader, 0x02, 0x00, 0x2A}))
	require.EqualError(t, err, "subscribe must contain at least one topic")
}

func TestDecode_ShouldReturnError_WhenSubscribePacketIDCannotBeRead(t *testing.T) {
	_, err := decodeSubscribe(bytes.NewReader([]byte{0x00}), 1)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestDecode_ShouldReturnError_WhenSubscribeTopicCannotBeRead(t *testing.T) {
	body := []byte{0x00, 0x2A, 0x00}

	_, err := decodeSubscribe(bytes.NewReader(body), len(body))
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestDecode_ShouldReturnError_WhenSubscribeQoSCannotBeRead(t *testing.T) {
	var body bytes.Buffer
	_ = binary.Write(&body, binary.BigEndian, decoderPacketID)
	writeUTF8String(&body, decoderTopicFilter)

	_, err := decodeSubscribe(bytes.NewReader(body.Bytes()), body.Len())
	require.Error(t, err)
}

func TestDecode_ShouldDecodeUnsubscribePacket_WhenPacketIsValid(t *testing.T) {
	input := buildUnsubscribePacket(decoderPacketID, []string{decoderTopicFilter, decoderTopicNameAlt})

	pkt, err := Decode(bytes.NewReader(input))
	require.NoError(t, err)

	unsub, ok := pkt.(*UnsubscribePacket)
	require.True(t, ok)
	assert.Equal(t, decoderPacketID, unsub.PacketID)
	assert.Equal(t, []string{decoderTopicFilter, decoderTopicNameAlt}, unsub.Topics)
}

func TestDecode_ShouldReturnError_WhenUnsubscribeFlagsAreInvalid(t *testing.T) {
	input := buildUnsubscribePacket(decoderPacketID, []string{decoderTopicFilter})
	input[0] = 0xA0

	_, err := Decode(bytes.NewReader(input))
	require.EqualError(t, err, "invalid UNSUBSCRIBE flags")
}

func TestDecode_ShouldReturnError_WhenUnsubscribePacketIDIsZero(t *testing.T) {
	input := buildUnsubscribePacket(0, []string{decoderTopicFilter})

	_, err := Decode(bytes.NewReader(input))
	require.EqualError(t, err, "invalid packet identifier")
}

func TestDecode_ShouldReturnError_WhenUnsubscribeContainsNoTopics(t *testing.T) {
	_, err := Decode(bytes.NewReader([]byte{unsubscribeHeader, 0x02, 0x00, 0x2A}))
	require.EqualError(t, err, "unsubscribe must contain at least one topic")
}

func TestDecode_ShouldReturnError_WhenUnsubscribePacketIDCannotBeRead(t *testing.T) {
	_, err := decodeUnsubscribe(bytes.NewReader([]byte{0x00}), 1)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestDecode_ShouldReturnError_WhenUnsubscribeTopicCannotBeRead(t *testing.T) {
	body := []byte{0x00, 0x2A, 0x00}

	_, err := decodeUnsubscribe(bytes.NewReader(body), len(body))
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestDecode_ShouldDecodeDisconnectPacket_WhenPacketIsValid(t *testing.T) {
	pkt, err := Decode(bytes.NewReader([]byte{disconnectHeader, 0x00}))
	require.NoError(t, err)
	_, ok := pkt.(*DisconnectPacket)
	require.True(t, ok)
}

func TestDecode_ShouldReturnError_WhenDisconnectPacketIsInvalid(t *testing.T) {
	_, err := Decode(bytes.NewReader([]byte{disconnectHeader, 0x01, 0x00}))
	require.EqualError(t, err, "invalid DISCONNECT packet")
}

func TestDecode_ShouldReturnError_WhenPacketTypeIsUnsupported(t *testing.T) {
	_, err := Decode(bytes.NewReader([]byte{0x50, 0x00}))
	require.EqualError(t, err, "unsupported packet type")
}

func TestDecodeRemainingLength_ShouldDecodeMultiByteValue_WhenInputIsValid(t *testing.T) {
	value, err := decodeRemainingLength(bytes.NewReader([]byte{0xC1, 0x02}))
	require.NoError(t, err)
	assert.Equal(t, 321, value)
}

func TestDecodeRemainingLength_ShouldReturnError_WhenReaderFails(t *testing.T) {
	_, err := decodeRemainingLength(errReader{err: io.ErrUnexpectedEOF})
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestReadUTF8String_ShouldDecodeValue_WhenInputIsValid(t *testing.T) {
	value, err := readUTF8String(bytes.NewReader(utf8Bytes(decoderTopicName)))
	require.NoError(t, err)
	assert.Equal(t, decoderTopicName, value)
}

func TestReadUTF8String_ShouldReturnError_WhenLengthCannotBeRead(t *testing.T) {
	_, err := readUTF8String(bytes.NewReader([]byte{0x00}))
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestReadUTF8String_ShouldReturnError_WhenPayloadCannotBeRead(t *testing.T) {
	_, err := readUTF8String(bytes.NewReader([]byte{0x00, 0x05, 't'}))
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestReadBinaryData_ShouldDecodeValue_WhenInputIsValid(t *testing.T) {
	value, err := readBinaryData(bytes.NewReader(binaryBytes([]byte(decoderPayloadValue))))
	require.NoError(t, err)
	assert.Equal(t, []byte(decoderPayloadValue), value)
}

func TestReadBinaryData_ShouldReturnError_WhenLengthCannotBeRead(t *testing.T) {
	_, err := readBinaryData(bytes.NewReader([]byte{0x00}))
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestReadBinaryData_ShouldReturnError_WhenPayloadCannotBeRead(t *testing.T) {
	_, err := readBinaryData(bytes.NewReader([]byte{0x00, 0x05, 'h'}))
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestPacketTypes_ShouldReturnExpectedType_ForEachPacket(t *testing.T) {
	tests := []struct {
		name string
		pkt  Packet
		want PacketType
	}{
		{name: "connect", pkt: &ConnectPacket{}, want: PacketTypeConnect},
		{name: "connack", pkt: &ConnAckPacket{}, want: PacketTypeConnAck},
		{name: "disconnect", pkt: &DisconnectPacket{}, want: PacketTypeDisconnect},
		{name: "publish", pkt: &PublishPacket{}, want: PacketTypePublish},
		{name: "puback", pkt: &PubAckPacket{}, want: PacketTypePubAck},
		{name: "subscribe", pkt: &SubscribePacket{}, want: PacketTypeSubscribe},
		{name: "suback", pkt: &SubAckPacket{}, want: PacketTypeSubAck},
		{name: "unsubscribe", pkt: &UnsubscribePacket{}, want: PacketTypeUnsubscribe},
		{name: "unsuback", pkt: &UnsubAckPacket{}, want: PacketTypeUnsubAck},
		{name: "pingreq", pkt: &PingReqPacket{}, want: PacketTypePingReq},
		{name: "pingresp", pkt: &PingRespPacket{}, want: PacketTypePingResp},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.pkt.Type())
		})
	}
}

type connectPacketOptions struct {
	protocolName  string
	protocolLevel byte
	cleanSession  bool
	clientID      string
	will          *WillMessage
	username      *string
	password      *string
	rawFlags      byte
}

type subscriptionInput struct {
	topic string
	qos   byte
}

type errReader struct {
	err error
}

func (r errReader) Read(_ []byte) (int, error) {
	return 0, r.err
}

func buildConnectPacket(opts connectPacketOptions) []byte {
	protocolName := opts.protocolName
	if protocolName == "" {
		protocolName = "MQTT"
	}

	protocolLevel := opts.protocolLevel
	if protocolLevel == 0 {
		protocolLevel = 0x04
	}

	flags := opts.rawFlags
	if flags == 0 {
		if opts.cleanSession {
			flags |= 0x02
		}
		if opts.will != nil {
			flags |= 0x04
			flags |= (opts.will.QoS & 0x03) << 3
			if opts.will.Retain {
				flags |= 0x20
			}
		}
		if opts.username != nil {
			flags |= 0x80
		}
		if opts.password != nil {
			flags |= 0x40
		}
	}

	var body bytes.Buffer
	writeUTF8String(&body, protocolName)
	body.WriteByte(protocolLevel)
	body.WriteByte(flags)
	_ = binary.Write(&body, binary.BigEndian, decoderKeepAlive)
	writeUTF8String(&body, opts.clientID)

	if opts.will != nil {
		writeUTF8String(&body, opts.will.Topic)
		writeBinary(&body, opts.will.Payload)
	}
	if opts.username != nil {
		writeUTF8String(&body, *opts.username)
	}
	if opts.password != nil {
		writeUTF8String(&body, *opts.password)
	}

	return withFixedHeader(connectHeader, body.Bytes())
}

func buildPublishPacket(header byte, packetID uint16, topic string, payload []byte) []byte {
	var body bytes.Buffer
	writeUTF8String(&body, topic)
	if header>>1&0x03 > 0 {
		_ = binary.Write(&body, binary.BigEndian, packetID)
	}
	body.Write(payload)
	return withFixedHeader(header, body.Bytes())
}

func buildSubscribePacket(packetID uint16, subscriptions []subscriptionInput) []byte {
	var body bytes.Buffer
	_ = binary.Write(&body, binary.BigEndian, packetID)
	for _, sub := range subscriptions {
		writeUTF8String(&body, sub.topic)
		body.WriteByte(sub.qos)
	}
	return withFixedHeader(subscribeHeader, body.Bytes())
}

func buildUnsubscribePacket(packetID uint16, topics []string) []byte {
	var body bytes.Buffer
	_ = binary.Write(&body, binary.BigEndian, packetID)
	for _, topic := range topics {
		writeUTF8String(&body, topic)
	}
	return withFixedHeader(unsubscribeHeader, body.Bytes())
}

func withFixedHeader(header byte, body []byte) []byte {
	var packet bytes.Buffer
	packet.WriteByte(header)
	writeRemainingLength(&packet, len(body))
	packet.Write(body)
	return packet.Bytes()
}

func writeRemainingLength(w *bytes.Buffer, length int) {
	for {
		encodedByte := length % 128
		length /= 128
		if length > 0 {
			encodedByte |= 0x80
		}
		w.WriteByte(byte(encodedByte))
		if length == 0 {
			return
		}
	}
}

func writeUTF8String(w *bytes.Buffer, value string) {
	_ = binary.Write(w, binary.BigEndian, uint16(len(value)))
	w.WriteString(value)
}

func writeBinary(w *bytes.Buffer, value []byte) {
	_ = binary.Write(w, binary.BigEndian, uint16(len(value)))
	w.Write(value)
}

func stringPtr(value string) *string {
	return &value
}

func connectVariableHeaderBody(protocolName string, protocolLevel byte) []byte {
	var body bytes.Buffer
	writeUTF8String(&body, protocolName)
	body.WriteByte(protocolLevel)
	return body.Bytes()
}

func connectBodyWithWill(willBytes []byte) []byte {
	var body bytes.Buffer
	writeUTF8String(&body, "MQTT")
	body.WriteByte(0x04)
	body.WriteByte(0x0E)
	_ = binary.Write(&body, binary.BigEndian, decoderKeepAlive)
	writeUTF8String(&body, decoderClientID)
	body.Write(willBytes)
	return body.Bytes()
}

func connectBodyWithCredentials(usernameFlag, passwordFlag bool, usernameBytes []byte, passwordBytes []byte) []byte {
	var flags byte = 0x02
	if usernameFlag {
		flags |= 0x80
	}
	if passwordFlag {
		flags |= 0x40
	}

	var body bytes.Buffer
	writeUTF8String(&body, "MQTT")
	body.WriteByte(0x04)
	body.WriteByte(flags)
	_ = binary.Write(&body, binary.BigEndian, decoderKeepAlive)
	writeUTF8String(&body, decoderClientID)
	if usernameFlag {
		body.Write(usernameBytes)
	}
	if passwordFlag {
		body.Write(passwordBytes)
	}
	return body.Bytes()
}

func utf8Bytes(value string) []byte {
	var body bytes.Buffer
	writeUTF8String(&body, value)
	return body.Bytes()
}

func binaryBytes(value []byte) []byte {
	var body bytes.Buffer
	writeBinary(&body, value)
	return body.Bytes()
}
