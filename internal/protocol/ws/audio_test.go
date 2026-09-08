package ws

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/coder/websocket"
)

func audioVectors(t testing.TB) []struct {
	version Version
	wire    []byte
} {
	t.Helper()
	v2, err := hex.DecodeString("00020000000000000000000000000003112233")
	if err != nil {
		t.Fatal(err)
	}
	return []struct {
		version Version
		wire    []byte
	}{
		{Version1, []byte{0x11, 0x22, 0x33}},
		{Version2, v2},
		{Version3, []byte{0, 0, 0, 3, 0x11, 0x22, 0x33}},
	}
}

func TestAudioVectors(t *testing.T) {
	for _, tc := range audioVectors(t) {
		storage := bytes.Repeat([]byte{0xa5}, 64)
		payload := storage[24:27]
		copy(payload, []byte{0x11, 0x22, 0x33})
		original := bytes.Clone(storage)
		wire, err := encodeAudio(tc.version, payload)
		if err != nil || !bytes.Equal(wire, tc.wire) {
			t.Fatalf("encode version %d: %x, %v", tc.version, wire, err)
		}
		if !bytes.Equal(storage, original) {
			t.Fatal("encoding modified input storage")
		}
		decoded, err := decodeAudio(tc.version, tc.wire, 1024)
		if err != nil || !bytes.Equal(decoded, payload) {
			t.Fatalf("decode version %d: %x, %v", tc.version, decoded, err)
		}
		if _, err := decodeAudio(tc.version, tc.wire, 2); !errors.Is(err, ErrMessageTooLarge) {
			t.Fatalf("payload limit: %v", err)
		}
		if _, err := encodeAudio(tc.version, nil); !errors.Is(err, ErrInvalidAudioFrame) {
			t.Fatalf("empty payload: %v", err)
		}
	}
}

func TestAudioV2Validation(t *testing.T) {
	valid := audioVectors(t)[1].wire
	for _, tc := range []struct {
		name string
		wire []byte
	}{
		{"empty", nil},
		{"short_header", valid[:15]},
		{"empty_payload", valid[:16]},
		{"truncated", valid[:18]},
		{"trailing", append(bytes.Clone(valid), 0x44)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := decodeAudio(Version2, tc.wire, 1024); !errors.Is(err, ErrInvalidAudioFrame) {
				t.Fatalf("decode = %v", err)
			}
		})
	}
	for _, offset := range []int{1, 3, 12, 15} {
		invalid := bytes.Clone(valid)
		invalid[offset] = 0xff
		if _, err := decodeAudio(Version2, invalid, 1024); !errors.Is(err, ErrInvalidAudioFrame) {
			t.Errorf("invalid field at %d: %v", offset, err)
		}
	}
	metadata := bytes.Clone(valid)
	copy(metadata[4:12], []byte{1, 2, 3, 4, 0xff, 0xff, 0xff, 0xff})
	if got, err := decodeAudio(Version2, metadata, 1024); err != nil || !bytes.Equal(got, []byte{0x11, 0x22, 0x33}) {
		t.Fatalf("metadata affected payload: %x, %v", got, err)
	}
}

func TestConnAudioVectors(t *testing.T) {
	for _, tc := range audioVectors(t) {
		conn, peer, ctx := testConnection(t, Options{Version: tc.version, MaxTextMessageBytes: 16, MaxOpusPacketBytes: 3})
		if err := peer.Write(ctx, websocket.MessageBinary, tc.wire); err != nil {
			t.Fatal(err)
		}
		_, payload, err := conn.Read(ctx)
		if err != nil || !bytes.Equal(payload, []byte{0x11, 0x22, 0x33}) {
			t.Fatalf("uplink = %x, %v", payload, err)
		}
		if err := conn.Write(ctx, websocket.MessageBinary, payload); err != nil {
			t.Fatal(err)
		}
		_, wire, err := peer.Read(ctx)
		if err != nil || !bytes.Equal(wire, tc.wire) {
			t.Fatalf("downlink = %x, %v", wire, err)
		}
	}
}

func TestAudioV3Validation(t *testing.T) {
	valid := audioVectors(t)[2].wire
	for _, wire := range [][]byte{nil, valid[:3], valid[:4], valid[:6], append(bytes.Clone(valid), 0x44)} {
		if _, err := decodeAudio(Version3, wire, 1024); !errors.Is(err, ErrInvalidAudioFrame) {
			t.Errorf("invalid v3 packet %x: %v", wire, err)
		}
	}
	for _, offset := range []int{0, 2, 3} {
		wire := bytes.Clone(valid)
		wire[offset] = 0xff
		if _, err := decodeAudio(Version3, wire, 1024); !errors.Is(err, ErrInvalidAudioFrame) {
			t.Errorf("invalid v3 field at %d: %v", offset, err)
		}
	}
	wire := bytes.Clone(valid)
	wire[1] = 0xff
	if payload, err := decodeAudio(Version3, wire, 1024); err != nil || !bytes.Equal(payload, valid[4:]) {
		t.Fatalf("reserved field changed payload: %x, %v", payload, err)
	}
}

func TestAudioV3SizeBoundary(t *testing.T) {
	payload := bytes.Repeat([]byte{0x55}, 65535)
	encoded, err := encodeAudio(Version3, payload)
	if err != nil || len(encoded) != 65539 || !bytes.Equal(encoded[:4], []byte{0, 0, 0xff, 0xff}) {
		t.Fatalf("maximum v3 frame length = %d, error = %v", len(encoded), err)
	}
	decoded, err := decodeAudio(Version3, encoded, 65535)
	if err != nil || !bytes.Equal(decoded, payload) {
		t.Fatalf("maximum v3 payload did not decode: %v", err)
	}
	if _, err := encodeAudio(Version3, append(payload, 0x55)); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("v3 size overflow = %v", err)
	}
}

func FuzzDecodeAudio(f *testing.F) {
	f.Add([]byte{})
	for _, vector := range audioVectors(f) {
		f.Add(vector.wire)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, version := range []Version{Version1, Version2, Version3} {
			payload, err := decodeAudio(version, data, 1024)
			if err != nil {
				continue
			}
			if len(payload) == 0 || len(payload) > 1024 {
				t.Fatalf("invalid decoded payload length %d", len(payload))
			}
			encoded, err := encodeAudio(version, payload)
			if err != nil {
				t.Fatal(err)
			}
			again, err := decodeAudio(version, encoded, 1024)
			if err != nil || !bytes.Equal(again, payload) {
				t.Fatalf("round trip failed: %v", err)
			}
		}
	})
}
