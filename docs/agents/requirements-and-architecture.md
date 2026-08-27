# xiaozhi-esp32-golang-server 最小语音链路需求与架构基线

> 目标读者：负责实现、审查和验证本项目的 Agents
> 状态：已完成，已通过全量自动化测试与真实设备全链路端到端验收
> 确认日期：2026-08-20
> 适用项目：`xiaozhi-esp32-golang-server`
> 文档角色：首期需求、架构边界和完成标准的详细事实基线
> Agent 规范：[项目 AGENTS.md](../../AGENTS.md)
> 人类摘要：[项目概览](../humans/project-overview.md)

## 1. 文档目的

本文档定义首期唯一目标：使用最少的服务端能力，为当前 `xiaozhi-esp32` 固件打通真实可用的双向语音对话链路。

首期工作按以下优先级取舍：

1. 先保证真实设备能够完成语音输入和语音回复。
2. ASR、LLM、TTS 保留独立的服务提供商边界，但每项只实现一个阿里云百炼适配器。
3. 不影响语音闭环的能力优先不实现；不可避免的固定选择优先使用代码常量，部署相关值使用配置文件，密钥只使用环境变量。
4. 不为后续阶段预先实现注册中心、插件框架、动态路由、持久化或管理系统。

## 2. 状态与事实来源

首期 Go 服务端生产代码、`go.mod`、配置文件、自动化测试套件以及真实百炼与固件全链路端到端验收均已全部完成并通过验证。

事实优先级如下：

1. 设备协议以当前工作区固件代码为最终事实来源，重点是：
   - `xiaozhi-esp32/main/ota.cc`
   - `xiaozhi-esp32/main/application.cc`
   - `xiaozhi-esp32/main/protocols/protocol.cc`
   - `xiaozhi-esp32/main/protocols/websocket_protocol.cc`
   - `xiaozhi-esp32/docs/websocket_zh.md`
2. 百炼模型、参数和事件协议以阿里云官方文档为事实来源，本文档核对日期为 2026-08-19。
3. 产品范围和取舍以 2026-08-19 确认的人工决策及本文档为准。
4. [人类项目概览](../humans/project-overview.md) 只是摘要；发生冲突时以本文档为准。

## 3. 首期实现目标

首期必须完成以下端到端链路：

```text
设备配置发现
  -> WebSocket v1 建连和 hello
  -> 设备上传 16 kHz Opus
  -> 服务端解码为 PCM
  -> 百炼流式 ASR 和服务端 VAD
  -> qwen3.7-flash 流式回答
  -> 按句送入百炼流式 TTS
  -> 24 kHz PCM 编码为 60 ms Opus 帧
  -> 设备显示文本并播放语音
```

设备固件源码和通信协议不得为接入本服务而修改。部署者只需要把固件的 `ota_url` 配置为本服务的配置发现地址。

首期是语音链路验证版，只用于受信任内网，或由外部反向代理提供 TLS 和访问保护的私有环境。不得宣称本阶段具备公网生产安全能力。

## 4. 已确认决策

| 决策项 | 首期方案 |
| --- | --- |
| 应用形态 | 单 Go module、单进程、单可执行程序 |
| 固件改动 | 不修改源码和协议，只配置 `ota_url` |
| 配置发现 | 直接返回 WebSocket 配置，不执行激活 |
| 设备传输 | 仅 WebSocket |
| 二进制协议 | 仅 v1，二进制消息直接承载单个 Opus 包 |
| 监听模式 | `auto` 必须支持，兼容 `manual`，拒绝 `realtime` |
| 端点检测 | 百炼流式 ASR 自带的服务端 VAD |
| ASR | 百炼 `qwen-audio-3.0-asr-flash-streaming` |
| LLM | 百炼兼容接口、`qwen3.7-flash`、流式输出、关闭思考 |
| TTS | 百炼 `qwen-audio-3.0-tts-flash`、音色 `longanlingxi` |
| TTS 输出 | 24000 Hz、16-bit、有符号小端、单声道 PCM |
| 文本到语音 | LLM 增量文本按句送入一个回答级 TTS 流 |
| 中断 | 支持设备显式 `abort`；不支持全双工说话抢断 |
| 对话上下文 | 仅保存当前 WebSocket 会话的有限内存上下文 |
| 设备认证 | 基于数据库 device_access_token 表查询校验 Bearer Token |
| 持久化 | 不使用数据库，不持久化设备、消息或音频 |
| 并发 | 不承诺固定容量；部署时必须配置正整数保护上限 |
| 部署交付 | 开发机可直接启动；首期不交付容器和多架构镜像 |
| 完成依据 | 真实设备、真实百炼调用和聚焦的自动化测试 |

## 5. 交付范围

### 5.1 必须实现

