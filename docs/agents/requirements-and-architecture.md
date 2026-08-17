# xiaozhi-esp32-golang-server 项目需求与架构约束

> 状态：已确认  
> 确认日期：2026-08-17  
> 适用项目：`xiaozhi-esp32-golang-server`  
> 目标读者：负责实现、审查和验证本项目的 Agents  
> 文档角色：项目需求与架构的详细事实基线  
> Agent 规范：[项目 AGENTS.md](../../AGENTS.md)  
> 人类摘要：[项目概览](../humans/project-overview.md)

## 1. 文档目的

本文档是项目范围、关键技术决策和验收标准的基线。后续设计和实现必须以本文档为准。

项目的首要约束是：在不牺牲正确性、安全性和可读性的前提下，使用能够完整解决当前问题的最简方案。不得为了假设中的规模、供应商或未来功能预先引入复杂架构。

## 2. 项目目标

实现一个供当前 `xiaozhi-esp32` 固件使用的服务端，满足以下目标：

- 基于 Go 1.26 构建单进程、单 Go module 的模块化单体。
- 当前 v1 `xiaozhi-esp32` 固件无需修改即可接入；v2 按当前固件协议完成 Mock 兼容。
- 首期完整打通设备激活、配置发现、WebSocket 双向语音和 ASR -> LLM -> TTS 对话链路。
- 首期同时兼容 `Activation-Version: 1` 激活码流程和 `Activation-Version: 2` challenge/HMAC 流程。
- 首期支持最多 10 路同时活跃的语音会话。
- 以单租户私有部署为目标，不实现多租户 SaaS 能力。
- 为后续 MCP、MQTT + UDP、固件升级和正式量产密钥管理保留清晰边界，但不提前实现这些能力。

## 3. 设计原则

### 3.1 最小正确实现

- 采用单体、单 Go module、单可执行程序，不拆分微服务或独立网关进程。
- 标准库能够清晰解决的问题直接使用标准库；其余能力优先采用成熟且持续维护的库，不重复实现 WebSocket、Opus、VAD、JSON、数据库迁移或 OpenAI 协议基础设施。
- 只有外部系统边界或确实存在多个实现的能力才定义接口。
- 依赖通过显式构造函数组装，不引入依赖注入框架。
- 不为尚未进入范围的供应商、数据库或传输协议实现插件框架。
- 不引入 Redis、消息队列、服务注册中心或分布式锁。
- 不保留没有明确生产调用方的兼容代码。

### 3.2 可读性

- 按业务能力组织 package，不堆叠通用 `util`、`manager`、`handler` 层。
- 一个用例应能沿着少量 package 和类型完成阅读，避免只有转发作用的层级。
- 请求、响应和领域状态使用明确的 Go struct 与自定义类型，避免无约束的 `map[string]any` 和字符串状态散落。
- 错误保留业务语义并使用 `%w` 包装原因；只在能够处理或统一记录的位置处理错误。
- 注释解释协议约束和不明显的并发原因，不复述代码。
- 配置映射到明确 struct，启动时严格解析并校验必填项，未知字段直接导致启动失败。

### 3.3 工程实践

- 除第 7.4 节明确限定的 v2 临时 Mock 外，密钥不得进入源码、数据库迁移、日志或示例配置。
- 所有外部输入必须做大小、格式、状态和时序校验。
- WebSocket 发送必须串行化；所有 channel、队列和音频缓存必须有界。
- 数据库结构由版本化 SQL migration 管理，禁止依赖 ORM 自动修改表结构。
- 外部 AI 调用必须有连接超时、响应头超时、整体超时、取消和错误映射。
- 结构化数据使用 JSON 编解码和明确校验，不使用字符串拼接生成协议消息。
- 依赖、模型和原生动态库必须固定版本；构建产物必须可复现并可审计。

## 4. 已确认的产品边界

| 决策项 | 已确认方案 |
| --- | --- |
| 部署模型 | 单租户私有部署 |
| 应用形态 | Go 模块化单体、单 module、单可执行程序 |
| Go 基线 | Go 1.26；构建固定 Go 1.26.6 |
| 部署目标 | Linux 容器；`linux/amd64` 和 `linux/arm64` |
| 首期传输 | 仅 WebSocket |
| 固件兼容 | v1 当前固件无需修改；v2 按当前固件协议完成 Mock 兼容 |
| 激活流程 | v1 激活码 + v2 单组硬编码凭证 Mock |
| AI 链路 | ASR + LLM + TTS |
| AI 接口 | OpenAI 兼容接口，地址和模型可配置 |
| TTS 返回格式 | 固定 24000 Hz、16-bit、有符号小端、单声道 PCM |
| VAD | 本地 Silero VAD / ONNX Runtime CPU |
| Opus | 原生 `libopus` + `hraban/opus` |
| WebSocket 二进制协议 | v1、v2、v3 |
| 服务端 AEC | 首期不实现 |
| 设备认证 | 每台设备独立的高熵 Bearer Token |
| 管理认证 | HTTP Basic + TLS |
| 管理入口 | 仅安全 REST API 和 OpenAPI 文档，不开发 UI |
| 助手配置 | 所有设备共用一套全局配置 |
| 数据库 | MySQL 8 + GORM + goose；PostgreSQL 仅保留扩展边界 |
| 对话留存 | 仅持久化文本和必要指标，不持久化原始音频 |
| 单实例并发 | 最多 10 路同时活跃语音会话 |
| 可观测性 | `log/slog` JSON 日志 + Prometheus 指标 |
| TLS | 由反向代理终止 |
| 验收 | 自动化测试 + v1 真实 ESP32 端到端验收 |

