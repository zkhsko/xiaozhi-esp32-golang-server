# xiaozhi-esp32-golang-server 项目概览

## 项目目标

为 `xiaozhi-esp32` 固件提供高性能、轻量级的 Go 语言服务端。固件无需修改源码或私有协议，只需将 `ota_url` 指向本服务即可完成设备激活、绑定和实时双向语音交互。

```text
ESP32 (16kHz Opus) ──> 服务端解码 ──> 百炼 ASR (VAD) ──> 百炼 LLM (qwen3.7-flash)
                                                               │ (支持 MCP / 工具调用)
ESP32 播放 <── 编码 (24kHz Opus) <── 百炼 TTS (按句合成) <───────┘
```

## 核心能力

- **配置发现与设备激活**：支持标准 OTA 配置发现，支持 eFuse HMAC 硬件激活、6 位动态验证码与带 SN / 无 SN 用户设备绑定。
- **凭证与鉴权**：支持批量生成设备出厂凭据，基于数据库动态签发与校验设备专属 Access Token（Bearer 鉴权）。
- **双向实时语音**：WebSocket v1 全双工通信，百炼流式 ASR（服务端 VAD）、流式 LLM（`qwen3.7-flash`）与按句百炼流式 TTS（`qwen-audio-3.0-tts-flash`）。
- **动态 Agent 配置**：ASR、LLM、TTS、提示词、音色及设备类型映射由数据库管理，并在会话建立时按设备类型装配运行时。
- **扩展与工具调用**：支持设备端 JSON-RPC 2.0 MCP 工具（`tools/list`, `tools/call`）与内置服务端工具（时间查询、会话关闭），支持 LLM 多轮工具编排。
- **多数据库支持**：支持 SQLite、MySQL 8 与 PostgreSQL，内置 Goose 嵌入式自动迁移。
- **健壮与并发防护**：具备严格有界的音频队列、背压保护机制、多轮内存历史淘汰及并发会话硬上限。

## AI 方案与技术选型

| 能力 | 实现方案 | 说明 |
| --- | --- | --- |
| 编程语言 | Go 1.26 | 模块化单体，轻量低延时 |
| ASR | 百炼 `qwen-audio-3.0-asr-flash-streaming` | 服务端 VAD 断句，WebSocket 直连 |
| LLM | 百炼兼容接口 `qwen3.7-flash` | OpenAI 官方 Go SDK，流式响应并关闭思考 |
| TTS | 百炼 `qwen-audio-3.0-tts-flash` (`longanlingxi`) | 按句流式合成，输出 24 kHz 单声道 PCM |
| 音频编码 | 原生 `libopus` | 16 kHz 上行解码，24 kHz 下行 60 ms 实时编码 |
| 数据库 | SQLite / MySQL 8 / PostgreSQL | Goose 模式迁移，连接池健康检测 |

## 风险、限制与未决事项

- **用户认证体系（未决事项）**：当前用户绑定接口采用 Mock 用户 Id（`user_id = 1`），生产级多用户体系（如 JWT / OAuth）尚未接入。
- **管理接口鉴权（未决事项）**：管理端凭证生成接口尚未配置权限拦截中间件，必须部署在受信局域网内或由反向代理提供身份验证。
- **网络与安全环境**：公网访问必须由前置反向代理提供 HTTPS/WSS 保护；数据库连接凭据由外部环境注入，AI 服务凭据通过受控管理入口维护且不得在查询结果或日志中返回原文。
- **数据库平滑升级（未决事项）**：复杂生产环境下的数据热迁移和多版本向下兼容策略尚未最终定型。
