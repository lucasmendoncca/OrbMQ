package protocol

type ConnAckReturnCode byte

const (
	ConnAckAccepted ConnAckReturnCode = 0x00
)

// ConnAckPacket is a CONNACK packet sent from the server to the client
// in response to a CONNECT packet from the client.
//
// It contains a session present flag and a return code.
type ConnAckPacket struct {
	SessionPresent bool
	ReturnCode     ConnAckReturnCode
}

func (c *ConnAckPacket) Type() PacketType {
	return PacketTypeConnAck
}