- 同时兼容固件配置发现使用的 `GET` 和 `POST`。
- 返回固定协议版本的 WebSocket URL 和共享 Token。
- WebSocket Bearer Token 校验、客户端 hello 校验和服务端 hello 响应。
- WebSocket 二进制协议 v1 的 Opus 上行和下行。
- `auto` 模式的流式识别与服务端 VAD 端点检测。
- `manual` 模式的 `listen.stop` 收音结束处理。
- 最终识别文本下发、流式 LLM、按句 TTS 和实时节奏音频下发。
- 显式 `abort`、连接断开和服务停止时的取消与资源清理。
- 同一连接内的有限多轮上下文。
- 配置校验、资源上限、超时和必要的结构化日志。
- 关键协议、取消路径和百炼适配器的自动化测试。
- 当前真实设备与真实百炼服务的端到端验收。

### 5.2 明确不实现

- v1 激活码、v2 challenge/HMAC、设备注册和设备绑定。
- 每设备 Token、Token 轮换、撤销、禁用和凭证生命周期。
- MySQL、其他数据库、migration、ORM、消息历史和长期记忆。
- 管理 REST API、HTTP Basic、OpenAPI、管理 UI 和设备查询。
- WebSocket 二进制协议 v2、v3，以及 MQTT + UDP。
- `realtime` 监听模式、服务端 AEC 和全双工说话抢断。
- 本地 Silero VAD、ONNX Runtime、GPU 推理和本地 ASR/TTS。
- MCP、System、Custom、Glyph Push、摄像头和设备工具调用。
- 固件上传、固件版本管理、OTA 二进制下载和远程重启。
- 多 AI 服务商实现、运行时服务商切换、自动降级和故障转移。
- RAG、多智能体、长期记忆、声纹识别和原始音频存储。
- Prometheus 指标、完整健康管理和可观测平台集成。
- Docker、`amd64/arm64` 多架构镜像和发布流水线。
- 多实例部署、会话迁移、共享状态和容量 SLA。
- 直接暴露公网所需的生产级认证、限流和密钥生命周期管理。

后续需求必须独立评审，不得通过首期占位代码提前实现。

## 6. 最小架构

### 6.1 进程与 package

应用采用模块化单体。建议的最小职责边界如下，只有出现生产调用时才创建对应 package：

- `cmd/server`：读取配置、构造依赖、启动 HTTP 服务和优雅退出。
- `internal/config`：严格解析 YAML 和环境变量并完成启动校验。
- `internal/router`：处理 HTTP 路由、OTA 配置发现及 WebSocket 升级入口。
- `internal/session`：WebSocket 生命周期、状态机、对话编排和取消。
- `internal/audio`：Opus 编解码、PCM 分帧和有界音频队列。
- `internal/ai`：ASR、LLM、TTS 的最小使用方接口和通用值类型。
- `internal/ai/bailian`：首期三个百炼适配器。

不得创建 `storage`、`admin`、`migration`、`openapi`、`provider registry` 或其他没有首期生产调用的目录与类型。

### 6.2 依赖方向

依赖方向固定为：

```text
HTTP / WebSocket 入口
  -> session 用例
  -> audio 与 ai 能力边界
  -> bailian 外部适配器
```

- `session` 不得依赖百炼事件结构、OpenAI SDK 类型或 WebSocket SDK 类型。
- 百炼适配器负责把供应商事件转换为项目内的识别结果、文本增量和 PCM 音频块。
- 音频 package 不感知 ASR、LLM、TTS 供应商。
- 依赖在 `cmd/server` 中显式创建；不引入依赖注入框架或可变全局单例。

### 6.3 服务商扩展边界

ASR、LLM、TTS 必须是三个独立的小接口，不创建统一的“大模型供应商”接口。

- ASR 边界：创建一轮流式识别会话、写入 PCM、通知输入结束、读取最终文本、取消。
- LLM 边界：输入内部消息列表，流式返回文本增量，支持取消。
- TTS 边界：创建一个回答级合成流、按顺序写入完整句子、结束输入、流式返回 PCM、取消。

接口只表达服务端用例需要的行为，不暴露百炼或 OpenAI SDK 类型。首期在装配代码中直接构造百炼实现，不增加服务商名称配置、工厂、注册表或动态选择逻辑。第二个真实供应商进入范围后再增加选择机制。

## 7. 配置发现

### 7.1 请求

配置发现使用固定路径 `GET|POST /xiaozhi/ota/`。部署者将该完整地址写入固件 `ota_url`。

- `POST` 正文是固件系统信息；`GET` 用于没有正文的设备。
- 请求正文最大 64 KiB（65536 字节，配置项 `max_http_body_bytes`，合法范围 1 KiB ~ 10 MiB）。使用 `http.MaxBytesReader` 限制，超限立即中止读取并返回 HTTP 413 Payload Too Large；空正文允许。
- 读取并限制 `Activation-Version`、`Device-Id`、`Client-Id`、`Serial-Number`、`User-Agent` 和 `Accept-Language` 等请求头长度（单 Header 上限 1024 字符，配置项 `max_http_header_bytes`，合法范围 128 ~ 8192 字符；全部 Header 汇总上限 8192 字节）。超出限制直接返回 HTTP 400 Bad Request 或 HTTP 431 Request Header Fields Too Large。
- 这些请求头只用于有限诊断日志，不参与身份认证、唯一性判断或数据保存。
- 不因 `Activation-Version` 为 1 或 2 进入不同业务流程。

### 7.2 响应

成功响应固定为 HTTP 200 和以下最小 JSON：