## 5. 交付范围

### 5.1 第一阶段：核心可用链路

第一阶段必须交付以下能力：

- OTA/配置发现接口。
- 基于激活码和管理员确认的 v1 设备绑定流程。
- 基于 challenge/HMAC 的 v2 协议流程及单组硬编码凭证 Mock。
- 受 HTTP Basic 认证中间件保护的管理员 REST API。
- 以 OpenAPI 为唯一 REST 契约并生成 Go 类型和 `chi` 服务端接口。
- 每设备独立的 WebSocket Bearer Token 签发、校验、撤销和轮换。
- WebSocket 握手、客户端 hello 校验和服务端 hello 响应。
- WebSocket 二进制协议 v1、v2、v3 的严格解析与编码。
- Opus 上行音频接收、16 kHz PCM 解码和有界缓存。
- Silero VAD 语音起止检测，支持 `auto`、`manual` 和可用设备端 AEC 的 `realtime` 交互。
- OpenAI 兼容 ASR、流式 Chat Completions 和 TTS 调用。
- STT、TTS 状态和文本消息下发。
- 24 kHz PCM 编码为设备可播放的 60 ms Opus 帧并按实时节奏下发。
- 对话打断、取消、断开清理、超时和重连。
- 文本对话历史持久化及有限轮次上下文。
- 健康检查、Prometheus 指标和安全日志。
- 自动化协议模拟测试及 v1 真实设备验收。

### 5.2 第一阶段明确不包含

- MQTT + UDP 传输。
- MQTT Broker、UDP 网关和 AES-CTR 音频通道。
- 服务端 AEC；二进制协议 v2 的时间戳首期仅解析和透传为协议元数据。
- MCP 工具发现和调用。
- 服务端主动向设备下发 MCP 指令。
- 固件文件上传、版本管理和 OTA 二进制下载。
- 摄像头图像分析接口。
- Glyph Push 和 Custom 扩展消息。
- 管理后台 Web UI。
- 多租户、用户注册、角色权限系统和第三方 OIDC。
- 多实例部署、会话迁移和共享会话状态。
- RAG、长期记忆、声纹识别、视觉模型和多智能体。
- 原始音频存储、回放和对象存储。
- 多 AI 供应商动态切换。
- MP3、AAC、FLAC 等 TTS 压缩音频解码、FFmpeg 和独立重采样链路。
- CUDA、TensorRT 或其他 VAD 硬件加速后端。
- PostgreSQL 驱动、migration 和兼容性测试。
- v2 量产密钥导入、轮换、吊销、多设备密钥存储和生产身份保证。

### 5.3 后续阶段

后续能力按真实需求独立评审，建议顺序如下：

1. 删除 v2 硬编码 Mock，接入可审计的量产密钥配置、存储和轮换机制。
2. MCP 设备工具发现和调用。
3. 固件版本管理与 OTA 文件服务。
4. 摄像头、Glyph Push 和 Custom 扩展。
5. MQTT + UDP 传输。
6. 服务端 AEC。

任何后续功能不得破坏第一阶段协议、数据和安全契约。替换 v2 Mock 时必须彻底删除硬编码值、Mock 分支及只为其存在的测试数据，不保留兼容入口。

## 6. 建议架构

### 6.1 应用形态

应用采用模块化单体。模块是 Go package 和明确的依赖方向，不是多个 module 或独立进程。

建议目录与业务边界：

- `cmd/server`：进程入口、配置加载和显式依赖组装，不承载业务逻辑。
- `internal/device`：设备身份、v1/v2 激活、状态和凭证。
- `internal/ota`：固件启动时的配置发现响应和公开激活入口。
- `internal/security`：管理员认证、设备 Token 认证和限流。
- `internal/dialogue`：WebSocket 生命周期、协议状态机和会话编排。
- `internal/audio`：二进制帧、Opus、PCM、VAD 和有界缓冲。
- `internal/ai`：ASR、LLM、TTS 三个外部能力边界及唯一的 OpenAI 兼容实现。
- `internal/conversation`：文本消息、上下文和持久化。
- `internal/admin`：管理 REST API 用例和传输适配。
- `internal/storage/mysql`：GORM model、仓储实现和事务边界。
- `api`：OpenAPI 契约及生成配置。
- `migrations`：版本化 MySQL SQL migration。

依赖方向必须保持为协议入口调用应用用例，应用用例调用领域能力和外部端口。GORM model、生成的 HTTP 类型、WebSocket 库类型、OpenAI SDK 类型和 ONNX 类型不得扩散到无关业务 package。

### 6.2 技术基线

