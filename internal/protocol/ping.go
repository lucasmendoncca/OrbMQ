package protocol

// PingReqPacket is a PINGREQ packet sent from the client to the server
// and is used to check if the server is alive.
//
// It has no payload and is used to request a PINGRESP packet from the server.
type PingReqPacket struct{}

// PingRespPacket is a PINGRESP packet sent from the server to the client
// in response to a PINGREQ packet from the client.
//
// It has no payload and is used to respond to a PINGREQ packet.
type PingRespPacket struct{}

func (p *PingReqPacket) Type() PacketType {
	return PacketTypePingReq
}

func (p *PingRespPacket) Type() PacketType {
	return PacketTypePingResp
}
