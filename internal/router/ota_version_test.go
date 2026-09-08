package router

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/protocol/ws"
)

func TestOTADiscoveryProtocolVersion(t *testing.T) {
	db := setupTestRouterDB(t)
	ctx := context.Background()
	const serialNumber = "ota-version-device"
	const deviceId = "11:22:33:44:55:66"
	const clientId = "ota-version-client"
	if _, err := db.ActivateDeviceBySerialNumber(ctx, serialNumber, deviceId, clientId); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpsertDeviceUserRef(ctx, serialNumber, 1); err != nil {
		t.Fatal(err)
	}
	for _, version := range []ws.Version{ws.Version1, ws.Version2, ws.Version3} {
		for _, withSerial := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/sn=%v", version, withSerial), func(t *testing.T) {
				cfg := &config.Config{Server: config.ServerConfig{
					WebSocketURL:     "wss://example.com/xiaozhi/v1/",
					WebSocketVersion: version,
				}}
				handler := NewOTAHandler(cfg, db, nil)
				req := httptest.NewRequest(http.MethodPost, "/", nil)
				req.Header.Set("Device-Id", deviceId)
				req.Header.Set("Client-Id", clientId)
				if withSerial {
					req.Header.Set("Serial-Number", serialNumber)
				}
				response := httptest.NewRecorder()
				handler.Routes().ServeHTTP(response, req)
				if response.Code != http.StatusOK {
					t.Fatalf("OTA status = %d: %s", response.Code, response.Body.String())
				}
				var body Response
				if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body.WebSocket == nil || body.WebSocket.Version != int(version) || body.WebSocket.URL != cfg.Server.WebSocketURL {
					t.Fatalf("unexpected websocket configuration: %+v", body.WebSocket)
				}
			})
		}
	}
}