```json
{
  "websocket": {
    "url": "wss://example.com/xiaozhi/v1/",
    "token": "shared-device-token",
    "version": 1
  }
}
```

- `url` 来自可信服务端配置，不根据客户端 `Host` 或任意转发头拼接。
- `token` 来自进程环境变量，每次配置发现都返回同一个值。
- `version` 固定为 1，不提供版本配置项。
- 不返回 `activation`、`mqtt`、`firmware` 或其他占位对象。
- 响应和日志不得暴露 Token 之外的其他敏感配置；日志不得记录 Token。

## 8. WebSocket v1 契约

### 8.1 握手

WebSocket 使用固定路径 `/xiaozhi/v1/`。

- 要求 `Authorization: Bearer <token>` 与环境变量中的共享 Token 匹配。
- 要求 `Protocol-Version` 等于 `1`。
- 接收 `Device-Id` 和 `Client-Id`，单个 Header 长度上限 1024 字符（超限拒绝握手），只写入必要的会话日志字段。
- 不要求 `Serial-Number`，保证没有序列号的当前 v1 固件能够接入。
- 禁用 WebSocket 压缩；Opus 数据不得重复压缩。
- Token 错误、协议版本错误或达到会话保护上限（`max_concurrent_sessions`）时在升级前拒绝；达到会话上限时直接返回 HTTP 503 Service Unavailable。

### 8.2 hello

连接升级后，设备必须在 10 秒内（配置项 `hello_timeout`，默认 10s，合法范围 3s ~ 30s）发送文本 hello。超限以 WebSocket 1008 (Policy Violation) 关闭连接。服务端至少校验：

- `type` 等于 `hello`。
- `transport` 等于 `websocket`。
- `version` 等于 1。
- `audio_params.format` 等于 `opus`。
- `audio_params.sample_rate` 等于 16000。
- `audio_params.channels` 等于 1。
- `audio_params.frame_duration` 等于 60。

服务端成功后立即返回：

```json
{
  "type": "hello",
  "transport": "websocket",
  "session_id": "generated-session-id",
  "audio_params": {
    "format": "opus",
    "sample_rate": 24000,
    "channels": 1,
    "frame_duration": 60
  }
}
```

`session_id` 由服务端随机生成，只在当前连接内使用。hello 完成前不得发起百炼请求。

### 8.3 文本消息

首期只处理以下设备消息（WebSocket 文本消息上限 32 KiB / 32768 字节，配置项 `max_ws_text_message_bytes`，合法范围 4 KiB ~ 512 KiB；超限以 WebSocket 1009 Message Too Big 关闭连接）：

- `{"type":"hello", ...}`。
- `{"type":"listen","state":"start","mode":"auto"}`。
- `{"type":"listen","state":"start","mode":"manual"}`。
- `{"type":"listen","state":"stop"}`。
- `{"type":"listen","state":"detect","text":"..."}`；只做有界解析和诊断，不实现声纹或唤醒词业务。
- `{"type":"abort", ...}`。

首期只发送以下服务端消息：

- `hello`。
- `{"session_id":"...","type":"stt","text":"..."}`。
- `{"session_id":"...","type":"tts","state":"start"}`。
- `{"session_id":"...","type":"tts","state":"sentence_start","text":"..."}`。
- `{"session_id":"...","type":"tts","state":"stop"}`。

不得发送 `llm` 表情、MCP、System、Custom、Glyph Push 或其他扩展消息。格式正确但不在范围内的扩展消息只做限频诊断（同一连接每秒最多 1 条诊断日志，burst 3 条）并忽略；除第 8.4 节明确允许丢弃的音频外，格式错误、超限或违反当前状态的消息关闭连接。

### 8.4 二进制音频

- 每个 WebSocket binary message 必须且只能包含一个原始 Opus 包。
- 单包大小上限 1024 字节（配置项 `max_opus_packet_bytes`，合法范围 128 ~ 4096 字节）。收到超过上限或 0 字节的空包视为协议错误，立即以 WebSocket 1003 (Unsupported Data) 或 1008 (Policy Violation) 关闭连接。
- 不解析或生成额外二进制头、时间戳、版本字段和 payload 长度字段。
- 上行 Opus 按 hello 中的 16000 Hz、单声道、60 ms 解码。
- 下行 Opus 按服务端 hello 中的 24000 Hz、单声道、60 ms 编码。
- 固件启用唤醒词音频上传时可能在 `listen.start` 前发送少量 Opus；服务端在 `READY` 状态直接丢弃，不缓存、不启动 ASR。
- ASR 已产生最终结果后，服务端直接丢弃设备在首次 `tts.start` 前继续发送的本轮 Opus，不送入下一轮识别。
- 空包、解码失败和超过配置上限的包视为协议错误。
- 需要跨异步边界保存的数据必须复制到有界队列，不得引用 WebSocket 回调生命周期外的缓冲区。

## 9. 会话与语音流程

### 9.1 状态机

每个连接至少维护以下状态：

```text
CONNECTED -> READY -> LISTENING -> PROCESSING -> SPEAKING
                 ^                       |           |
                 +-----------------------+-----------+
```