- Go 1.26，`go.mod` 声明 `go 1.26`，CI 和容器构建固定 Go 1.26.6。
- Go Modules 管理依赖，提交 `go.mod` 和 `go.sum`。
- `net/http` + `github.com/go-chi/chi/v5` 提供 HTTP 服务与路由。
- `github.com/coder/websocket` 提供 WebSocket 能力。
- OpenAPI 契约优先，使用 `github.com/oapi-codegen/oapi-codegen/v2` 生成 Go 类型和 `chi` 服务端接口。
- GORM + MySQL 驱动实现仓储，GORM 仅存在于 `internal/storage/mysql`。
- `github.com/pressly/goose/v3` 管理嵌入式 SQL migration。
- 标准库 `encoding/json` 处理 JSON。
- 原生 `libopus` + `github.com/hraban/opus` 处理 Opus 编解码；使用 `nolibopusfile` build tag 禁用未使用的 Ogg/Opus 文件流能力。
- `github.com/yalue/onnxruntime_go` + 官方 ONNX Runtime CPU 动态库运行 Silero VAD。
- OpenAI 官方 Go SDK `github.com/openai/openai-go/v3` 调用 OpenAI 兼容接口。
- 标准库 `log/slog` 输出 JSON 结构化日志。
- `github.com/prometheus/client_golang` 提供指标。
- `github.com/testcontainers/testcontainers-go` 启动 MySQL 8 集成测试环境。

不得仅为 10 路并发引入事件循环网络框架、全量异步框架或依赖注入框架。需要流式读取 LLM 响应时，只在 AI 适配器边界使用可取消的 SDK 流。

### 6.3 构建与部署

- 官方交付物是 `linux/amd64` 和 `linux/arm64` 容器镜像；不承诺 Windows 或 macOS 原生发布。
- macOS 仅作为本地开发环境。
- CGO 必须开启；不同架构分别构建和测试，不假设原生依赖可直接交叉编译。
- `libopus`、ONNX Runtime 和 Silero 模型在镜像构建阶段以固定版本获取并校验 SHA-256 与许可证。
- 运行时不联网下载模型或动态库，不允许通过请求动态替换模型。
- 容器内使用非 root 用户，根文件系统除明确的数据目录外保持只读。

## 7. 设备配置与激活

### 7.1 身份口径

- `Device-Id` 是固件与服务端纯后端交互的唯一定位口径，数据库必须设置唯一约束。
- `Client-Id` 是辅助校验字段，不参与组成设备唯一身份。
- v1 设备没有 `Serial-Number`，不得伪造或用其他字段填充序列号。
- v2 设备的 `Serial-Number` 是设备业务身份，必须设置唯一约束；`Device-Id` 仍用于当前固件请求定位和辅助校验。
- 数据库使用应用生成的内部稳定主键关联数据，不把任何请求头直接作为数据库主键。
- Token 绑定内部设备记录；WebSocket 握手按固件实际行为校验 `Device-Id` 和 `Client-Id`。

### 7.2 配置发现请求

接口必须兼容固件 `Ota::CheckVersion()` 的现有行为：

- 支持 POST；当设备没有系统信息正文时兼容 GET。
- 读取 `Activation-Version`、`Device-Id`、`Client-Id`、`Serial-Number`、`User-Agent` 和 `Accept-Language` 请求头。
- `Activation-Version: 1` 不要求 `Serial-Number`；`Activation-Version: 2` 必须携带合法 `Serial-Number`。
- `Device-Id`、`Client-Id` 和 `Serial-Number` 在签发凭证前都只是设备声明，不能单独视为认证信息。
- 请求正文按 JSON 解析并设置大小上限；固件新增的未知字段应安全忽略。
- 不信任客户端 `Host` 或任意转发头生成服务地址。

### 7.3 v1 激活码流程

- 为未绑定的 `Device-Id` 生成短期、一次性激活码，并保存本次声明的 `Client-Id`。
- 返回固件能够展示的 `activation.message` 和 `activation.code`。
- 激活完成前不得返回可用的 WebSocket Token。
- 激活码必须有过期时间、尝试次数限制并在成功后立即失效。
- 同一 `Device-Id` 和 `Client-Id` 重复轮询时应复用仍有效的激活申请；`Client-Id` 中途变化时拒绝复用，避免身份声明被静默替换。
- 管理员通过受保护的 REST API 提交激活码并确认设备。
- 激活操作必须幂等；重复确认返回当前结果，不创建重复设备。

### 7.4 v2 challenge/HMAC Mock 流程

首期完整实现固件 v2 消息时序，但只支持一组直接硬编码在源码中的 Mock `Serial-Number` 和 HMAC Key。

配置发现阶段：

- 只接受与硬编码 Mock 序列号完全一致的 `Serial-Number`。
- 为未激活设备生成高熵、短期、一次性 challenge，并绑定当前 `Device-Id`、`Client-Id` 和 `Serial-Number`。
- 返回 `activation.challenge` 和 `activation.timeout_ms`，不返回 v1 激活码。
- 同一设备重复轮询时复用仍有效的 challenge；过期后生成新的 challenge。

设备激活阶段：

