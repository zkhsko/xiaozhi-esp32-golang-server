# xiaozhi-esp32-golang-server

`xiaozhi-esp32-golang-server` 是专为 `xiaozhi-esp32` 硬件固件打造的轻量级 Go 语言双向语音对话服务端。固件无需修改任何源码或通信协议，仅需将 `ota_url` 指向本服务，即可实现端到端语音问答交互。

完整语音交互链路：

```text
ESP32 设备语音输入 (16 kHz Opus)
  -> 服务端解码为 PCM
  -> 阿里云百炼流式 ASR (qwen-audio-3.0-asr-flash-streaming) 与服务端 VAD 端点检测
  -> 阿里云百炼流式大语言模型 (qwen3.7-flash)
  -> 文本增量按句送入阿里云百炼流式语音合成 (qwen-audio-3.0-tts-flash, longanlingxi 音色)
  -> 服务端 24 kHz PCM 编码为 60 ms Opus 帧
  -> 设备显示文本并实时播放语音
```

---

## 目录

- [环境依赖](#环境依赖)
- [配置与环境变量](#配置与环境变量)
- [编译与启动](#编译与启动)
- [设备接入指引](#设备接入指引)
- [网络安全与反向代理](#网络安全与反向代理)
- [常见问题与故障排查](#常见问题与故障排查)
- [首期非目标声明](#首期非目标声明)
- [开发与测试](#开发与测试)
- [架构设计文档](#架构设计文档)

---

## 环境依赖

### 基础技术栈要求

- **Go**：`1.26+`
- **CGO**：必须开启（`CGO_ENABLED=1`），音频编解码模块基于原生 `libopus` C 库实现
- **系统库依赖**：系统必须预装 `libopus` 开发头文件与动态库，以及 `pkg-config` 工具

### 系统依赖安装

#### macOS (Homebrew)

```bash
brew install opus pkg-config
```

> **提示**：如果使用 Apple Silicon (M 系列芯片) 遇到 `pkg-config` 找不到 libopus 的情况，请确保环境变量包含 Homebrew 路径：
> ```bash
> export PKG_CONFIG_PATH="/opt/homebrew/lib/pkgconfig:$PKG_CONFIG_PATH"
> ```

#### Linux (Debian / Ubuntu)

```bash
sudo apt-get update
sudo apt-get install -y libopus-dev libopus0 pkg-config build-essential
```

#### Linux (CentOS / RHEL / Fedora)

```bash
sudo dnf install -y opus-devel pkgconfig gcc
```

---

## 配置与环境变量

### 1. 复制配置文件

服务端非敏感业务参数使用 YAML 文件管理。从示例配置文件复制生成本地配置文件：

```bash
cp config.example.yaml config.yaml
```

### 2. 配置文件说明

`config.yaml` 核心配置项解析：

```yaml
server:
  listen_addr: ":8080"                         # HTTP 与 WebSocket 服务监听地址
  websocket_url: "ws://192.168.1.100:8080/xiaozhi/v1/" # 配置发现下发给设备的 WebSocket 地址（内网直接填 IP，公网填 WSS 域名）
  max_concurrent_sessions: 10                  # 最大并发会话数（必填正整数，超出直接返回 503 拒绝）
  shutdown_timeout: 10s                        # 优雅停机等待超时
  http_read_timeout: 15s                       # HTTP 读取超时
  http_write_timeout: 30s                      # HTTP 写入超时
  http_idle_timeout: 60s                       # HTTP 空闲连接超时
  max_http_body_bytes: 65536                   # HTTP 请求体上限（默认 64 KiB）
  max_http_header_bytes: 1024                  # 单 Header 最大字符数

session:
  hello_timeout: 10s                           # WebSocket 升级后等待客户端 Hello 消息超时
  max_ws_text_message_bytes: 32768             # WebSocket 文本消息上限（默认 32 KiB）
  max_opus_packet_bytes: 1024                  # 单个 Opus 二进制音频包上限（默认 1024 字节）
  max_listening_duration: 30s                  # 单次收音最长持续时间
  asr_pcm_queue_capacity: 100                  # 上行 ASR PCM 队列容量（帧）
  tts_pcm_queue_capacity: 100                  # 下行 TTS PCM 队列容量（块）
  downlink_opus_queue_capacity: 100            # 下行待发送 Opus 队列容量（包）
  max_history_turns: 6                         # 会话内内存历史保留轮数（超出按 FIFO 淘汰最旧一轮）
  system_prompt: "你是小智，一个智能语音助手。请用简明、友好的中文回答，回答适合直接语音朗读。"

ai:
  bailian:
    ws_endpoint: "wss://llm-hi9nns9y8jekpmpt.cn-beijing.maas.aliyuncs.com/api-ws/v1/inference"
    llm_endpoint: "https://llm-hi9nns9y8jekpmpt.cn-beijing.maas.aliyuncs.com/compatible-mode/v1"
    asr_model: "qwen-audio-3.0-asr-flash-streaming"
    llm_model: "qwen3.7-flash"
    tts_model: "qwen-audio-3.0-tts-flash"
    tts_voice: "longanlingxi"
    asr_connect_timeout: 10s                   # 百炼 ASR 建连超时
    tts_connect_timeout: 10s                   # 百炼 TTS 建连超时
    llm_first_token_timeout: 15s               # LLM 首 Token 到达超时
    llm_overall_timeout: 60s                   # LLM 整体响应超时（必须大于首 Token 超时）
    tts_first_audio_timeout: 10s               # TTS 首音频到达超时
    tts_sentence_timeout: 15s                  # TTS 单句合成超时

proxy:
  enabled: false                               # 是否启用出站网络代理
  url: ""                                      # 代理地址（支持 http/https/socks5/socks5h）
```

### 3. 注入敏感环境变量

出于安全合规要求，敏感凭据**严禁**写入配置文件、源码或版本控制中，必须通过环境变量注入：

| 环境变量名 | 描述 | 是否必填 |
| --- | --- | --- |
| `DASHSCOPE_API_KEY` | 阿里云百炼大模型平台 API Key（ASR/LLM/TTS 统一使用） | **必填** |
| `DEVICE_SHARED_TOKEN` | 服务端与设备通信共享 Bearer Token（用于配置发现下发与 WebSocket 鉴权） | **必填** |

在启动终端中导出环境变量（请替换为您自己的有效凭证）：

```bash
export DASHSCOPE_API_KEY="sk-your-valid-dashscope-api-key"
export DEVICE_SHARED_TOKEN="your-secure-device-shared-token"
```

---

## 编译与启动

### 本地直接运行

```bash
go run ./cmd/server -config config.yaml
```

### 编译可执行二进制文件

```bash
# 编译
go build -o bin/xiaozhi-server ./cmd/server

# 启动
./bin/xiaozhi-server -config config.yaml
```

### 验证服务运行状态

服务启动成功后，控制台将输出类似如下日志：

```text
{"time":"...","level":"INFO","msg":"configuration loaded successfully","listen_addr":":8080","websocket_url":"...","max_concurrent_sessions":10,...}
{"time":"...","level":"INFO","msg":"starting HTTP server","addr":":8080"}
```

通过 `curl` 验证配置发现接口：

```bash
curl -i http://127.0.0.1:8080/xiaozhi/ota/
```

预期返回状态码 `HTTP/1.1 200 OK`，且响应体包含对应的 WebSocket 连接配置：

```json
{"websocket":{"url":"ws://192.168.1.100:8080/xiaozhi/v1/","token":"[REDACTED]","version":1}}
```

---

## 设备接入指引

1. **固件零改动**：设备端固件（`xiaozhi-esp32`）无需修改任何源代码或定制私有协议；
2. **配置固件 `ota_url`**：在设备固件的配置项中，将 `ota_url` 指向本服务的配置发现入口：
   ```text
   http://<服务端局域网IP或域名>:<端口>/xiaozhi/ota/
   ```
   *示例*：`http://192.168.1.100:8080/xiaozhi/ota/`
3. **设备自协商流程**：
   - 设备开机连接 Wi-Fi 后，向 `ota_url` 发起 HTTP 请求获取 WebSocket 连接信息与共享 Token；
   - 设备使用返回的 `websocket_url` 发起 WebSocket 握手，携带 `Authorization: Bearer <token>` 凭证与协议版本号；
   - 握手成功后发送 `hello` 进行音频参数确认（上行 16 kHz Opus，下行 24 kHz Opus）；
   - 握手就绪后，设备即可通过按键或唤醒词开始语音对话（默认使用 `auto` 服务端 VAD 收音模式）。

---

## 网络安全与反向代理

1. **内网适用性**：
   - 本服务定位为最小可用语音链路服务端，设计运行于**受信任的局域网**或受保护的私有网络环境；
   - 服务端本身不直接内置 TLS 证书终止能力，默认提供明文 HTTP 与 WS 服务。
2. **公网暴露与反向代理责任**：
   - 若将服务部署于公网或跨公网提供服务，**必须**在服务前端配置受控的反向代理（如 Nginx、Caddy、Traefik 等）；
   - **TLS / WSS 终端**：必须由反向代理负责终止 HTTPS 与 WSS，对公网通信进行强加密传输；
   - **网络防护与限流**：必须由反向代理或 WAF 提供公网 IP 速率限制、防 DDoS 攻击与连接数防护；
   - **WebSocket 代理配置**：确保反向代理正确开启 Connection Upgrade 及合理的长连接保持超时。
3. **设备共享 Token 安全边界**：
   - 环境变量 `DEVICE_SHARED_TOKEN` 仅用于受信任设备的基础接入准入鉴权，不代表强每设备唯一身份认证；
   - 请妥善保管 API Key 与 Token，切勿提交至公开代码仓库或写入日志。

---

## 常见问题与故障排查

### 1. CGO / libopus 缺失或编译报错

- **现象**：
  - `pkg-config --cflags opus` 报错；
  - `fatal error: opus.h: No such file or directory`；
  - `CGO_ENABLED=0` 导致无法编译。
- **排查与解决**：
  1. 确保已通过包管理器安装 `libopus` 和 `pkg-config`（参见[环境依赖](#环境依赖)）；
  2. 确保执行 `go build` 或 `go run` 时 `CGO_ENABLED=1`（Go 默认开启 CGO，若全局设置了 `0` 需显式设为 `1`）；
  3. macOS 用户若使用 Homebrew，请确认 `PKG_CONFIG_PATH` 环境变量指向 Homebrew 的 pkgconfig 路径。

### 2. 缺少必需环境变量

- **现象**：
  - 启动失败并输出：`validate config: credentials: dashscope api key is required (environment variable DASHSCOPE_API_KEY)` 或 `validate config: credentials: device shared token is required (environment variable DEVICE_SHARED_TOKEN)`。
- **排查与解决**：
  - 检查启动当前进程的终端环境中是否已导出 `DASHSCOPE_API_KEY` 和 `DEVICE_SHARED_TOKEN`，不可为空或纯空白字符。

### 3. 配置校验失败或未知字段错误

- **现象**：
  - 启动失败并输出：`decode yaml config: ... field not found` 或 `validate config: ...`。
- **排查与解决**：
  1. 检查 `config.yaml` 字段拼写，YAML 解析器开启了严格未知字段校验（`KnownFields`）；
  2. 检查数值区间是否合法（如 `max_concurrent_sessions` 必须为 `1 ~ 10000` 范围内的正整数）；
  3. 检查超时配置逻辑，`llm_overall_timeout` 必须严格大于 `llm_first_token_timeout`。

### 4. 端口占用（Address already in use）

- **现象**：
  - 启动失败并输出：`listen tcp :8080: bind: address already in use`。
- **排查与解决**：
  - 修改 `config.yaml` 中的 `server.listen_addr` 为其他空闲端口（例如 `:8081`），或排查释放占用 `:8080` 端口的现有进程。

### 5. 设备握手被拒绝（HTTP 503 / 401 / 400）

- **现象**：
  - 设备连接 WebSocket 时失败，服务端返回特定 HTTP 状态码。
- **排查与解决**：
  - **HTTP 503 Service Unavailable**：当前活跃会话数已达到 `server.max_concurrent_sessions` 配置上限，服务端实施了背压防护，可调大上限配置；
  - **HTTP 401 Unauthorized**：设备携带的 Token 与服务端环境变量 `DEVICE_SHARED_TOKEN` 不匹配；
  - **HTTP 400 Bad Request**：设备请求头超过 `max_http_header_bytes` 限制或缺少 `Protocol-Version: 1` 协议头。

### 6. 阿里云百炼调用超时或报错

- **现象**：
  - 语音对话时日志出现 ASR / LLM / TTS 握手或调用超时。
- **排查与解决**：
  1. 检查百炼 API Key 是否有效、是否开通了对应的模型权限并具有可用额度；
  2. 检查运行机器到百炼端点的网络连通性；若在代理网络环境中，可在 `config.yaml` 中配置 `proxy` 参数。

---

## 首期非目标声明

为保持架构极简与核心语音链路稳定，本项目首期明确**不交付**以下功能：

- ❌ **容器镜像与发布流水线**：不交付 Dockerfile、多架构容器镜像或 K8s 部署清单；
- ❌ **数据库与持久化**：不引入 MySQL、Redis 或任何持久化存储，不记录设备档案、历史消息或原始音频文件；
- ❌ **管理后台与管控 API**：不提供 Web 管理后台、OpenAPI / Swagger 文档或管理员 REST 接口；
- ❌ **监控与度量体系**：不提供 Prometheus Metrics 端点或分布式链路追踪平台集成；
- ❌ **每设备身份与激活系统**：不提供每设备独立 Token、设备激活码或绑定机制；
- ❌ **多 AI 服务商动态切换**：不提供多服务商运行时路由、动态降级或热切换能力；
- ❌ **全双工打断与本地 VAD**：不提供本地 Silero/ONNX VAD 模型推理，不支持边播边听的硬件全双工抢断；
- ❌ **固定容量 SLA**：不承诺特定高并发吞吐 SLA，通过有限最大并发数保护进程稳定。

---

## 开发与测试

### 代码格式化与静态检查

```bash
# 格式化 Go 代码
gofmt -s -w .

# 静态语法与 Vet 检查
go vet ./...
```

### 运行单元测试与集成测试

```bash
# 运行全量测试套件
go test -v ./...

# 运行数据竞争检测（修改并发逻辑时必跑）
go test -race ./...
```

### 依赖与已知安全漏洞扫描

```bash
# 依赖漏洞检查
govulncheck ./...
```

---

## 架构设计文档

如需了解完整的协议定义、状态机、队列背压设计以及完成标准，请参考设计基线文档：

- [最小语音链路需求与架构基线](docs/agents/requirements-and-architecture.md)
- [项目概览（人类文档）](docs/humans/project-overview.md)