任何状态都可以因取消、错误或断开进入 `CLOSED`。

- 单个会话监督流程独占状态和当前回答代次。
- 同一连接只允许一轮当前识别和一轮当前回答。
- 非法状态消息不得隐式启动第二个任务。
- 所有异步结果都携带回答代次；旧代次结果必须丢弃。

### 9.2 上行与 ASR

1. 收到 `listen.start` 后创建百炼 ASR 流并进入 `LISTENING`。
2. 二进制 Opus 包解码为 16000 Hz、16-bit、有符号小端、单声道 PCM。
3. PCM 按顺序写入 `qwen-audio-3.0-asr-flash-streaming`。
4. `auto` 模式使用百炼默认启用的服务端 VAD；不加载本地 VAD。
5. `manual` 模式收到 `listen.stop` 后通知 ASR 输入结束。
6. 只使用非空最终识别文本；部分结果只用于内部状态，不下发设备、不进入上下文。
7. 收到最终文本后停止接收本轮 ASR 音频、发送 `stt`，进入 `PROCESSING`。

ASR 连接和音频队列必须在超时、`abort`、设备断开或服务停止时立即取消。不得缓存整段原始音频、生成 WAV 或写入磁盘。

### 9.3 LLM 与上下文

- LLM 使用百炼 OpenAI 兼容 Chat Completions 接口和 OpenAI 官方 Go SDK。
- 模型固定配置为 `qwen3.7-flash`，请求启用流式输出并显式关闭思考模式。
- 上下文只包含配置中的系统提示词，以及当前连接最近有限轮已完成对话。
- 默认最多保留最近 6 轮，配置必须是有限正整数；超出后从最旧完整轮次开始删除。
- 只有用户文本和完整助手回答组成的一轮成功结束后才写入内存历史。
- 被取消或失败的助手部分回答不得进入后续上下文。
- 不发送工具定义，不处理 function calling，不调用 MCP。
- 禁用 SDK 隐式重试；流式输出开始后禁止重新生成。

### 9.4 按句 TTS

1. LLM 文本增量进入轻量分句器。
2. 遇到中文或英文句末标点时输出完整句；长时间没有句末时按固定且有界的字符数切分，防止首音频无限等待。
3. 句子按顺序写入一个回答级百炼 TTS 流。
4. TTS 使用 `qwen-audio-3.0-tts-flash` 和 `longanlingxi`，请求 24000 Hz、16-bit、有符号小端、单声道 PCM。
5. 首个有效 PCM 到达前发送一次 `tts.start`；每个完整句子写入 TTS 流前发送一次对应的 `tts.sentence_start`，确保文本消息先于该句音频。
6. PCM 每 1440 个采样点组成 60 ms 帧，编码为单个 Opus 包；最后不足一帧时最多补一帧静音。
7. 下行按 60 ms 实时节奏发送，待发送音频队列必须有界。
8. 全部文本和音频完成后发送一次 `tts.stop`，再提交该轮内存上下文。

不得请求 MP3 后再解码，也不得直接把供应商返回的 Opus 数据当作设备帧。服务端必须控制设备所需的采样率、帧长和 Opus 包边界。

### 9.5 中断与失败

收到 `abort` 时必须：

1. 增加回答代次并取消当前 ASR、LLM 和 TTS context。
2. 关闭或终止对应的百炼流。
3. 清空尚未发送的旧文本和音频。
4. 丢弃迟到的供应商事件和编码结果。
5. 如果已经发送 `tts.start`，发送一次 `tts.stop`。
6. 保持连接可用于下一轮 `listen.start`。

不实现麦克风边播边听形成的全双工抢断。设备断开时必须完成相同资源清理，但不再写 WebSocket。

错误行为固定如下：

- ASR 或 LLM 在 `tts.start` 前失败：记录一次错误并关闭设备连接，使固件回到 idle 后重连。
- TTS 或下行音频在 `tts.start` 后失败：尽力发送 `tts.stop`，随后关闭连接。
- 配置、Opus 初始化或必需依赖失败：启动失败，不监听端口。
- 不执行供应商自动降级、无限重试或重复回答。

## 10. 并发与资源边界

首期不声明固定并发容量，也不做容量 SLA，但运行时所有资源、队列、消息和超时均必须具备确定数值的硬边界，严禁出现无界分配或隐式无限等待。

### 10.1 资源边界基准总表

下表为首期系统全部资源边界、超时、队列和容量的确定数值与行为规范：

