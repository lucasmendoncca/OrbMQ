package protocol

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

	Username *string
	Password *string
}

func (c *ConnectPacket) Type() PacketType {
	return PacketTypeConnect
}