- 提供与 OTA URL 同级的 `POST /activate`，接收固件发送的 `algorithm`、`serial_number`、`challenge` 和 `hmac`。
- 要求 `algorithm` 等于 `hmac-sha256`，正文序列号与请求头及硬编码序列号一致。
- 校验 challenge 存在、未过期、未消费，并与当前 `Device-Id`、`Client-Id` 和 `Serial-Number` 完全一致。
- 使用硬编码 HMAC Key 计算摘要并进行常量时间比较。
- 成功后原子地绑定三项设备声明、激活设备并消费 challenge，返回 HTTP 200。
- 同一已激活设备对同一成功请求的网络重试返回 HTTP 200，但不得重复创建记录、签发凭证或产生其他副作用。
- 已消费的 challenge 不得用于绑定其他设备声明；已激活的 Mock 序列号也不得静默改绑新的 `Device-Id`。
- 校验失败增加有限尝试计数；过期、跨身份重放、字段不一致和 HMAC 错误均不得激活设备。
- 激活成功后由设备下一次配置发现领取 WebSocket Token。

v2 Mock 的安全边界：

- 硬编码值只能对应专用测试数据，不得使用量产密钥。
- 拿到源码的人能够伪造该 Mock 设备，因此该流程不提供生产身份安全保证。
- Mock Key 不写入日志、指标、数据库、错误响应或生成的 OpenAPI 示例。
- v2 首期只要求自动化协议验收，不要求真实 ESP32 验收。
- 接入正式密钥管理后必须完整删除硬编码值和 Mock 分支。

### 7.5 管理员操作

- 管理员可查看待激活设备、已绑定设备、激活版本、最后在线时间和禁用状态。
- 管理员确认仅用于 v1 激活码流程；v2 在 HMAC 校验成功后自动激活。
- 管理员可禁用或重新启用设备、撤销凭证和请求凭证轮换。
- 管理 API 的设备定位遵守第 7.1 节身份口径，不使用 `(Device-Id, Client-Id)` 联合身份。

### 7.6 已激活设备响应

配置响应至少包含：

```json
{
  "server_time": {
    "timestamp": 0,
    "timezone_offset": 0
  },
  "websocket": {
    "url": "wss://example.com/xiaozhi/v1/",
    "token": "device-secret",
    "version": 1
  }
}
```

约束如下：

- `timestamp` 使用 Unix 毫秒。
- `url` 必须根据可信的外部基础 URL 配置生成，不信任任意客户端 `Host` 或转发头。
- `version` 可配置为 1、2 或 3，默认使用 1。
- Token 只在首次签发或管理员触发轮换后的领取窗口返回。
- Token 明文不得持久化；服务端只保存不可逆摘要。
- 固件升级不在首期范围；没有升级时省略 `firmware`，不得伪造新版本。

## 8. WebSocket 协议

### 8.1 握手

- WebSocket 路径使用稳定的版本化地址，例如 `/xiaozhi/v1/`。
- 校验 `Authorization: Bearer <token>`。
- 校验 `Protocol-Version` 为 1、2 或 3。
- 依据 `Device-Id` 定位设备，并校验 `Client-Id` 与 Token 绑定设备一致。
- 被禁用、未激活、凭证已撤销或头部不一致时拒绝升级。
- 记录认证失败原因，但日志不得包含 Token。
- 禁用 WebSocket 压缩；Opus 数据不得重复压缩。
- 对握手头部总大小和单字段长度设置硬上限。

### 8.2 hello 协商

连接升级后，设备必须在限定时间内发送文本 hello。服务端校验：

- `type` 等于 `hello`。
- `transport` 等于 `websocket`。
- 正文 `version` 与 `Protocol-Version` 请求头一致。
- `audio_params.format` 等于 `opus`。
- 上行参数是 16000 Hz、单声道和受支持的帧时长。

成功后，服务端返回：

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

固件等待 hello 的超时时间为 10 秒，因此服务端不得在 hello 前执行模型加载或外部 AI 请求。Silero 模型和 ONNX 推理能力必须在应用启动阶段完成加载、自检和健康校验。

### 8.3 二进制协议

服务端必须依据已协商版本解析和发送音频：

- v1：WebSocket binary payload 直接是单个 Opus 包。
- v2：解析 16 字节网络字节序头，验证版本、类型、时间戳和 32 位 payload 长度。
- v3：解析 4 字节头，验证类型、保留字段和 16 位 payload 长度。

通用要求：

- 收到的数据长度必须与头部声明完全一致。
- 拒绝超出配置上限、截断、空载荷或类型不支持的消息。
- 解析层不得把超出消息生命周期的切片传给异步任务；需要保留的数据必须复制到有界缓冲。
- 下行音频使用连接协商的同一协议版本。
- 首期解析 v2 时间戳但不实现服务端 AEC。

### 8.4 文本消息

第一阶段处理以下设备到服务端消息：

- `listen.start`，模式为 `auto`、`manual` 或 `realtime`。
- `listen.stop`。
- `listen.detect` 及唤醒词文本。
- `abort`，包括 `wake_word_detected` 原因。
- `hello`。

第一阶段发送以下服务端到设备消息：

- `hello`。
- `stt`。
- `tts.start`。
- `tts.sentence_start`。
- `tts.stop`。

首期不主动发送 MCP、System、Custom 或 Glyph Push 消息。收到暂未支持但格式正确的扩展消息时，应记录限频诊断并安全忽略；不得导致连接崩溃。

### 8.5 会话状态机

