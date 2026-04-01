package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
)

var ErrUnsupportedPacket = errors.New("unsupported packet type")

// encodeRemainingLength writes the MQTT variable-length remaining length field.
// It encodes values from 0 up to 268,435,455 using 1-4 bytes.
func encodeRemainingLength(w io.Writer, length int) error {
	for {
		encodedByte := length % 128
		length /= 128
		if length > 0 {
			encodedByte |= 0x80
		}
		if _, err := w.Write([]byte{byte(encodedByte)}); err != nil {
			return err
		}
		if length == 0 {
			return nil
		}
	}
}

// Encode writes a packet to the given io.Writer. It returns an error
// if the packet type is not supported.
func Encode(w io.Writer, p Packet) error {
	switch pkt := p.(type) {
	case *ConnAckPacket:
		return encodeConnAck(w, pkt)
	case *PingRespPacket:
		return encodePingResp(w)
	case *PubAckPacket:
		return encodePubAck(w, pkt)
	case *SubAckPacket:
		return encodeSubAck(w, pkt)
	case *UnsubAckPacket:
		return encodeUnsubAck(w, pkt)
	default:
		return ErrUnsupportedPacket
	}
}

// EncodePublish writes a PUBLISH packet to the given io.Writer.
func EncodePublish(w io.Writer, pkt *PublishPacket) error {
	if pkt.QoS > 1 {
		return errors.New("unsupported QoS level")
	}
	if pkt.QoS > 0 && pkt.PacketID == 0 {
		return errors.New("invalid packet identifier")
	}
	if pkt.QoS == 0 && pkt.DUP {
		return errors.New("invalid DUP flag for QoS 0")
	}

	remainingLength := 2 + len(pkt.Topic) + len(pkt.Payload)
	if pkt.QoS > 0 {
		remainingLength += 2
	}

	flags := byte(0x30)
	if pkt.DUP {
		flags |= 0x08
	}
	flags |= (pkt.QoS & 0x03) << 1
	if pkt.Retain {
		flags |= 0x01
	}

	if _, err := w.Write([]byte{flags}); err != nil {
		return err
	}
	if err := encodeRemainingLength(w, remainingLength); err != nil {
		return err
	}

	if err := binary.Write(w, binary.BigEndian, uint16(len(pkt.Topic))); err != nil {
		return err
	}
	if _, err := w.Write([]byte(pkt.Topic)); err != nil {
		return err
	}
	if pkt.QoS > 0 {
		if err := binary.Write(w, binary.BigEndian, pkt.PacketID); err != nil {
			return err
		}
	}

	_, err := w.Write(pkt.Payload)
	return err
}

func EncodeRetained(topic string, payload []byte) []byte {
	var buf bytes.Buffer

	_ = EncodePublish(&buf, &PublishPacket{
		Topic:   topic,
		Payload: payload,
		Retain:  true,
	})
	return buf.Bytes()
}

// EncodeConnAck writes a CONNACK packet to the given io.Writer. The
// packet will contain the given session present flag and return code.
//
// The function returns an error if the write operation fails.
//
// The function is intended for use by the OrbMQ server only.
// It is not intended for use by clients.
func encodeConnAck(w io.Writer, pkt *ConnAckPacket) error {
	if _, err := w.Write([]byte{0x20, 0x02}); err != nil {
		return err
	}

	var flags byte
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

func encodePubAck(w io.Writer, pkt *PubAckPacket) error {
	if pkt.PacketID == 0 {
		return errors.New("invalid packet identifier")
	}
	if _, err := w.Write([]byte{0x40, 0x02}); err != nil {
		return err
	}
	return binary.Write(w, binary.BigEndian, pkt.PacketID)
}

// encodeUnsubAck writes an UNSUBACK packet to the given io.Writer.
func encodeUnsubAck(w io.Writer, pkt *UnsubAckPacket) error {
	if _, err := w.Write([]byte{0xB0, 0x02}); err != nil {
		return err
	}
	return binary.Write(w, binary.BigEndian, pkt.PacketID)
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

	if _, err := w.Write([]byte{0x90}); err != nil {
		return err
	}
	if err := encodeRemainingLength(w, remainingLength); err != nil {
		return err
	}
	if err := binary.Write(w, binary.BigEndian, pkt.PacketID); err != nil {
		return err
	}

	_, err := w.Write(pkt.ReturnCodes)
	return err
}