| 边界分类 | 配置键名 / 常量名 | 类型 / 单位 | 默认值 / 必填规则 | 校验合法范围 | 超限 / 失败行为 |
| --- | --- | --- | --- | --- | --- |
| 并发会话 | `max_concurrent_sessions` | 整数（个） | **必填正整数**（无默认值） | `[1, 10000]` | 达到上限时拒绝新握手，HTTP 握手阶段返回 `503 Service Unavailable` |
| HTTP 请求体 | `max_http_body_bytes` | 整数（字节） | `65536`（64 KiB） | `[1024, 10485760]` (1 KiB ~ 10 MiB) | `http.MaxBytesReader` 限制，超限中止读取并返回 `413 Payload Too Large` |
| 单 Header 长度 | `max_http_header_bytes` | 整数（字符/字节） | `1024` 字符 | `[128, 8192]` | 单个请求头超限直接返回 `400 Bad Request` 或 `431 Request Header Fields Too Large` |
| WS 文本消息 | `max_ws_text_message_bytes` | 整数（字节） | `32768`（32 KiB） | `[4096, 524288]` (4 KiB ~ 512 KiB) | WebSocket ReadLimit 限制，超限发送 `1009 (Message Too Big)` 并关闭连接 |
| 上行 Opus 单包 | `max_opus_packet_bytes` | 整数（字节） | `1024` 字节 | `[128, 4096]` | 收到超过 1024 字节或 0 字节二进制包，发送 `1003/1008` 并关闭连接 |
| ASR PCM 队列 | `asr_pcm_queue_capacity` | 整数（帧） | `100` 帧 (约 6.0s 音频) | `[20, 500]` | ASR 写入阻塞导致队列满时触发背压，丢弃新帧；阻塞超 5s 则取消 ASR 并关闭连接 |
| TTS PCM 队列 | `tts_pcm_queue_capacity` | 整数（块） | `100` 块 | `[20, 500]` | 编码消费阻塞导致队列满时暂停读取；阻塞超 5s 则取消 TTS 并关闭连接 |
| 下行 Opus 队列 | `downlink_opus_queue_capacity` | 整数（包） | `100` 包 (约 6.0s 播放) | `[20, 500]` | 写设备受阻导致下行队列积压满时，触发背压保护，记录 WARN 并主动关闭连接 |
| 单次收音时长 | `max_listening_duration` | 时间（秒） | `30s` | `[5s, 120s]` | 持续收音超 30s 仍未断句，立即停止收音，取消本轮 ASR 并关闭连接 |
| 客户端 Hello 超时 | `hello_timeout` | 时间（秒） | `10s` | `[3s, 30s]` | 升级后 10s 内未收到合法 hello，以 `1008` 关闭连接 |
| ASR 建连超时 | `asr_connect_timeout` | 时间（秒） | `10s` | `[3s, 30s]` | 百炼 ASR WebSocket 握手建连超时，记录 ERROR 并关闭设备连接 |
| TTS 建连超时 | `tts_connect_timeout` | 时间（秒） | `10s` | `[3s, 30s]` | 百炼 TTS WebSocket 握手建连超时，记录 ERROR 并关闭设备连接 |
| LLM 首 token 超时 | `llm_first_token_timeout` | 时间（秒） | `15s` | `[3s, 30s]` | 15s 未收到 LLM 首个增量 chunk，取消 LLM，记录 ERROR 并关闭设备连接 |
| LLM 整体超时 | `llm_overall_timeout` | 时间（秒） | `60s` | `[10s, 180s]`（必须 > 首 token 超时） | LLM 流总耗时超 60s，取消 LLM，记录 ERROR 并关闭设备连接 |
| TTS 首音频超时 | `tts_first_audio_timeout` | 时间（秒） | `10s` | `[3s, 30s]` | 首句送入 TTS 后 10s 未收到首个 PCM 块，取消 TTS 并关闭设备连接 |
| TTS 单句合成超时 | `tts_sentence_timeout` | 时间（秒） | `15s` | `[5s, 60s]` | 单句 TTS 合成耗时超限，取消 TTS 并关闭设备连接 |
| HTTP 读取超时 | `http_read_timeout` | 时间（秒） | `15s` | `[5s, 60s]` | HTTP Server ReadTimeout 超限中断读取 |
| HTTP 写入超时 | `http_write_timeout` | 时间（秒） | `30s` | `[5s, 60s]` | HTTP Server WriteTimeout 超限中断写入 |
| HTTP 空闲超时 | `http_idle_timeout` | 时间（秒） | `60s` | `[10s, 300s]` | HTTP Keep-Alive 空闲超时关闭连接 |
| 优雅关闭宽限期 | `shutdown_timeout` | 时间（秒） | `10s` | `[1s, 60s]` | 进程优雅退出等待所有会话清理的最大时长，超限强制退出 |
| 内存历史轮数 | `max_history_turns` | 整数（轮） | `6` 轮 | `[1, 50]` | 成功轮次超限时按 FIFO 滚动淘汰最旧的一整轮对话（1 问 1 答） |
| 日志字段截断 | `log_truncate_limit` | 整数（字符） | `64` 字符（**代码常量**） | 固定 `64` | 日志记录的设备 ID、消息片段等超长字段截断为 64 字符 + `...` |
| 诊断日志限频 | `diag_rate_limit` | 频率（条/秒） | `1 msg/s`, burst `3`（**代码常量**） | 固定 | 未知扩展消息或诊断日志超过限频阈值时静默丢弃或聚合计数，防日志刷屏 |

### 10.2 队列与背压设计

