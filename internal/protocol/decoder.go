package protocol

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
)

type connectFlags struct {
	cleanSession bool
	willFlag     bool
	willQoS      byte
	willRetain   bool
	usernameFlag bool
	passwordFlag bool
}

// Decode reads a packet from the given io.Reader and returns the corresponding
// decoded Packet, or an error if the packet is invalid.
//
// The function first reads the fixed header, then reads the remaining
// length of the packet. Finally, it decodes the packet based on the
// packet type and the given flags and remaining length.
//
// If the packet type is not supported, the function returns an error.
//
// If the flags or remaining length are invalid for the given packet
// type, the function returns an error.
func Decode(r io.Reader) (Packet, error) {
	br := bufio.NewReader(r)

	b1, err := br.ReadByte()
	if err != nil {
		return nil, err
	}

	packetType := PacketType(b1 >> 4)
	flags := b1 & 0x0F

	remainingLength, err := decodeRemainingLength(br)
	if err != nil {
		return nil, err
	}

	switch packetType {
	case PacketTypeConnect:
		if flags != 0 {
			return nil, errors.New("invalid CONNECT flags")
		}
		return decodeConnect(br, remainingLength)

	case PacketTypePingReq:
		if flags != 0 || remainingLength != 0 {
			return nil, errors.New("invalid PINGREQ packet")
		}
		return &PingReqPacket{}, nil

	case PacketTypeSubscribe:
		if flags != 0x02 {
			return nil, errors.New("invalid SUBSCRIBE flags")
		}
		return decodeSubscribe(br, remainingLength)

	case PacketTypePublish:
		qos := (flags >> 1) & 0x03
		if qos != 0 {
			return nil, errors.New("only QoS 0 supported")
		}

		retain := flags&0x01 != 0

		return decodePublish(br, remainingLength, retain)

	case PacketTypeUnsubscribe:
		if flags != 0x02 {
			return nil, errors.New("invalid UNSUBSCRIBE flags")
		}
		return decodeUnsubscribe(br, remainingLength)

	case PacketTypeDisconnect:
		if flags != 0 || remainingLength != 0 {
			return nil, errors.New("invalid DISCONNECT packet")
		}
		return &DisconnectPacket{}, nil

	default:
		return nil, errors.New("unsupported packet type")
	}
}

func decodeConnectFlags(b byte) (connectFlags, error) {
	if b&0x01 != 0 {
		return connectFlags{}, errors.New("reserved connect flag must be 0")
	}

	f := connectFlags{
		cleanSession: b&0x02 != 0,
		willFlag:     b&0x04 != 0,
		willQoS:      (b >> 3) & 0x03,
		willRetain:   b&0x20 != 0,
		usernameFlag: b&0x80 != 0,
		passwordFlag: b&0x40 != 0,
	}

	if !f.willFlag && (f.willQoS != 0 || f.willRetain) {
		return connectFlags{}, errors.New("will QoS and retain must be 0 when will flag is not set")
	}

	if f.passwordFlag && !f.usernameFlag {
		return connectFlags{}, errors.New("password flag requires username flag")
	}

	return f, nil
}

func decodeWill(r io.Reader, qos byte, retain bool) (*WillMessage, error) {
	topic, err := readUTF8String(r)
	if err != nil {
		return nil, err
	}

	payload, err := readBinaryData(r)
	if err != nil {
		return nil, err
	}

	return &WillMessage{Topic: topic, Payload: payload, QoS: qos, Retain: retain}, nil
}

func decodeConnect(r io.Reader, remainingLength int) (*ConnectPacket, error) {
	lr := &io.LimitedReader{R: r, N: int64(remainingLength)}

	// Variable header
	protoName, err := readUTF8String(lr)
	if err != nil {
		return nil, err
	}
	if protoName != "MQTT" {
		return nil, errors.New("invalid protocol name")
	}

	var level [1]byte
	if _, err := io.ReadFull(lr, level[:]); err != nil {
		return nil, err
	}
	if level[0] != 0x04 {
		return nil, errors.New("unsupported protocol level")
	}

	var flagsByte [1]byte
	if _, err := io.ReadFull(lr, flagsByte[:]); err != nil {
		return nil, err
	}
	flags, err := decodeConnectFlags(flagsByte[0])
	if err != nil {
		return nil, err
	}

	var keepAlive uint16
	if err := binary.Read(lr, binary.BigEndian, &keepAlive); err != nil {
		return nil, err
	}

	// Payload
	clientID, err := readUTF8String(lr)
	if err != nil {
		return nil, err
	}
	if clientID == "" && !flags.cleanSession {
		return nil, errors.New("clientID must be present if clean session is false")
	}

	var will *WillMessage
	if flags.willFlag {
		will, err = decodeWill(lr, flags.willQoS, flags.willRetain)
		if err != nil {
			return nil, err
		}
	}

	var username *string
	if flags.usernameFlag {
		u, err := readUTF8String(lr)
		if err != nil {
			return nil, err
		}
		username = &u
	}

	var password *string
	if flags.passwordFlag {
		p, err := readUTF8String(lr)
		if err != nil {
			return nil, err
		}
		password = &p
	}

	if lr.N != 0 {
		return nil, errors.New("malformed CONNECT packet: extra bytes")
	}

	return &ConnectPacket{
		ProtocolName:  protoName,
		ProtocolLevel: level[0],
		CleanSession:  flags.cleanSession,
		KeepAlive:     keepAlive,
		ClientID:      clientID,
		Will:          will,
		Username:      username,
		Password:      password,
	}, nil
}

