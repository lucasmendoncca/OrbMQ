package protocol

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