1. **ASR PCM 缓冲队列**：
   - 上行 Opus 解码后生成 16000 Hz 16-bit 单声道 PCM 帧（每帧 60 ms / 960 采样点 / 1920 字节），送入容量为 100 帧的有界 channel。
   - 消费协程持续从队列读取并通过 WebSocket binary 帧写入百炼 ASR。
   - 若百炼 ASR 写入阻塞导致队列满（积压约 6.0 秒音频），背压机制丢弃后续新 PCM 帧并记录 WARN；若单次阻塞超过 5 秒，判定百炼 ASR 链路不可用，主动取消 ASR 任务并关闭设备连接。
2. **TTS PCM 缓冲队列**：
   - 百炼 TTS 产生的 PCM 音频块送入容量为 100 块的有界 channel。
   - 编码协程持续取出 PCM 块组装为 24000 Hz 60 ms PCM 帧（每帧 1440 采样点 / 2880 字节）并进行 Opus 编码。
   - 队列满时暂停 TTS 读取协程；若阻塞超过 5 秒未恢复，取消 TTS 任务并记录异常。
3. **下行 Opus 串行写队列**：
   - 编码后的 60 ms Opus 包按实时时序推入容量为 100 包（可缓冲 6.0 秒音频）的待发送队列。
   - 会话内唯一串行写入协程负责将 Opus 包发送至设备 WebSocket。
   - 若设备网络缓慢或断开导致下行队列积压满 100 包，判定下行通道严重阻塞，立即触发背压保护，记录 WARN 并主动关闭设备连接，防止服务端内存泄露。

### 10.3 超时与生命周期

1. **会话级超时**：
   - 握手后 10 秒内未收到合法 `hello`，断开连接。
   - 收音状态下，单次收音超过 30 秒未触发 VAD 结束或收到 `listen.stop`，超时取消收音并关闭连接。
2. **外部服务调用超时**：
   - 百炼 ASR / TTS WebSocket 建连必须在 10 秒内完成。
   - 百炼 LLM 首 token（TTFT）必须在 15 秒内到达；LLM 整体文本流耗时不得超过 60 秒（启动校验强制验证 `llm_overall_timeout > llm_first_token_timeout`）。
   - 百炼 TTS 写入首句后必须在 10 秒内收到首个 PCM 块；单句合成耗时不得超过 15 秒。
   - 任何外部超时均通过 `context.WithTimeout` 严格实施，超时后立即取消下游并在会话边界记录错误。
3. **服务生命周期超时**：
   - HTTP 基础参数配置：`ReadTimeout` 15s、`WriteTimeout` 30s、`IdleTimeout` 60s。
   - 优雅退出宽限期 10s：收到终止信号后，停止接收入口请求，广播取消所有活跃会话，在 10 秒内等待资源清理完成并退出。

### 10.4 并发控制与多轮对话

1. **并发控制**：
   - 部署者必须显式配置 `max_concurrent_sessions`（正整数，无默认值，如 `10`）。
   - 采用进程内带缓冲 channel 或信号量严格限制活跃 WebSocket 会话数量。
   - 当活跃会话数达到上限时，新 WebSocket 握手请求在 HTTP 阶段直接返回 `503 Service Unavailable`，不执行协议升级。
2. **多轮对话上下文淘汰**：
   - 会话内仅在内存中保留最近有限轮完整对话（默认 6 轮，合法范围 1 ~ 50 轮）。
   - 一轮对话仅在用户识别文本与助手完整回复均成功结束后才提交写入。
   - 对话轮数超过配置上限时，按先进先出（FIFO）原则滚动丢弃最旧的一整轮对话（1 条 user 消息 + 1 条 assistant 消息），系统提示词始终保持在最前。

### 10.5 不可变协议参数（代码常量）

以下参数属于固件通信与音频编解码协议契约，固定为代码常量，严禁作为外部可变配置项：

- `ProtocolVersion = 1`
- 上行音频参数：`SampleRate = 16000` Hz，`Channels = 1`（单声道），`FrameDurationMs = 60` ms（每帧 `960` 采样点 / `1920` 字节 16-bit PCM）
- 下行音频参数：`SampleRate = 24000` Hz，`Channels = 1`（单声道），`FrameDurationMs = 60` ms（每帧 `1440` 采样点 / `2880` 字节 16-bit PCM）
- PCM 编码格式：`16-bit signed little-endian`

## 11. 配置

### 11.1 YAML

非敏感配置使用单一 YAML 文件，结构与字段如下：

