# xiaozhi-esp32-golang-server

`xiaozhi-esp32-golang-server` 是专为 `xiaozhi-esp32` 硬件固件打造的轻量级 Go 语言双向语音对话服务端。固件无需修改任何源码或通信协议，仅需将 `ota_url` 指向本服务，即可实现端到端语音问答交互。

```text
ESP32 设备语音输入 (16 kHz Opus)
  -> 服务端解码为 PCM
  -> 阿里云百炼流式 ASR (qwen-audio-3.0-asr-flash-streaming) 与服务端 VAD 断句
  -> 阿里云百炼流式 LLM (qwen3.7-flash)
  -> 文本增量按句送入百炼流式 TTS (qwen-audio-3.0-tts-flash, longanlingxi 音色)
  -> 服务端 24 kHz PCM 编码为 60 ms Opus 帧
  -> 设备显示文本并实时播放语音回答
```

---

## 快速开始

### 1. 安装系统依赖

运行需要 Go 1.26+、开启 CGO 及系统 `libopus` 编解码库：

- **macOS**：
  ```bash
  brew install opus pkg-config
  export PKG_CONFIG_PATH="/opt/homebrew/lib/pkgconfig:$PKG_CONFIG_PATH"
  ```
- **Linux (Ubuntu / Debian)**：
  ```bash
  sudo apt-get install -y libopus-dev pkg-config build-essential
  ```

### 2. 准备配置与环境变量

```bash
# 复制配置文件
cp config.example.yaml config.yaml
```

打开 `config.yaml`，将 `websocket_url` 中的 IP 修改为当前机器的**局域网 IP**（如 `192.168.1.100`）：

```yaml
server:
  listen_addr: ":8080"
  websocket_url: "ws://192.168.1.100:8080/xiaozhi/v1/"
```

在终端导出百炼 API Key 与共享设备 Token：

```bash
export DASHSCOPE_API_KEY="sk-your-dashscope-api-key"
export DEVICE_SHARED_TOKEN="your-shared-device-token"
```

### 3. 启动服务与接口自检

```bash
# 本地启动
go run ./cmd/server -config config.yaml

# 验证配置发现接口
curl -i http://127.0.0.1:8080/xiaozhi/ota/
```

返回 `200 OK` 且包含 `websocket` 配置即表示服务端就绪。

### 4. 真实设备（ESP32）接入与语音测试

1. **固件零改动**：确保 ESP32 设备与服务端处于**同一局域网**；
2. **配置设备的 OTA URL**：在设备配网页面或串口配置中将 `ota_url` 设为：
   ```text
   http://<服务端局域网IP>:8080/xiaozhi/ota/
   ```
3. **设备自协商建连**：
   - 设备开机连上 Wi-Fi 后会自动请求 OTA 地址获取 WebSocket URL 与 Token，随后自动完成 WebSocket 握手与 Hello 协商进入就绪状态（`READY`）；
4. **语音交互测试**：
   - **单轮对话**：按键或唤醒设备说话（如“*今天天气怎么样*”），停顿后百炼服务端 VAD 自动断句，服务端流式返回大模型回答并通过 TTS 实时下发语音，设备同步显示文字并播放语音；
   - **多轮对话**：上一轮播放完毕后继续提问（如“*我刚才问了什么*”），大模型将结合前文历史准确回答；
   - **打断（Abort）**：在设备播放语音期间按下打断按键或唤醒设备，播放立即停止，服务端自动取消后端流并重置为就绪状态。

---

## 核心配置说明

| 配置路径 | 说明 | 默认值 / 建议 |
| --- | --- | --- |
| `server.listen_addr` | 服务监听端口 | `:8080` |
| `server.websocket_url` | 下发给设备的 WebSocket 地址 | `ws://<局域网IP>:8080/xiaozhi/v1/`（公网需通过反代填 `wss://`） |
| `server.max_concurrent_sessions` | 最大并发会话数上限 | `10`（必填正整数，满载返回 503） |
| `session.max_history_turns` | 内存历史保留轮数 | `6`（超出按 FIFO 滚动淘汰最旧一轮） |
| `session.system_prompt` | 系统提示词 | 适合语音朗读的简明助手设定 |
| `ai.bailian.asr_model` | 百炼 ASR 模型 | `qwen-audio-3.0-asr-flash-streaming` |
| `ai.bailian.llm_model` | 百炼 LLM 模型 | `qwen3.7-flash` |
| `ai.bailian.tts_model` / `tts_voice` | 百炼 TTS 模型 / 音色 | `qwen-audio-3.0-tts-flash` / `longanlingxi` |
| `proxy.enabled` / `proxy.url` | 出站代理开关与地址 | 默认 `false` 直连，支持 `http`/`socks5h` |

---

## 常见排查

1. **设备无法连接**：确认电脑防火墙允许入站 `8080` 端口；确认 `config.yaml` 中 `websocket_url` 填写的是局域网实际 IP，不能填 `127.0.0.1`；
2. **设备报 401 Unauthorized**：检查启动终端导出的 `DEVICE_SHARED_TOKEN` 环境变量是否已设置；
3. **百炼无声音或超时**：检查 `DASHSCOPE_API_KEY` 是否有效且具有可用额度；
4. **编译报错找不到 opus.h**：确保安装了 `libopus` 和 `pkg-config`，且 `CGO_ENABLED=1`。

---

## 开发与验证

```bash
# 运行全量单元与集成测试
go test ./...

# 运行并发数据竞争检测
go test -race ./...
```

详细架构与协议规范见 [最小语音链路需求与架构基线](docs/agents/requirements-and-architecture.md)。
