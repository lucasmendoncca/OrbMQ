package protocol

import (
	"encoding/binary"
	"io"
)

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
