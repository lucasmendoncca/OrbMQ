package protocol

type PacketType byte

const (
	PacketTypeConnect     PacketType = 1
	PacketTypeConnAck     PacketType = 2
	PacketTypePublish     PacketType = 3
	PacketTypePubAck      PacketType = 4
	PacketTypeSubscribe   PacketType = 8
	PacketTypeSubAck      PacketType = 9
	PacketTypeUnsubscribe PacketType = 10
	PacketTypeUnsubAck    PacketType = 11
	PacketTypePingReq     PacketType = 12
	PacketTypePingResp    PacketType = 13
	PacketTypeDisconnect  PacketType = 14
)

type Packet interface {
	Type() PacketType
}

// WillMessage is the Last Will and Testament declared in a CONNECT packet.
// The broker publishes it when the client disconnects without sending DISCONNECT.
type WillMessage struct {
	Topic   string
	Payload []byte
	QoS     byte
	Retain  bool
}

// ConnectPacket represents a CONNECT packet sent by a client to the server.
// It contains information about the client such as its protocol version,
// whether it wants to clean its session, and how often it wants to send
// PINGREQ packets to the server.
//
// The client may also send a username and password to authenticate with the
// server.
type ConnectPacket struct {
	ProtocolName  string
	ProtocolLevel byte
	CleanSession  bool
	KeepAlive     uint16

	ClientID string

	Will     *WillMessage
	Username *string
	Password *string
}

func (c *ConnectPacket) Type() PacketType {
	return PacketTypeConnect
}

// ConnAckPacket is a CONNACK packet sent from the server to the client
// in response to a CONNECT packet from the client.
//
// It contains a session present flag and a return code.
type ConnAckPacket struct {
	SessionPresent bool
	ReturnCode     ConnAckReturnCode
}

type ConnAckReturnCode byte

const (
	ConnAckAccepted ConnAckReturnCode = 0x00
)

func (c *ConnAckPacket) Type() PacketType {
	return PacketTypeConnAck
}

// DisconnectPacket is a DISCONNECT packet sent from the client to the server
// and is used to indicate that the client is disconnecting from the server.
//
// It has no payload.
type DisconnectPacket struct{}

func (d *DisconnectPacket) Type() PacketType {
	return PacketTypeDisconnect
}

// PublishPacket is a PUBLISH packet sent between client and broker.
type PublishPacket struct {
	Topic    string
	Payload  []byte
	Retain   bool
	DUP      bool
	QoS      byte
	PacketID uint16
}

func (p *PublishPacket) Type() PacketType {
	return PacketTypePublish
}

// PubAckPacket is a PUBACK packet sent in response to a QoS 1 PUBLISH packet.
type PubAckPacket struct {
	PacketID uint16
}

func (p *PubAckPacket) Type() PacketType {
	return PacketTypePubAck
}

// SubscribePacket represents a SUBSCRIBE packet sent by a client to the server.
// It contains a packet identifier and a slice of Subscription objects, each of
// which contains the topic name and QoS level.
type SubscribePacket struct {
	PacketID      uint16
	Subscriptions []Subscription
}

type Subscription struct {
	Topic string
	QoS   byte
}

func (s *SubscribePacket) Type() PacketType {
	return PacketTypeSubscribe
}

// SubAckPacket is a SUBACK packet sent from the server to the client
// in response to a SUBSCRIBE packet from the client.
//
// It contains a packet identifier and a slice of return codes, one
// for each topic in the SUBSCRIBE packet. The return codes are
// byte values that indicate the success or failure of each topic
// subscription.
type SubAckPacket struct {
	PacketID    uint16
	ReturnCodes []byte
}

func (s *SubAckPacket) Type() PacketType {
	return PacketTypeSubAck
}

// UnsubscribePacket represents an UNSUBSCRIBE packet sent from the client to the server.
// It contains a packet identifier and a slice of topic filters to unsubscribe from.
type UnsubscribePacket struct {
	PacketID uint16
	Topics   []string
}

func (u *UnsubscribePacket) Type() PacketType {
	return PacketTypeUnsubscribe
}

// UnsubAckPacket is an UNSUBACK packet sent from the server to the client
// in response to an UNSUBSCRIBE packet. It contains only the packet identifier.
type UnsubAckPacket struct {
	PacketID uint16
}

func (u *UnsubAckPacket) Type() PacketType {
	return PacketTypeUnsubAck
}

// PingReqPacket is a PINGREQ packet sent from the client to the server
// and is used to check if the server is alive.
//
// It has no payload and is used to request a PINGRESP packet from the server.
type PingReqPacket struct{}

func (p *PingReqPacket) Type() PacketType {
	return PacketTypePingReq
}

// PingRespPacket is a PINGRESP packet sent from the server to the client
// in response to a PINGREQ packet from the client.
//
// It has no payload and is used to respond to a PINGREQ packet.
type PingRespPacket struct{}

func (p *PingRespPacket) Type() PacketType {
	return PacketTypePingResp
}