// decodeSubscribe reads a SUBSCRIBE packet from the given io.Reader.
// It returns a *SubscribePacket and an error if the packet is invalid.
// The remainingLength parameter specifies the number of bytes remaining in the packet.
// If the packet is malformed, an error will be returned.
// If the packet is valid, a *SubscribePacket will be returned with its fields populated.
// The *SubscribePacket will contain the packet identifier and a slice of Subscription objects,
// each of which contains the topic name and QoS level.
func decodeSubscribe(r io.Reader, remainingLength int) (*SubscribePacket, error) {
	lr := &io.LimitedReader{
		R: r,
		N: int64(remainingLength),
	}

	var packetID uint16
	if err := binary.Read(lr, binary.BigEndian, &packetID); err != nil {
		return nil, err
	}

	if packetID == 0 {
		return nil, errors.New("invalid packet identifier")
	}

	var subs []Subscription

	for lr.N > 0 {
		topic, err := readUTF8String(lr)
		if err != nil {
			return nil, err
		}

		var qos [1]byte
		if _, err := io.ReadFull(lr, qos[:]); err != nil {
			return nil, err
		}

		if qos[0] > 2 {
			return nil, errors.New("invalid QoS level")
		}

		subs = append(subs, Subscription{
			Topic: topic,
			QoS:   qos[0],
		})
	}

	if len(subs) == 0 {
		return nil, errors.New("subscribe must contain at least one topic")
	}

	return &SubscribePacket{
		PacketID:      packetID,
		Subscriptions: subs,
	}, nil
}

// decodePublish reads a PUBLISH packet from the given io.Reader.
// It returns a *PublishPacket and an error if the packet is invalid.
// The remainingLength parameter specifies the number of bytes remaining in the packet.
// If the packet is malformed, an error will be returned.
// If the packet is valid, a *PublishPacket will be returned with its fields populated.
// The *PublishPacket will contain the topic name and payload.
func decodePublish(r io.Reader, remainingLength int, retain bool) (*PublishPacket, error) {
	lr := &io.LimitedReader{
		R: r,
		N: int64(remainingLength),
	}

	topic, err := readUTF8String(lr)
	if err != nil {
		return nil, err
	}

	payload := make([]byte, lr.N)
	if _, err := io.ReadFull(lr, payload); err != nil {
		return nil, err
	}

	return &PublishPacket{
		Topic:   topic,
		Payload: payload,
		Retain:  retain,
	}, nil
}

// decodeUnsubscribe reads an UNSUBSCRIBE packet from the given io.Reader.
func decodeUnsubscribe(r io.Reader, remainingLength int) (*UnsubscribePacket, error) {
	lr := &io.LimitedReader{
		R: r,
		N: int64(remainingLength),
	}

	var packetID uint16
	if err := binary.Read(lr, binary.BigEndian, &packetID); err != nil {
		return nil, err
	}

	if packetID == 0 {
		return nil, errors.New("invalid packet identifier")
	}

	var topics []string

	for lr.N > 0 {
		topic, err := readUTF8String(lr)
		if err != nil {
			return nil, err
		}
		topics = append(topics, topic)
	}

	if len(topics) == 0 {
		return nil, errors.New("unsubscribe must contain at least one topic")
	}

	return &UnsubscribePacket{
		PacketID: packetID,
		Topics:   topics,
	}, nil
}

// decodeRemainingLength reads a variable-length integer from the given io.Reader.
// It returns the decoded integer and an error if the packet is invalid.
// The function will return an error if the packet is malformed, or if
// the end of the packet is reached before the integer is complete.
func decodeRemainingLength(r io.Reader) (int, error) {
	multiplier := 1
	value := 0

	for range 4 {
		var encodedByte [1]byte
		if _, err := r.Read(encodedByte[:]); err != nil {
			return 0, err
		}

		digit := int(encodedByte[0])
		value += (digit & 127) * multiplier

		if digit&128 == 0 {
			return value, nil
		}

		multiplier *= 128
	}

	return 0, io.ErrUnexpectedEOF
}
