package protocol

import (
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

const (
	MaxRemainingLength = 268435455
	// MaxPacketSize includes the fixed header and the four-byte remaining length.
	MaxPacketSize = MaxRemainingLength + 5
)

// readBinaryData reads a length-prefixed binary field from the given io.Reader.
// The length is encoded as a big-endian uint16 followed by that many bytes.
func readBinaryData(r io.Reader) ([]byte, error) {
	var length uint16
	if err := binary.Read(r, binary.BigEndian, &length); err != nil {
		return nil, err
	}

	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}

	return buf, nil
}

// readUTF8String reads a UTF-8 encoded string from the given io.Reader.
//
// The function will return an error if the packet is malformed, or if
// the end of the packet is reached before the integer is complete.
func readUTF8String(r io.Reader) (string, error) {
	var length uint16
	if err := binary.Read(r, binary.BigEndian, &length); err != nil {
		return "", err
	}

	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}

	value := string(buf)
	if err := validateUTF8String(value); err != nil {
		return "", err
	}
	return value, nil
}

func validateUTF8String(value string) error {
	if len(value) > 65535 || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return errors.New("invalid mqtt utf-8 string")
	}
	return nil
}

func validateTopicName(value string) error {
	if err := validateUTF8String(value); err != nil {
		return err
	}
	if value == "" || strings.ContainsAny(value, "+#") {
		return errors.New("invalid topic name")
	}
	return nil
}

func validateTopicFilter(value string) error {
	if err := validateUTF8String(value); err != nil {
		return err
	}
	if value == "" {
		return errors.New("invalid topic filter")
	}
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case '+':
			if (i > 0 && value[i-1] != '/') || (i+1 < len(value) && value[i+1] != '/') {
				return errors.New("invalid topic filter")
			}
		case '#':
			if (i > 0 && value[i-1] != '/') || i != len(value)-1 {
				return errors.New("invalid topic filter")
			}
		}
	}
	return nil
}
