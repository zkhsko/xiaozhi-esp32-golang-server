# xiaozhi-esp32-golang-server

专为 `xiaozhi-esp32` 打造的轻量级 Go 语言双向语音服务端。固件无需修改源码或私有协议，将 `ota_url` 指向本服务即可使用。

```text
ESP32 (16kHz Opus) -> 服务端解码 -> 百炼流式 ASR (VAD) -> 百炼 LLM (qwen3.7-flash) -> 百炼 TTS -> 编码 (24kHz Opus) -> ESP32 实时播放
```

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
export DEVICE_SHARED_TOKEN="your-device-token"
export DATABASE_DSN="file:xiaozhi-dev.db?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000"
```

### 3. 启动服务

```bash
# 启动
go run ./cmd/server -config config.yaml

# 验证（另开终端）
curl http://127.0.0.1:8080/xiaozhi/ota/
```
返回包含 `websocket` 配置的 JSON 即启动成功。

### 4. 真机接入与测试

1. 确保 ESP32 与电脑处于**同一 Wi-Fi 局域网**；
2. 在设备配网页面将 **OTA URL** 设置为：
   ```text
   http://<电脑局域网IP>:8080/xiaozhi/ota/
   ```
3. 设备开机自动建连，进入待命状态后即可测试：
   - **单轮对话**：按键或唤醒说话，停顿后服务端 VAD 自动断句并实时播报回答；
   - **多轮对话**：播报结束后继续提问，大模型自动结合上文回答；
   - **打断（Abort）**：播放中按键或唤醒，设备立即静音并重置待命。

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
| `database.driver` | 数据库驱动类型（当前支持 sqlite） | `sqlite` |

---

## 常见排查

- **设备连不上**：检查防火墙是否放行 `8080` 端口；确认 `websocket_url` 是实际局域网 IP（不能是 `127.0.0.1`）；
- **报 401 错误**：检查终端是否导出 `DEVICE_SHARED_TOKEN`；
- **无声音或超时**：检查 `DASHSCOPE_API_KEY` 是否有效且有余额；
- **编译报 opus 缺失**：macOS 运行 `export PKG_CONFIG_PATH="/opt/homebrew/lib/pkgconfig:$PKG_CONFIG_PATH"`。

---

## 测试

```bash
# 运行全量测试
go test ./...

# 竞态检测
go test -race ./...
```

设计规范参见 [需求与架构基线](docs/agents/requirements-and-architecture.md)。