```yaml
server:
  listen_addr: ":8080"
  websocket_url: "wss://example.com/xiaozhi/v1/"
  max_concurrent_sessions: 10          # 必填正整数，无默认值
  shutdown_timeout: 10s                # 默认 10s
  http_read_timeout: 15s               # 默认 15s
  http_write_timeout: 30s              # 默认 30s
  http_idle_timeout: 60s               # 默认 60s
  max_http_body_bytes: 65536           # 默认 64 KiB
  max_http_header_bytes: 1024          # 默认 1024 字符

session:
  hello_timeout: 10s                   # 默认 10s
  max_ws_text_message_bytes: 32768     # 默认 32 KiB
  max_opus_packet_bytes: 1024          # 默认 1024 字节
  max_listening_duration: 30s          # 默认 30s
  asr_pcm_queue_capacity: 100          # 默认 100 帧
  tts_pcm_queue_capacity: 100          # 默认 100 块
  downlink_opus_queue_capacity: 100    # 默认 100 包
  max_history_turns: 6                 # 默认 6 轮
  system_prompt: "你是小智，一个智能语音助手。请用简明、友好的中文回答，回答适合直接语音朗读。"

ai:
  bailian:
    ws_endpoint: "wss://llm-hi9nns9y8jekpmpt.cn-beijing.maas.aliyuncs.com/api-ws/v1/inference"
    llm_endpoint: "https://llm-hi9nns9y8jekpmpt.cn-beijing.maas.aliyuncs.com/compatible-mode/v1"
    asr_model: "qwen-audio-3.0-asr-flash-streaming"
    llm_model: "qwen3.7-flash"
    tts_model: "qwen-audio-3.0-tts-flash"
    tts_voice: "longanlingxi"
    asr_connect_timeout: 10s           # 默认 10s
    tts_connect_timeout: 10s           # 默认 10s
    llm_first_token_timeout: 15s       # 默认 15s
    llm_overall_timeout: 60s           # 默认 60s
    tts_first_audio_timeout: 10s       # 默认 10s
    tts_sentence_timeout: 15s          # 默认 15s
```

YAML 必须严格映射到明确 struct（启用未知字段检查 `Decoder.KnownFields(true)`）。以下情况启动校验必须直接返回错误并拒绝启动：
- 包含未在 struct 中定义的未知 YAML 字段。
- 缺失必需环境变量（`DASHSCOPE_API_KEY`、`DATABASE_DSN`）。
- 缺失必填字段（如 `max_concurrent_sessions`）或字段值为非正数（<= 0）。
- 任何数值或时长超出第 10.1 节定义的合法范围。
- 超时逻辑矛盾（如 `llm_overall_timeout <= llm_first_token_timeout`）。
- 非法 URL 格式或空监听地址。
- 不实现配置热更新，配置变更需重启进程。

### 11.2 环境变量

敏感值只允许通过环境变量注入：

- `DASHSCOPE_API_KEY`：百炼 ASR、LLM、TTS 共用的当前有效 Key。
- `DATABASE_DSN`：数据库连接串（支持 SQLite / MySQL / PostgreSQL）。

不得把真实值写入源码、YAML、测试数据、文档、日志、错误响应或命令示例。任何曾出现在聊天、日志或仓库中的 Key 都视为已泄露，必须撤销并重新生成后才能使用。

## 12. 技术基线

- Go 1.26，使用单一 `go.mod`。
- 标准库 `net/http` 提供配置发现和服务生命周期。
- `github.com/coder/websocket` 提供设备 WebSocket 和百炼 ASR/TTS WebSocket 能力。
- 原生 `libopus` 与 `github.com/hraban/opus` 处理 Opus 编解码，因此本地运行需要 CGO 和可用的 `libopus`。
- OpenAI 官方 Go SDK `github.com/openai/openai-go/v3` 调用百炼 LLM 兼容接口。
- 标准库 `encoding/json` 处理协议 JSON，标准库 `log/slog` 输出结构化日志。

首期不引入 HTTP 路由框架、ORM、migration、OpenAPI 生成器、ONNX Runtime、Prometheus、依赖注入框架或供应商插件框架。所有依赖必须在 `go.mod` 固定版本并经过许可证和漏洞检查。

## 13. 安全与日志边界

- 本阶段的共享 Token 只提供最低限度接入保护，不构成设备身份认证。
- `Device-Id`、`Client-Id` 和 `Serial-Number` 都是客户端声明，不得在日志或响应中描述为可信身份。
- 公网传输必须由受控反向代理提供 HTTPS/WSS；本服务首期不自行终止 TLS。
- 配置发现、WebSocket 头、JSON、Opus 包、队列和收音时长必须有上限。
- Token 比较使用常量时间比较（`crypto/subtle.ConstantTimeCompare`），日志严禁记录 `Authorization`、API Key、共享 Token 或完整外部响应。
- 严禁记录原始 PCM、Opus、完整系统提示词或完整对话正文。
- 日志字段截断规则：客户端声明字段（如 `device_id`、`client_id`、`serial_number`）、未识别扩展消息 payload 及错误摘要等，在日志输出时按最大 64 字符（常量 `log_truncate_limit`）进行截断并追加 `...`。
- 诊断日志限频规则：针对未识别扩展消息或高频异常，采用会话级令牌桶/滑动窗口限频器，每秒最多输出 1 条日志（常量 `diag_rate_limit = 1 msg/s`，突发容量 3 条），超限部分静默丢弃或聚合计数。
- 日志只保留会话 ID、截断后的设备声明、状态转换、取消原因以及 ASR/LLM/TTS 耗时和错误对象。
- 同一个错误只在能够决定处理方式的边界记录一次。

## 14. 测试策略

### 14.1 单元测试