每个 WebSocket 连接必须维护明确状态，至少覆盖：

`CONNECTED -> READY -> LISTENING -> PROCESSING -> SPEAKING -> LISTENING/READY -> CLOSED`

约束如下：

- 单个会话监督 goroutine 独占状态机和当前回答代次，其他 goroutine 不直接写状态。
- 非法状态下的消息不得隐式改变状态。
- 同一连接只允许一个当前回答任务。
- `abort` 或新的唤醒词必须取消旧的 ASR、LLM、TTS 任务，清空尚未发送的旧音频。
- 取消同时使用 `context` 和会话代次；迟到的异步结果不得进入新一轮对话。
- WebSocket 关闭后必须释放 VAD 状态、Opus 编解码器、音频缓冲、发送队列和外部请求。
- 同一设备首期保留最新连接并关闭旧连接。

## 9. 音频与 AI 链路

### 9.1 上行处理

1. 严格解析当前二进制协议帧。
2. 使用 `libopus` 直接解码为 16000 Hz、16-bit、单声道 PCM，不经过独立重采样。
3. 将 PCM 输入每会话独立的 Silero VAD 隐状态。
4. 使用有界预滚动缓冲保留语音起点前的少量音频。
5. 检测到语音结束或收到 `listen.stop` 后，在内存中封装标准 16 kHz、16-bit、单声道 WAV。
6. 对最大语音时长、最大静音时长、WAV 大小和最大内存占用实施硬限制。
7. 通过 `openai-go` 调用 OpenAI 兼容 transcription 接口。

`manual` 模式以 `listen.stop` 作为主要结束信号；`auto` 和 `realtime` 模式由服务端 VAD 完成端点检测。上行采样率不符时拒绝 hello，不做静默转换。

### 9.2 文本与 LLM

- ASR 成功后立即发送 `stt` 文本并持久化用户消息。
- LLM 使用流式 Chat Completions 接口。
- 上下文仅包含全局系统提示词和该设备最近的有限轮文本。
- 上下文轮数或 Token 预算必须有明确上限。
- 不实现供应商动态路由、自动降级或重试风暴。
- 禁用 SDK 隐式重试；只在请求尚未开始返回时执行有限的显式幂等重试。
- 流式输出开始后失败不得重新生成重复回答。

### 9.3 TTS 与下行

- 将 LLM 增量文本按自然句边界切分。
- 达到句末或安全长度上限后立即调用 OpenAI 兼容 speech 接口。
- 请求并且只接受 24000 Hz、16-bit、有符号小端、单声道原始 PCM。
- 返回格式、字节对齐或配置不符合约定时终止本轮 TTS，不尝试 MP3 解码或重采样。
- 第一段音频前发送 `tts.start`。
- 每句下发前发送带文本的 `tts.sentence_start`。
- 将 PCM 累积为每帧 1440 个采样点，编码为设备支持的 60 ms Opus 包；末尾不足一帧时最多补一帧静音。
- 使用连接协商的 v1、v2 或 v3 格式发送。
- 按音频时长节奏下发，防止瞬时写满设备和网络缓冲。
- 全部句子发送完成后发送 `tts.stop`。
- 被打断后禁止发送旧回答剩余文本、状态或音频。

### 9.4 本地音频与模型边界

Silero VAD 是首期唯一的本地推理模型，只负责语音端点检测。ASR、LLM、TTS 均使用外部 API。

本地依赖必须满足：

- 固定 `libopus`、ONNX Runtime 和 Silero 模型版本，校验来源、SHA-256 和许可证。
- 在镜像构建流程中以可重复方式获取，运行时不下载。
- 应用启动时初始化 ONNX Runtime、加载模型并执行一次已知输入自检。
- 只使用 ONNX Runtime CPU 执行后端，不携带 GPU 运行库。
- 为每个语音会话维护独立的 VAD 隐状态，不得跨设备复用状态。
- 每个会话独立持有 Opus 编码器和解码器，不跨 goroutine 并发调用同一实例。
- 模型、动态库或自检失败时应用保持未就绪，不接受设备会话。

## 10. 配置

所有设备首期共用一套全局配置：

- 服务监听地址、可信外部基础 URL 和关闭宽限期。
- MySQL 非敏感连接参数、连接池和 migration 设置；包含账号或密码的 DSN 不得写入 YAML。
- ASR base URL、API key、model、language 和超时。
- LLM base URL、API key、model、system prompt、temperature、上下文上限和超时。
- TTS base URL、API key、model、voice 和超时；输出格式固定为第 9.3 节 PCM，不提供格式配置。
- WebSocket 协议版本、消息上限、队列容量、音频时长和并发限制。
- VAD 参数、模型路径和 ONNX Runtime 动态库路径；路径由镜像构建结果提供，不接受客户端输入。

配置规则：

- 非敏感配置使用单一 YAML 文件；启动参数只负责指定配置文件位置。
- YAML 严格映射到配置 struct，未知字段、缺失必填项、非法枚举和越界数值均导致启动失败。
- MySQL DSN 或数据库凭证、AI API key、管理员密码哈希等敏感值只允许通过环境变量或容器 Secret 注入。
- 示例配置只包含非敏感默认值和敏感项名称，不包含可用凭证。
- 不支持配置热更新；配置变更通过受控重启生效。
- 启动状态区分进程存活、依赖就绪和 AI 配置完整。
- 外部响应不得原样写入错误消息或日志，以免泄露供应商数据。
- v2 硬编码 Mock 是唯一临时例外，并受第 7.4 节限制。

