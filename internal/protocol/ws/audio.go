package ws

import (
	"encoding/binary"
	"math"
)

func decodeAudio(version Version, data []byte, maxPayload int) ([]byte, error) {
	headerSize := version.headerSize()
	if len(data) <= headerSize {
		return nil, ErrInvalidAudioFrame
	}
	payload := data[headerSize:]
	if len(payload) > maxPayload {
		return nil, ErrMessageTooLarge
	}
	if version == Version2 {
		if binary.BigEndian.Uint16(data[:2]) != 2 || binary.BigEndian.Uint16(data[2:4]) != 0 ||
			uint64(binary.BigEndian.Uint32(data[12:16])) != uint64(len(payload)) {
			return nil, ErrInvalidAudioFrame
		}
	} else if version == Version3 {
		if data[0] != 0 || uint64(binary.BigEndian.Uint16(data[2:4])) != uint64(len(payload)) {
			return nil, ErrInvalidAudioFrame
		}
	}
	return payload, nil
}

func encodeAudio(version Version, payload []byte) ([]byte, error) {
	if len(payload) == 0 {
		return nil, ErrInvalidAudioFrame
	}
	if version == Version1 {
		return payload, nil
	}
	headerSize := version.headerSize()
	maxPayload := uint64(math.MaxUint32)
	if version == Version3 {
		maxPayload = math.MaxUint16
	}
	if uint64(len(payload)) > maxPayload || len(payload) > math.MaxInt-headerSize {
		return nil, ErrMessageTooLarge
	}
	data := make([]byte, headerSize+len(payload))
	if version == Version2 {
		binary.BigEndian.PutUint16(data[:2], uint16(version))
		binary.BigEndian.PutUint32(data[12:16], uint32(len(payload)))
	} else {
		binary.BigEndian.PutUint16(data[2:4], uint16(len(payload)))
	}
	copy(data[headerSize:], payload)
	return data, nil
}
