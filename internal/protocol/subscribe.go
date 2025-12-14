package protocol

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
