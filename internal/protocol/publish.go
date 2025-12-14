package protocol

// PublishPacket is a PUBLISH packet sent from the client to the server
// and is used to send a message to all clients subscribed to topics that
// match the packet's topic name.
//
// It contains the topic name and the message payload.
type PublishPacket struct {
	Topic   string
	Payload []byte
}

func (p *PublishPacket) Type() PacketType {
	return PacketTypePublish
}