## 11. 安全要求

### 11.1 管理接口

- 使用无状态 HTTP Basic 认证中间件，不使用浏览器会话或表单登录。
- 仅允许通过 HTTPS 访问；TLS 由反向代理终止。
- 管理密码使用自适应哈希，以哈希形式注入，不保存明文默认密码。
- 默认关闭跨域访问；确有调用方时只允许显式来源。
- OpenAPI 文档、Prometheus 指标和详细健康信息必须受管理认证保护。
- 公开存活和就绪探针只返回最小状态，不返回配置、依赖地址、模型或设备信息。
- 对认证失败限流；日志不得记录 `Authorization` 内容。

### 11.2 设备 Token

- 每台设备独立生成至少 256 bit 随机 Token。
- Token 通过 `Authorization: Bearer` 使用。
- 数据库只保存 Token 的 SHA-256 摘要。
- 握手时按 `Device-Id` 读取绑定设备，并使用常量时间方式比较摘要。
- Token 不设置依赖设备主动刷新的短过期时间；通过禁用、撤销和轮换控制生命周期。
- Token 明文不得出现在日志、指标、异常、数据库或管理查询结果中。

### 11.3 激活身份保证

- 配置发现端点和 v2 `/activate` 端点必须公开给设备访问，但必须限流并限制正文和头部大小。
- v1 的 `Device-Id` 与 `Client-Id` 都是可伪造声明，只有管理员确认激活码后才允许签发 Token。
- v2 的硬编码 Mock Key 已公开在源码中，只验证协议实现，不提供真实设备身份保证。
- 未激活设备不得获得 WebSocket Token。
- Token 只在激活后的首次领取或明确轮换窗口下发。
- 公网部署必须使用 HTTPS/WSS；不得把 HTTP Basic 或设备 Token 暴露在明文链路。
- 不得宣称 v1 或 v2 Mock 具备硬件级不可伪造身份。

### 11.4 通用防护

- REST、WebSocket 文本帧、二进制帧、音频段和 channel 均设置上限。
- 对配置发现、激活、认证失败和握手设置按来源及设备标识的内存限流。
- 对 JSON 字段、枚举、UUID、MAC 地址和 URL 做严格校验。
- 不接受客户端提交的本地文件路径、任意回调地址或任意固件下载地址。
- 日志对设备标识做必要保留，但对凭证和 AI 密钥做完全脱敏。
- 依赖版本固定并进入常规漏洞扫描。
- 所有限流状态只在单实例内生效；不得虚假声明为分布式防护。

## 12. 数据持久化

### 12.1 技术要求

- 开发、测试和生产统一使用 MySQL 8。
- GORM 只实现仓储映射和事务，不允许在应用启动或生产代码中调用自动建表能力。
- `github.com/pressly/goose/v3` 管理普通 SQL migration；禁止存储过程和任何过程调用语句。
- migration 使用 Go `embed` 打入二进制，并在 HTTP 端口开始监听前自动执行。
- migration 失败时终止启动，不允许在未知表结构上继续运行。
- 使用应用生成的稳定 UUID 主键，不依赖数据库自增语义。
- 时间统一使用 UTC `time.Time`，写入和读取时不得依赖服务器本地时区。
- 激活、凭证签发、撤销和轮换必须使用明确事务与唯一约束保证一致性。
- GORM 只存在于 MySQL 仓储实现，不向应用用例暴露 `*gorm.DB` 或 GORM model。

### 12.2 最小数据模型

首期只保留以下概念：

- Device：内部主键、唯一 `Device-Id`、辅助 `Client-Id`、可空且唯一的 `Serial-Number`、激活版本、状态、凭证摘要、创建时间和最后在线时间。
- Activation：激活版本、设备声明、激活码摘要或 challenge 摘要、过期时间、状态和尝试次数。
- Conversation：设备、会话标识、开始/结束时间和终止原因。
- Message：会话、角色、文本、顺序、创建时间和必要耗时指标。

不得存储：

- 原始上行或下行音频。
- AI API key。
- 设备 Token 明文。
- v2 Mock HMAC Key 或设备提交的完整 HMAC。
- 没有明确用途的完整第三方 AI 原始响应。

### 12.3 PostgreSQL 扩展边界

- 首期只交付并测试 MySQL 8，不声明 PostgreSQL 可运行。
- 数据访问必须通过仓储边界；业务 package 不依赖 MySQL 驱动、GORM model 或方言表达式。
- 主键、时间和领域字段避免无必要的 MySQL 专有语义。
- 首期不引入 PostgreSQL 驱动、配置、双份 migration 或测试容器。
- 后续支持 PostgreSQL 时必须新增独立 migration，并在 PostgreSQL Testcontainers 环境执行 migration 和仓储集成测试。
- 不得把“GORM 支持 PostgreSQL”表述为本项目已经支持 PostgreSQL。

## 13. HTTP API

### 13.1 契约

