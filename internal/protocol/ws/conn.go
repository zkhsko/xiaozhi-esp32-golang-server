package ws

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/coder/websocket"
)

var (
	ErrInvalidAudioFrame = errors.New("invalid audio frame")
	ErrMessageTooLarge   = errors.New("message exceeds size limit")
)

type Options struct {
	Version             Version
	MaxTextMessageBytes int64
	MaxOpusPacketBytes  int
}

// Conn exposes text and raw Opus messages over one fixed protocol version.
type Conn struct {
	raw     *websocket.Conn
	options Options
}

func NewConn(raw *websocket.Conn, opts Options) (*Conn, error) {
	if raw == nil {
		return nil, errors.New("websocket connection is required")
	}
	if err := opts.Version.Validate(); err != nil {
		return nil, err
	}
	if opts.MaxTextMessageBytes <= 0 || opts.MaxOpusPacketBytes <= 0 {
		return nil, errors.New("message read limits must be positive")
	}
	headerSize := int64(opts.Version.headerSize())
	if int64(opts.MaxOpusPacketBytes) > math.MaxInt64-headerSize-1 {
		return nil, errors.New("audio read limit is too large")
	}
	readLimit := max(opts.MaxTextMessageBytes, int64(opts.MaxOpusPacketBytes)+headerSize)
	if readLimit == math.MaxInt64 {
		return nil, errors.New("message read limit is too large")
	}
	raw.SetReadLimit(readLimit)
	return &Conn{raw: raw, options: opts}, nil
}

func (c *Conn) Version() Version {
	return c.options.Version
}

func (c *Conn) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	typ, data, err := c.raw.Read(ctx)
	if errors.Is(err, websocket.ErrMessageTooBig) {
		return 0, nil, fmt.Errorf("%w: %w", ErrMessageTooLarge, err)
	}
	if err != nil {
		return 0, nil, err
	}
	if typ == websocket.MessageText {
		if int64(len(data)) > c.options.MaxTextMessageBytes {
			return 0, nil, ErrMessageTooLarge
		}
	} else {
		data, err = decodeAudio(c.options.Version, data, c.options.MaxOpusPacketBytes)
		if err != nil {
			return 0, nil, err
		}
	}
	return typ, data, nil
}

func (c *Conn) Write(ctx context.Context, typ websocket.MessageType, data []byte) error {
	if typ == websocket.MessageBinary {
		var err error
		data, err = encodeAudio(c.options.Version, data)
		if err != nil {
			return err
		}
	}
	return c.raw.Write(ctx, typ, data)
}

func (c *Conn) Close(code websocket.StatusCode, reason string) error {
	return c.raw.Close(code, reason)
}
