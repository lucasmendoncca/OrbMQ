package protocol

// DisconnectPacket is a DISCONNECT packet sent from the client to the server
// and is used to indicate that the client is disconnecting from the server.
//
// It has no payload.
type DisconnectPacket struct{}

func (d *DisconnectPacket) Type() PacketType {
	return PacketTypeDisconnect
}