- 所有 HTTP 接口只允许使用 GET 和 POST。
- API 使用稳定的版本化路径。
- OpenAPI YAML 是 REST 契约的唯一来源。
- 使用 `oapi-codegen` 生成请求/响应类型和 `chi` 服务端接口，生成文件不得手工修改。
- CI 必须重新生成并检查工作区无差异，防止契约与生成代码漂移。
- 请求进入用例前完成 OpenAPI schema、大小和业务字段校验。
- 错误统一使用 RFC 9457 Problem Details；公开设备协议端点同时遵守固件能处理的状态码和响应体。

WebSocket 文本和二进制协议不塞入 OpenAPI，由独立协议类型、文档和测试约束。

### 13.2 管理 API

首期至少覆盖：

- 查询待激活申请。
- 使用激活码确认 v1 设备。
- 查询已绑定设备及激活版本。
- 禁用或重新启用设备。
- 撤销并轮换设备凭证。
- 查询文本会话和消息。
- 查询应用详细健康状态和非敏感运行信息。

接口要求：

- 创建、确认、禁用和轮换操作必须定义幂等语义。
- 管理 API 不提供 v2 Mock Key 的读取或修改入口。
- 管理 API 不读写 AI API key。
- 对外响应不得包含 Token 摘要、密码哈希、数据库 DSN 或内部错误。

## 14. 并发与资源约束

- 单实例最多允许 10 路同时活跃语音会话，超过时明确拒绝新会话并记录指标。
- 每个 WebSocket 连接由一个会话监督 goroutine 独占状态和回答代次。
- 每个连接只有一个读取循环和一个发送循环；文本和音频共享有序的有界发送 channel。
- 音频接收、VAD 和 AI 工作通过有界 channel 与监督器交互，不阻塞 WebSocket 读取循环。
- 不为每个音频帧创建 goroutine。
- 每个会话的上行音频、待 TTS 文本、待发送音频和对话上下文均有独立上限。
- 慢设备不得无限积压下行音频；达到上限后终止当前回答或连接。
- AI 调用必须在连接关闭、`abort` 和服务停机时通过 `context` 取消。
- 同一设备建立新连接后关闭旧连接，清理旧连接的所有任务和队列。
- 服务关闭时停止接受新连接，取消进行中的回答，并在有限宽限期内完成关闭。
- 所有 goroutine 必须有明确所有者和退出路径；测试必须覆盖断开、取消和启动失败时的泄漏。

具体数值集中在严格配置 struct 中，并通过测试证明不会形成无界增长。

## 15. 错误处理与可观测性

### 15.1 错误处理

- 协议错误：拒绝当前消息；严重或重复错误关闭连接。
- 认证错误：握手阶段拒绝，不进入业务处理。
- ASR 失败：结束本轮处理，不调用 LLM/TTS，并向设备结束相应状态。
- LLM 失败：停止 TTS，清理本轮任务，允许下一轮对话。
- TTS 失败：发送 `tts.stop`，不得让设备永久停留在 speaking 状态。
- 数据库失败：激活和凭证事务回滚，不返回成功。
- migration、原生库、模型加载或启动自检失败：记录明确原因并终止启动。
- panic：只在进程和连接统一边界恢复，记录必要上下文、清理会话；不得用 panic 表达可预期业务错误。
- 错误只在责任边界记录一次，避免相同异常被逐层重复打印。

### 15.2 日志

使用 `log/slog` 输出 JSON 结构化日志。关键日志至少覆盖：

- 外部 AI 调用开始、结束、取消、有限重试和失败。
- v1/v2 激活结果、设备状态变更和凭证状态流转。
- 会话 ID、脱敏设备 ID、协议版本和 listening mode。
- 会话状态转换。
- ASR、LLM 首 Token、TTS 首音频耗时。
- 中断、超时、队列溢出和断开原因。

不得在音频帧循环或批量内层逐条打印 INFO 日志。不得记录原始音频、完整 Token、Token 摘要、API key、v2 Mock Key、设备 HMAC、完整外部响应或默认完整系统提示词。异常日志必须包含业务标识、关键入参摘要和错误对象，但不得包含敏感值。

### 15.3 指标

使用 `prometheus/client_golang`，至少提供：

- 当前 WebSocket 连接数和活跃语音会话数。
- 握手成功/失败计数。
- 按 v1/v2 区分的激活成功/失败计数。
- 上行和下行音频帧计数及丢弃计数。
- VAD 语音段数量和时长。
- ASR、LLM、TTS 请求次数、错误数和耗时。
- 中断、超时、队列溢出和协议错误计数。

指标标签不得使用 session ID、完整设备 ID 等高基数字段。外部供应商延迟不作为服务端自身的固定 SLA，但必须被独立测量。

### 15.4 健康端点

- 公开存活探针只表示进程事件循环仍可响应。
- 公开就绪探针只返回就绪或未就绪，不披露具体依赖信息。
- MySQL、migration、ONNX Runtime、Silero 自检和必要配置未就绪时不得接收设备流量。
- 详细健康信息和 `/metrics` 必须通过管理员 HTTP Basic 认证。

## 16. 测试策略

### 16.1 单元测试