- 配置严格解析：未知字段报错、缺失 `max_concurrent_sessions` 报错、数值超出合法范围报错、`llm_overall_timeout <= llm_first_token_timeout` 矛盾报错。
- 环境变量注入：缺失 `DASHSCOPE_API_KEY` 或 `DATABASE_DSN` 报错，空字符串报错。
- HTTP 发现与边界：超大请求体（>64 KiB）返回 413、超长请求头（>1024 字符）返回 400/431、合法请求返回正确 200 JSON。
- WebSocket 协议与消息边界：超大文本消息（>32 KiB）关闭连接（1009）、超大 Opus 包（>1024 字节）或空包关闭连接（1003/1008）、未知扩展消息限频与忽略。
- 编解码与音频边界：16 kHz Opus 解码为 1920 字节 PCM 帧、24 kHz PCM 组 1440 采样点编码为 60 ms Opus 帧。
- 队列与背压测试：ASR PCM 队列满 100 帧背压丢弃与超时断开、TTS PCM 队列满 100 块暂停读取与超时断开、下行 Opus 队列满 100 包主动关闭连接。
- 状态机与代次：合法/非法状态转换、回答代次递增、旧代次迟到结果丢弃。
- LLM 分句与上下文淘汰：标点分句、最长字符切分、内存历史超过 6 轮按 FIFO 淘汰最旧完整轮次。
- 超时与清理：Hello 10s 超时断开、收音 30s 超时断开、LLM 首 token 15s 超时取消、TTS 首音频 10s 超时取消、优雅关闭 10s 超时强退、`abort` 资源清理。
- 日志截断与限频：日志辅助函数对 >64 字符字段截断、限频器丢弃 >1 msg/s 诊断日志。

### 14.2 集成测试

使用本地模拟服务，不调用真实收费接口，覆盖：

- 配置发现返回固定 WebSocket v1 配置。
- Token 校验、协议版本 1 校验、hello 字段协商和 10 秒超时。
- 达到 `max_concurrent_sessions` 上限时握手直接返回 503 拒绝。
- 模拟百炼 ASR 的部分结果、VAD 最终结果、错误和取消。
- 模拟 OpenAI 兼容 LLM 的流式文本、流中错误和取消。
- 模拟百炼 TTS 的 PCM 分块、结束、错误和取消。
- 一轮完整语音、连续多轮、`manual` 停止、`abort` 和断线重连。
- 队列满载背压与串行下发保护。

实现完成后至少运行：

```sh
gofmt -w <changed-go-files>
go test ./...
go vet ./...
go test -race ./...
```

### 14.3 真实设备验收

真实验收使用当前未修改的 `xiaozhi-esp32` 固件和新生成的百炼 API Key：

1. 只修改设备 `ota_url`，配置发现成功返回 WebSocket v1 配置。
2. 设备完成 WebSocket 和 hello，默认 `auto` 模式开始收音。
3. 百炼服务端 VAD 正确结束一段语音，设备显示最终 STT 文本。
4. `qwen3.7-flash` 流式回答按句进入 TTS，设备连续播放完整语音。
5. 同一连接继续完成至少一轮依赖前文的对话。
6. 播放中触发显式 `abort` 后，旧文本和旧音频不再下发。
7. 主动断开连接后设备能够回到 idle 并重新连接完成新一轮对话。
8. 服务端没有把原始音频、完整设备声明或对话正文写入业务存储；必要诊断日志符合第 13 节的脱敏边界。

真实百炼调用只用于受控验收，默认自动化测试不得消费真实额度。

## 15. 完成定义

首期只有同时满足以下条件才算完成：

- 第 5.1 节中的能力全部存在稳定生产入口，没有未调用的占位实现。
- 当前固件不改源码和协议即可完成第 14.3 节真实语音验收。
- ASR、LLM、TTS 通过三个独立项目接口接入，供应商 SDK 类型没有泄漏到会话编排。
- 首期只有百炼实现，没有动态供应商框架或第二套无生产调用的适配器。
- `auto`、`manual`、按句 TTS、有限多轮、显式中断和断线清理行为符合本文档。
- 运行时所有连接、队列、音频、上下文和外部请求均有界且可取消。
- `go test ./...`、`go vet ./...` 和 `go test -race ./...` 通过。
- 源码、配置、测试、日志和文档中不存在真实 API Key、共享 Token 或原始音频。
- 人类概览、启动说明和实际实现保持一致，明确标注 MVP 的安全限制和未实现范围。

成功编译、只通过模拟服务或只完成文本调用都不能替代真实设备语音验收。

## 16. 外部协议依据

百炼实现应以以下官方资料为准，并在实际编码前再次核对模型可用性和事件字段：

- [实时语音识别](https://help.aliyun.com/zh/model-studio/real-time-speech-recognition-user-guide)
- [实时语音识别 WebSocket API](https://help.aliyun.com/zh/model-studio/fun-asr-realtime-websocket-api)
- [文本生成模型](https://help.aliyun.com/zh/model-studio/text-generation-model/)
- [实时语音合成](https://help.aliyun.com/zh/model-studio/realtime-tts-user-guide)
- [Qwen-Audio-TTS 音色列表](https://help.aliyun.com/zh/model-studio/qwen-audio-tts-voice-list)

阿里云当前没有适用于本项目 ASR/TTS 的官方 Go DashScope SDK。首期 LLM 使用 OpenAI 官方 Go SDK；ASR 和 TTS 使用 Go WebSocket 库按百炼官方协议直连，不引入 Java/Python 边车或非官方 SDK。
