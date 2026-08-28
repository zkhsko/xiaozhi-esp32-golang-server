# xiaozhi-esp32-golang-server

专为 `xiaozhi-esp32` 打造的轻量级 Go 语言双向语音服务端。固件无需修改源码或私有协议，将 `ota_url` 指向本服务即可使用。

```text
ESP32 (16kHz Opus) ──> 服务端解码 ──> 百炼 ASR (VAD) ──> 百炼 LLM (qwen3.7-flash)
                                                               │ (支持 MCP / 工具调用)
ESP32 播放 <── 编码 (24kHz Opus) <── 百炼 TTS (按句合成) <───────┘
```

---

## 核心特性

- **双向实时语音**：百炼流式 ASR (VAD)、百炼流式 LLM (`qwen3.7-flash`)、百炼流式 TTS (`qwen-audio-3.0-tts-flash`) 与 Opus 实时下发。
- **设备激活与绑定**：支持 OTA 配置发现、eFuse HMAC 硬件激活、6 位动态码用户绑定与每设备 Access Token 鉴权。
- **扩展与工具调用**：支持设备端 JSON-RPC 2.0 MCP 工具（`tools/list` / `tools/call`）及内置服务端工具（时间查询、会话关闭）。
- **多数据库持久化**：原生支持 SQLite、MySQL 8 与 PostgreSQL，Goose 自动数据库迁移。
- **并发与背压防护**：严格有界的 PCM / Opus 音频缓冲队列、FIFO 多轮对话淘汰与会话数硬上限。

---

## 快速开始

### 1. 安装依赖

需要 Go 1.26+ 与系统 `libopus`：

```bash
# macOS
brew install opus pkg-config

# Linux (Debian / Ubuntu)
sudo apt-get install -y libopus-dev pkg-config
```

### 2. 配置与环境变量

```bash
# 复制配置文件
cp config.example.yaml config.yaml
```

修改 `config.yaml` 中的 `websocket_url` 为本机**局域网 IP**：
```yaml
server:
  listen_addr: ":8080"
  websocket_url: "ws://192.168.1.100:8080/xiaozhi/v1/"
```

在终端设置环境变量（敏感凭据不写入配置文件）：
```bash
export DASHSCOPE_API_KEY="sk-your-dashscope-api-key"

# 数据库连接串（支持 sqlite / mysql / postgres）：
# SQLite:
export DATABASE_DSN="file:xiaozhi-dev.db?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000"
# MySQL 8:
# export DATABASE_DSN="user:password@tcp(127.0.0.1:3306)/xiaozhi?charset=utf8mb4&parseTime=True&loc=Local"
# PostgreSQL:
# export DATABASE_DSN="host=localhost user=postgres password=secret dbname=xiaozhi port=5432 sslmode=disable TimeZone=Asia/Shanghai"
```

### 3. 启动服务

```bash
# 启动
go run ./cmd/server -config config.yaml

# 验证配置发现接口（另开终端）
curl http://127.0.0.1:8080/xiaozhi/ota/
```

---

## 设备接入与绑定流程

1. **出厂凭证生成（可选）**：
   ```bash
   curl -X POST http://127.0.0.1:8080/admin-api/device-hmac-credential/generate \
     -H "Content-Type: application/json" \
     -d '{"count": 1}'
   ```
2. **设备配网**：将 ESP32 的 **OTA URL** 设置为 `http://<局域网IP>:8080/xiaozhi/ota/`。
3. **获取激活码**：未绑定设备请求 OTA 接口将返回 HTTP 401 并在屏幕/串口展示 6 位数字激活码。
4. **用户绑定设备**：
   ```bash
   curl -X POST http://127.0.0.1:8080/user-api/device/bind \
     -H "Content-Type: application/json" \
     -d '{"code": "123456"}'
   ```
5. **自动建连与对话**：绑定成功后设备再次请求 OTA 接口自动获取专属 Access Token，并建立 WebSocket v1 连接进入待命对话状态。

---

## 核心配置

| 配置项 | 说明 | 默认值 |
| --- | --- | --- |
| `server.listen_addr` | 监听端口 | `:8080` |
| `server.websocket_url` | 下发给设备的 WebSocket 地址 | `ws://<局域网IP>:8080/xiaozhi/v1/` |
| `server.max_concurrent_sessions` | 最大并发会话上限 | `10`（超限返回 503） |
| `session.max_history_turns` | 上下文保留轮数 | `6`（FIFO 滚动淘汰） |
| `session.system_prompt` | 系统提示词 | 简明语音助手设定 |
| `ai.bailian.*` | 百炼 ASR / LLM / TTS 模型 | `qwen-audio-3.0-asr-flash-streaming` / `qwen3.7-flash` / `qwen-audio-3.0-tts-flash` |
| `proxy.enabled` | 出站代理开关 | `false` |
| `database.driver` | 数据库驱动类型（`sqlite` / `mysql` / `postgres`） | `sqlite` |

---

## 常见排查

- **设备连不上**：检查防火墙是否放行 `8080` 端口；确认 `websocket_url` 是实际局域网 IP（不能是 `127.0.0.1`）；
- **报 401 错误**：检查设备是否已完成用户绑定并获取到有效 Access Token；数据库中 Token 是否已过期或被撤销；
- **无声音或超时**：检查 `DASHSCOPE_API_KEY` 是否有效且有余额；
- **编译报 opus 缺失**：macOS 运行 `export PKG_CONFIG_PATH="/opt/homebrew/lib/pkgconfig:$PKG_CONFIG_PATH"`。

---

## 安全与生产限制

- **用户体系与管理鉴权（未决事项）**：当前 `/user-api/` 使用模拟用户（`user_id = 1`），`/admin-api/` 未挂载身份拦截，仅适用于受信任内网或由反向代理保护的环境。
- **公网传输**：生产环境必须由前置反向代理（如 Nginx / Envoy）提供 TLS (HTTPS/WSS) 终结保护。

---

## 测试

```bash
# 运行测试
go test ./...

# 竞态检测
go test -race ./...
```

详细规范参见 [架构与需求详细事实基线](docs/agents/requirements-and-architecture.md) 与 [人类项目概览](docs/humans/project-overview.md)。