- WebSocket 二进制协议 v1、v2、v3 帧编码和解码。
- 非法长度、字节序、类型和大小限制。
- 会话状态机合法和非法转换。
- VAD 分段边界、独立隐状态和最大时长。
- LLM 文本分句、回答代次和取消。
- v1 激活码过期、幂等和尝试限制。
- v2 challenge 绑定、过期、HMAC 成功/失败、重放和一次性消费。
- Token 摘要校验、撤销和设备绑定。
- YAML 未知字段、缺失敏感配置和越界资源配置。

### 16.2 集成测试

- 使用 `net/http/httptest` 验证管理接口 HTTP Basic 授权矩阵和 RFC 9457 错误。
- 验证 OpenAPI 请求校验、生成接口与实现绑定，并检查重新生成无差异。
- 验证 WebSocket 握手 Token、设备头、版本、压缩禁用和 hello 超时。
- 验证 OTA v1/v2 未激活、已激活和禁用设备响应。
- 使用 MySQL 8 Testcontainers 验证 goose 从空库迁移、GORM 仓储事务和唯一约束。
- 使用本地测试 HTTP 服务模拟 OpenAI 兼容接口的成功、流式、超时、取消和错误。
- 使用真实 `libopus`、ONNX Runtime CPU 和固定 Silero 模型执行音频与 VAD 集成测试。
- 测试启动 migration、动态库、模型或自检失败时不会进入就绪状态。

### 16.3 协议模拟测试

提供轻量 Go 测试客户端，覆盖：

- v1 激活码轮询、管理员确认和 Token 首次领取。
- v2 challenge/HMAC 成功、错误 Key、错误序列号、过期和重放。
- hello 成功和超时。
- WebSocket 二进制协议 v1、v2、v3 Opus 上行和下行。
- `auto`、`manual`、`realtime` 三种模式。
- 正常一轮和连续多轮对话。
- speaking 期间 abort 和新唤醒词打断。
- AI 响应中断线。
- 慢接收端、超大消息和格式错误。
- 10 路并发会话。

并发相关测试必须运行 Go race detector，并检查连接关闭后 goroutine、channel、Opus 和 VAD 资源能够回收。

### 16.4 容器测试

- `linux/amd64` 和 `linux/arm64` 镜像均必须成功构建。
- 两个架构的镜像都必须完成动态库加载、migration、Silero 推理自检和健康探针 smoke test。
- 镜像中模型、原生库版本、校验值和许可证必须与构建清单一致。
- 容器不得依赖运行时下载模型或系统级临时安装依赖。

### 16.5 真实设备验收

使用当前未修改、走 `Activation-Version: 1` 的 `xiaozhi-esp32` 固件验证：

1. 设备通过 OTA URL 请求配置并显示激活码。
2. 管理 API 确认激活后，设备获得 WebSocket 配置并连接。
3. hello 在固件 10 秒超时内完成。
4. 用户语音被正确识别并在设备显示 STT 文本。
5. LLM 回答按句合成，设备显示文本并连续播放 Opus 音频。
6. 用户可通过按键或唤醒词中断回答，旧音频不再播放。
7. WebSocket 断开后设备可恢复到 idle 并重新建立会话。
8. 服务端不落盘保存本次原始音频。

v2 Mock 不要求真实设备验收。只有烧录了对应专用 Mock HMAC Key 的测试设备可作为补充验证，不能替代自动化协议测试，也不构成生产安全证明。

成功构建和模拟器通过不能替代 v1 真机验收。

## 17. 完成定义

第一阶段只有同时满足以下条件才算完成：

- 第一阶段范围中的接口和状态流全部实现。
- 当前 v1 固件无需修改即可完成真实语音对话。
- v1 激活码和 v2 challenge/HMAC Mock 自动化测试通过。
- WebSocket 二进制协议 v1、v2、v3 自动化测试通过。
- 管理认证、v1 激活、凭证撤销和越权测试通过。
- v2 Mock 已明确隔离为非生产能力，未被纳入安全身份声明。
- MySQL 8 migration 和 GORM 仓储 Testcontainers 测试通过。
- `linux/amd64` 和 `linux/arm64` 镜像构建及启动 smoke test 通过。
- 10 路模拟并发下无数据竞争、goroutine 泄漏、无界内存增长、发送并发错误或会话串音。
- 日志和数据库中没有 Token、Token 摘要、API key、v2 Mock Key、设备 HMAC 或原始音频。
- OpenAPI 生成检查通过，README、部署配置、OpenAPI 和已知限制与实现一致。
- 依赖、模型和原生库版本固定并完成许可证与漏洞检查。
- 明确记录尚未进行的硬件、网络环境或供应商兼容验证。

## 18. 协议依据

实现时以当前工作区 `xiaozhi-esp32` 代码为最终事实来源，重点包括：

- `xiaozhi-esp32/main/ota.cc`
- `xiaozhi-esp32/main/protocols/protocol.cc`
- `xiaozhi-esp32/main/protocols/websocket_protocol.cc`
- `xiaozhi-esp32/docs/websocket_zh.md`
- `xiaozhi-esp32/docs/mcp-protocol_zh.md`

文档与代码不一致时，应先依据当前设备代码编写兼容测试，再更新本项目文档。不得仅凭第三方服务端行为推断协议。
