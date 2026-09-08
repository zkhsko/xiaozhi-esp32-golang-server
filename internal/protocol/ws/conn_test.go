package ws

import (
	"bytes"
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func testConnection(t *testing.T, opts Options) (*Conn, *websocket.Conn, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	accepted := make(chan *websocket.Conn, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		accepted <- raw
		<-release
	}))
	t.Cleanup(func() { close(release); server.Close() })
	peer, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.CloseNow() })
	var raw *websocket.Conn
	select {
	case raw = <-accepted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	t.Cleanup(func() { _ = raw.CloseNow() })
	conn, err := NewConn(raw, opts)
	if err != nil {
		t.Fatal(err)
	}
	return conn, peer, ctx
}

func TestConnV1Messages(t *testing.T) {
	conn, peer, ctx := testConnection(t, Options{Version: Version1, MaxTextMessageBytes: 128, MaxOpusPacketBytes: 16})
	if conn.Version() != Version1 {
		t.Fatal("connection version differs")
	}
	var retained []byte
	for _, message := range []struct {
		typ  websocket.MessageType
		data []byte
	}{
		{websocket.MessageText, []byte(`{"type":"hello","version":1}`)},
		{websocket.MessageBinary, []byte{0x11, 0x22, 0x33}},
		{websocket.MessageBinary, []byte{0x44, 0x55}},
	} {
		if err := peer.Write(ctx, message.typ, message.data); err != nil {
			t.Fatal(err)
		}
		typ, data, err := conn.Read(ctx)
		if err != nil || typ != message.typ || !bytes.Equal(data, message.data) {
			t.Fatalf("Read = %v %x %v", typ, data, err)
		}
		if message.typ == websocket.MessageBinary && retained == nil {
			retained = data
		}
	}
	if !bytes.Equal(retained, []byte{0x11, 0x22, 0x33}) {
		t.Fatal("subsequent read changed retained audio")
	}
	for _, typ := range []websocket.MessageType{websocket.MessageText, websocket.MessageBinary} {
		data := bytes.Repeat([]byte{0x55}, 256)
		original := bytes.Clone(data)
		if err := conn.Write(ctx, typ, data); err != nil {
			t.Fatal(err)
		}
		gotType, got, err := peer.Read(ctx)
		if err != nil || gotType != typ || !bytes.Equal(got, original) || !bytes.Equal(data, original) {
			t.Fatalf("downlink = %v %x %v", gotType, got, err)
		}
	}
}

func TestConnReadLimits(t *testing.T) {
	for _, tc := range []struct {
		name     string
		typ      websocket.MessageType
		textMax  int64
		audioMax int
		size     int
		want     error
	}{
		{"text", websocket.MessageText, 16, 32, 17, ErrMessageTooLarge},
		{"audio", websocket.MessageBinary, 32, 16, 17, ErrMessageTooLarge},
		{"library", websocket.MessageBinary, 16, 16, 33, ErrMessageTooLarge},
		{"empty_audio", websocket.MessageBinary, 16, 16, 0, ErrInvalidAudioFrame},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, peer, ctx := testConnection(t, Options{Version: Version1, MaxTextMessageBytes: tc.textMax, MaxOpusPacketBytes: tc.audioMax})
			if err := peer.Write(ctx, tc.typ, make([]byte, tc.size)); err != nil {
				t.Fatal(err)
			}
			if _, _, err := conn.Read(ctx); !errors.Is(err, tc.want) {
				t.Fatalf("Read error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestConnRejectsInvalidOptions(t *testing.T) {
	valid := Options{Version: Version1, MaxTextMessageBytes: 128, MaxOpusPacketBytes: 128}
	if _, err := NewConn(nil, valid); err == nil {
		t.Fatal("nil connection accepted")
	}
	conn, _, _ := testConnection(t, valid)
	for _, opts := range []Options{
		{Version: 0, MaxTextMessageBytes: 128, MaxOpusPacketBytes: 128},
		{Version: Version1, MaxTextMessageBytes: 0, MaxOpusPacketBytes: 128},
		{Version: Version1, MaxTextMessageBytes: 128, MaxOpusPacketBytes: -1},
		{Version: Version1, MaxTextMessageBytes: math.MaxInt64, MaxOpusPacketBytes: 128},
	} {
		if _, err := NewConn(conn.raw, opts); err == nil {
			t.Errorf("invalid options accepted: %+v", opts)
		}
	}
}
