package router

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/database"
)

func TestAdminAgentKitConfig_CreateAndList(t *testing.T) {
	db := setupTestRouterDB(t)
	cfg := &config.Config{}
	adminHandler := NewAdminHandler(cfg, db, nil)

	// 1. 创建一条 AgentKit 配置
	reqBody := []byte(`{
		"tool_name": "server.get_current_weather",
		"tool_config": "{\"api_key\":\"seniverse_test_key\",\"location\":\"beijing\"}",
		"enabled": true
	}`)
	req := httptest.NewRequest(http.MethodPost, "/agentkit-config/save", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	adminHandler.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}

	var createResp AdminResponse
	if err := json.Unmarshal(w.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if !createResp.Success {
		t.Fatalf("expected success true")
	}

	// 2. 列表查询
	listReq := httptest.NewRequest(http.MethodGet, "/agentkit-config/list?tool_name=weather", nil)
	listW := httptest.NewRecorder()
	adminHandler.Routes().ServeHTTP(listW, listReq)

	if listW.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", listW.Code, listW.Body.String())
	}

	var listResp struct {
		Success bool                   `json:"success"`
		Data    AgentKitConfigListData `json:"data"`
	}
	if err := json.Unmarshal(listW.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("unmarshal list response failed: %v", err)
	}

	if listResp.Data.Total != 1 || len(listResp.Data.Items) != 1 {
		t.Fatalf("expected total 1, got %d", listResp.Data.Total)
	}

	item := listResp.Data.Items[0]
	if item.ToolName != "server.get_current_weather" {
		t.Fatalf("expected tool_name server.get_current_weather, got %s", item.ToolName)
	}
	if !item.Enabled {
		t.Fatalf("expected enabled true")
	}
}

func TestAdminAgentKitConfig_Update(t *testing.T) {
	db := setupTestRouterDB(t)
	cfg := &config.Config{}
	adminHandler := NewAdminHandler(cfg, db, nil)

	// 先插入初始数据
	initial := &database.AgentKitConfig{
		ToolName:   "server.get_current_time",
		ToolConfig: `{"timezone":"Asia/Shanghai"}`,
		Enabled:    true,
	}
	if err := db.CreateAgentKitConfig(context.Background(), initial); err != nil {
		t.Fatalf("setup AgentKitConfig failed: %v", err)
	}

	// 更新
	updateBody, _ := json.Marshal(map[string]any{
		"id":          initial.Id,
		"tool_name":   "server.get_current_time",
		"tool_config": `{"timezone":"UTC"}`,
		"enabled":     false,
	})
	req := httptest.NewRequest(http.MethodPost, "/agentkit-config/update", bytes.NewReader(updateBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	adminHandler.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}

	found, err := db.FindAgentKitConfigById(context.Background(), initial.Id)
	if err != nil {
		t.Fatalf("FindAgentKitConfigById failed: %v", err)
	}
	if found.ToolConfig != `{"timezone":"UTC"}` {
		t.Fatalf("expected updated config, got %s", found.ToolConfig)
	}
	if found.Enabled {
		t.Fatalf("expected enabled false")
	}
}

func TestAdminAgentKitConfig_DeleteAndBatchDelete(t *testing.T) {
	db := setupTestRouterDB(t)
	cfg := &config.Config{}
	adminHandler := NewAdminHandler(cfg, db, nil)

	// 插入三条数据
	c1 := &database.AgentKitConfig{ToolName: "tool1", ToolConfig: `{}`, Enabled: true}
	c2 := &database.AgentKitConfig{ToolName: "tool2", ToolConfig: `{}`, Enabled: true}
	c3 := &database.AgentKitConfig{ToolName: "tool3", ToolConfig: `{}`, Enabled: true}
	_ = db.CreateAgentKitConfig(context.Background(), c1)
	_ = db.CreateAgentKitConfig(context.Background(), c2)
	_ = db.CreateAgentKitConfig(context.Background(), c3)

	// 单条删除
	delBody, _ := json.Marshal(map[string]any{"id": c1.Id})
	delReq := httptest.NewRequest(http.MethodPost, "/agentkit-config/delete", bytes.NewReader(delBody))
	delReq.Header.Set("Content-Type", "application/json")
	delW := httptest.NewRecorder()
	adminHandler.Routes().ServeHTTP(delW, delReq)

	if delW.Code != http.StatusOK {
		t.Fatalf("delete expected 200, got %d", delW.Code)
	}

	// 批量删除 c2, c3
	batchBody, _ := json.Marshal(map[string]any{"ids": []uint64{c2.Id, c3.Id}})
	batchReq := httptest.NewRequest(http.MethodPost, "/agentkit-config/batch-delete", bytes.NewReader(batchBody))
	batchReq.Header.Set("Content-Type", "application/json")
	batchW := httptest.NewRecorder()
	adminHandler.Routes().ServeHTTP(batchW, batchReq)

	if batchW.Code != http.StatusOK {
		t.Fatalf("batch delete expected 200, got %d", batchW.Code)
	}

	// 验证已全部删除
	list, total, err := db.ListAgentKitConfigs(context.Background(), database.AgentKitConfigFilter{})
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if total != 0 || len(list) != 0 {
		t.Fatalf("expected 0 configs, got %d", total)
	}
}

func TestAdminAgentKitConfig_ValidationAndErrors(t *testing.T) {
	db := setupTestRouterDB(t)
	cfg := &config.Config{}
	adminHandler := NewAdminHandler(cfg, db, nil)

	// 1. 空 ToolName
	body1 := []byte(`{"tool_name":"","tool_config":"{}"}`)
	req1 := httptest.NewRequest(http.MethodPost, "/agentkit-config/save", bytes.NewReader(body1))
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	adminHandler.Routes().ServeHTTP(w1, req1)
	if w1.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty tool_name, got %d", w1.Code)
	}

	// 2. 非法 JSON
	body2 := []byte(`{"tool_name":"test","tool_config":"not a json"}`)
	req2 := httptest.NewRequest(http.MethodPost, "/agentkit-config/save", bytes.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	adminHandler.Routes().ServeHTTP(w2, req2)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid json config, got %d", w2.Code)
	}

	// 3. 重复 ToolName
	body3 := []byte(`{"tool_name":"duplicate_tool","tool_config":"{}"}`)
	req3 := httptest.NewRequest(http.MethodPost, "/agentkit-config/save", bytes.NewReader(body3))
	req3.Header.Set("Content-Type", "application/json")
	w3 := httptest.NewRecorder()
	adminHandler.Routes().ServeHTTP(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("expected 200 on first creation, got %d", w3.Code)
	}

	req4 := httptest.NewRequest(http.MethodPost, "/agentkit-config/save", bytes.NewReader(body3))
	req4.Header.Set("Content-Type", "application/json")
	w4 := httptest.NewRecorder()
	adminHandler.Routes().ServeHTTP(w4, req4)
	if w4.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for duplicate tool_name, got %d", w4.Code)
	}
}
