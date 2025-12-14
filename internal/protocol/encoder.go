package protocol

import (
	"encoding/binary"
	"errors"
	"io"
)

var ErrUnsupportedPacket = errors.New("unsupported packet type")

// Encode writes a packet to the given io.Writer. It returns an error
// if the packet type is not supported.
func Encode(w io.Writer, p Packet) error {
	switch pkt := p.(type) {
	case *ConnAckPacket:
		return encodeConnAck(w, pkt)
	case *PingRespPacket:
		return encodePingResp(w)
	case *SubAckPacket:
		return encodeSubAck(w, pkt)
	default:
		return ErrUnsupportedPacket
	}
}

// EncodePublish writes a PUBLISH packet to the given io.Writer. The
// packet will contain the given topic name and payload.
//
// The function returns an error if the write operation fails.
//
// The topic name is a UTF-8 string and the payload is a byte slice.
//
// The function is intended for use by the OrbMQ server only.
// It is not intended for use by clients.
func EncodePublish(w io.Writer, topic string, payload []byte) error {
	remainingLength := 2 + len(topic) + len(payload)

	// Fixed header
	if _, err := w.Write([]byte{
		0x30,
		byte(remainingLength),
	}); err != nil {
		return err
	}

	// Topic
	if err := binary.Write(w, binary.BigEndian, uint16(len(topic))); err != nil {
		return err
	}
	if _, err := w.Write([]byte(topic)); err != nil {
		return err
	}

	// Payload
	_, err := w.Write(payload)
	return err
}

// EncodeConnAck writes a CONNACK packet to the given io.Writer. The
// packet will contain the given session present flag and return code.
//
// The function returns an error if the write operation fails.
//
// The function is intended for use by the OrbMQ server only.
// It is not intended for use by clients.
func encodeConnAck(w io.Writer, pkt *ConnAckPacket) error {
	// Fixed Header
	if _, err := w.Write([]byte{0x20, 0x02}); err != nil {
		return err
	}

	var flags byte = 0x00
	if pkt.SessionPresent {
		flags = 0x01
	}

	_, err := w.Write([]byte{
		flags,
		byte(pkt.ReturnCode),
	})

	return err
}

// EncodePingResp writes a PINGRESP packet to the given io.Writer.
// The packet has no payload and is used to respond to a PINGREQ packet.
// The function returns an error if the write operation fails.
func encodePingResp(w io.Writer) error {
	_, err := w.Write([]byte{0xD0, 0x00})
	return err
}

// EncodeSubAck writes a SUBACK packet to the given io.Writer. The
// packet will contain the given packet identifier and return codes.
//
// The function returns an error if the write operation fails.
//
// The function is intended for use by the OrbMQ server only.
// It is not intended for use by clients.
func encodeSubAck(w io.Writer, pkt *SubAckPacket) error {
	remainingLength := 2 + len(pkt.ReturnCodes)

	// Fixed header
	if _, err := w.Write([]byte{
		0x90, // SUBACK
		byte(remainingLength),
	}); err != nil {
		return err
	}

	// Packet Identifier
	if err := binary.Write(w, binary.BigEndian, pkt.PacketID); err != nil {
		return err
	}

	// Return codes
	_, err := w.Write(pkt.ReturnCodes)
	return err
}
