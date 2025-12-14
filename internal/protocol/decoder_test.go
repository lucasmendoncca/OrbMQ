package protocol

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeConnect(t *testing.T) {
	tests := []struct {
		name    string
		input   []byte
		wantErr bool
		check   func(t *testing.T, pkt Packet)
	}{
		{
			name: "valid CONNECT",
			input: []byte{
				0x10, 0x15,
				0x00, 0x04, 'M', 'Q', 'T', 'T',
				0x04,
				0x02,
				0x00, 0x3C,
				0x00, 0x09, 'c', 'l', 'i', 'e', 'n', 't', '1', '2', '3',
			},
			wantErr: false,
			check: func(t *testing.T, pkt Packet) {
				conn, ok := pkt.(*ConnectPacket)
				require.True(t, ok, "expected ConnectPacket")

				assert.Equal(t, "MQTT", conn.ProtocolName)
				assert.Equal(t, byte(0x04), conn.ProtocolLevel)
				assert.True(t, conn.CleanSession)
				assert.Equal(t, uint16(60), conn.KeepAlive)
				assert.Equal(t, "client123", conn.ClientID)
			},
		},
		{
			name: "invalid fixed header flags",
			input: []byte{
				0x11, 0x00,
			},
			wantErr: true,
		},
		{
			name: "truncated remaining length",
			input: []byte{
				0x10, 0x05,
				0x00, 0x04, 'M', 'Q',
			},
			wantErr: true,
		},
		{
			name: "extra bytes after payload",
			input: []byte{
				0x10, 0x14,
				0x00, 0x04, 'M', 'Q', 'T', 'T',
				0x04,
				0x02,
				0x00, 0x3C,
				0x00, 0x01, 'a',
				0xFF,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pkt, err := Decode(bytes.NewReader(tt.input))

			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, pkt)

			if tt.check != nil {
				tt.check(t, pkt)
			}
		})
	}
}
