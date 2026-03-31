package protocol

import (
	"encoding/binary"
	"io"
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

	return string(buf), nil
}
