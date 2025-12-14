package protocol

type PacketType byte

const (
	PacketTypeConnect   PacketType = 1
	PacketTypeConnAck   PacketType = 2
	PacketTypePublish   PacketType = 3
	PacketTypeSubscribe PacketType = 8
	PacketTypeSubAck    PacketType = 9
	PacketTypePingReq   PacketType = 12
	PacketTypePingResp  PacketType = 13
)

type Packet interface {
	Type() PacketType
}
