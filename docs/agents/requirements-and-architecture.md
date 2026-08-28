# xiaozhi-esp32-golang-server 架构与需求详细事实基线

> 目标读者：负责实现、审查、排错和验证本项目的 Agents
> 状态：生产代码已包含最小语音链路、多数据库持久化、设备激活绑定、Admin/User API 与 MCP 工具调用
> 确认日期：2026-08-28
> 适用项目：`xiaozhi-esp32-golang-server`
> 文档角色：系统事实基线与验收约束
> Agent 规范：[项目 AGENTS.md](../../AGENTS.md)
> 人类摘要：[项目概览](../humans/project-overview.md)

## 1. 文档目的与范围

本文档是 `xiaozhi-esp32-golang-server` 的唯一技术事实基线，用于驱动后续实现、重构、审查与测试验证。

系统提供面向 `xiaozhi-esp32` 固件的服务端能力：
1. **OTA 与设备生命周期**：固件配置发现、eFuse HMAC 硬件激活、用户设备绑定、出厂凭证管理、Access Token 签发与鉴权。
2. **双向语音对话**：WebSocket v1 Opus 音频上下行、百炼流式 ASR（服务端 VAD）、百炼流式 LLM（`qwen3.7-flash`）、按句百炼流式 TTS（`qwen-audio-3.0-tts-flash`）与实时节奏 Opus 编码下发。
3. **工具与扩展**：JSON-RPC 2.0 客户端 MCP 工具调用、内置服务端工具（时间查询、会话关闭）及 LLM 多轮工具编排。
4. **持久化与存储**：支持 SQLite、MySQL 8、PostgreSQL 三种驱动，基于 Goose 的统一数据库迁移。

## 2. 事实来源与优先级

1. **设备协议事实**：以 `xiaozhi-esp32` 固件源码为准（`ota.cc`、`application.cc`、`protocols/websocket_protocol.cc`）。
2. **代码实现事实**：以当前服务端生产代码、Goose 迁移脚本及测试用例为准。
3. **第三方供应商事实**：以阿里云百炼官方 WebSocket/HTTP 协议文档为准。
4. **文档同步关系**：本文档为技术实施事实基线；[人类项目概览](../humans/project-overview.md) 仅作高层精简摘要。

## 3. 架构与 Package 边界

### 3.1 模块与职责

- `cmd/server`：服务入口，负责配置加载、数据库初始化、各适配器与 Handler 装配、优雅退出。
- `internal/config`：严格 YAML 与环境变量解析校验，强制合法区间检查。
- `internal/database`：数据库连接池管理、Goose 自动迁移、出厂凭据/激活/用户绑定/Access Token 数据访问。
- `internal/router`：HTTP 路由分发，包含 OTA 配置发现/激活 (`/xiaozhi/ota`)、管理接口 (`/admin-api`)、用户绑定 (`/user-api`)。
- `internal/session`：WebSocket 生命周期、握手 Bearer 鉴权、状态机、多轮对话编排、MCP 客户端协议、内置服务端工具、流式背压与并发控制。
- `internal/audio`：16 kHz / 24 kHz Opus 编解码、PCM 帧缓冲区与实时节奏控制。
- `internal/ai`：ASR、LLM、TTS 统一接口定义与值对象。
- `internal/ai/bailian`：阿里云百炼 ASR、LLM、TTS 适配器实现。
- `internal/logger`：基于 `slog` 的结构化日志与字段脱敏截断。
- `internal/server`：HTTP 服务生命周期封装。

### 3.2 依赖拓扑

```text
HTTP / WebSocket 入口 (internal/router)
  ├── OTA / Admin / User API  ──>  internal/database
  └── WebSocket Session (internal/session)
        ├── internal/database (Bearer Token 鉴权)
        ├── internal/audio (Opus 编解码)
        └── internal/ai (ASR / LLM / TTS 边界)
              └── internal/ai/bailian (百炼适配器)
```

## 4. 数据库与持久化基线

### 4.1 驱动与迁移

- **支持驱动**：`sqlite`、`mysql`、`postgres`（通过 `database.driver` 配置）。
- **DSN 注入**：敏感 DSN 必须通过环境变量 `DATABASE_DSN` 注入。
- **迁移引擎**：使用 Goose 嵌入式迁移，启动时自动按对应驱动执行 `00001_init_schema.sql`。

### 4.2 数据表结构

1. **`device_hmac_credential`（出厂与激活 HMAC 凭证表）**
   - `id`: 主键自增。
   - `serial_number`: VARCHAR(64) UNIQUE NOT NULL，设备序列号。
   - `auth_method`: VARCHAR(32) NOT NULL DEFAULT 'efuse_hmac'（可选 `efuse_hmac` / `activation_code` / `manual_code_hmac`）。
   - `hmac_key_ciphertext`: VARCHAR(64) NOT NULL，64 位十六进制 HMAC 密钥。
   - `credential_status`: VARCHAR(16) NOT NULL DEFAULT 'enabled'（`enabled` / `activated` / `blocked` / `revoked`）。
   - `created_at`, `updated_at`: DATETIME NOT NULL。

2. **`device_activation`（设备激活关系表）**
   - `id`: 主键自增。
   - `serial_number`: VARCHAR(64) UNIQUE NOT NULL。
   - `device_id`: VARCHAR(64) NOT NULL（带普通索引 `idx_device_id`）。
   - `client_id`: VARCHAR(64) DEFAULT NULL。
   - `activation_status`: VARCHAR(16) NOT NULL DEFAULT 'active'（`active` / `frozen` / `revoked`）。
   - `activated_at`, `created_at`, `updated_at`: DATETIME NOT NULL。

3. **`device_user_ref`（设备与用户绑定关系表）**
   - `id`: 主键自增。
   - `serial_number`: VARCHAR(64) UNIQUE NOT NULL。
   - `user_id`: BIGINT/INTEGER NOT NULL（带联合索引 `idx_user_id_serial_number (user_id, serial_number)`）。
   - `created_at`, `updated_at`: DATETIME NOT NULL。

4. **`device_access_token`（设备鉴权 Access Token 表）**
   - `id`: 主键自增。
   - `serial_number`: VARCHAR(64) UNIQUE NOT NULL。
   - `access_token`: VARCHAR(128) UNIQUE NOT NULL，明文 Access Token。
   - `has_exposed`: INTEGER NOT NULL DEFAULT 0（0: 待下发, 1: 已下发）。
   - `issued_at`: DATETIME NOT NULL。
   - `expires_at`: DATETIME DEFAULT NULL（NULL 表示不过期）。
   - `revoked_at`: DATETIME DEFAULT NULL（NULL 表示未撤销）。
   - `created_at`, `updated_at`: DATETIME NOT NULL。

## 5. HTTP API 契约

### 5.1 OTA 配置发现与激活 (`/xiaozhi/ota`)

#### 1) 配置发现：`GET|POST /xiaozhi/ota/`
- **请求头**：`Activation-Version`（如 `2`）、`Device-Id`、`Client-Id`、`Serial-Number`。
- **业务逻辑**：
  - 若设备已绑定用户（`device_user_ref` 存在）：
    - 查 `device_access_token`；
    - 若 `has_exposed == 0`，返回 HTTP 200 并在 JSON 中携带 `websocket.token`，同时将 `has_exposed` 置为 1；
    - 若 `has_exposed == 1`，返回 HTTP 200，JSON 仅含 `url` 与 `version`，不返回 `token`。
  - 若设备未绑定或未激活：
    - 生成 256-bit Challenge 和 6 位数字激活码（内存 TTL 5 分钟，容量 10000），返回 HTTP 401 Unauthorized：
      ```json
      {
        "challenge": "<hex-challenge>",
        "code": "123456",
        "message": "请在管理后台绑定设备"
      }
      ```
- **成功响应示例 (HTTP 200)**：
  ```json
  {
    "websocket": {
      "url": "wss://example.com/xiaozhi/v1/",
      "token": "generated-access-token",
      "version": 1
    }
  }
  ```

#### 2) 硬件签名激活：`POST /xiaozhi/ota/activate`
- **请求参数/请求头**：`challenge`, `hmac`, `serial_number`。
- **验证**：使用 `device_hmac_credential` 中的密钥对 Challenge 执行 HMAC-SHA256 常量时间校验。
- **状态变更**：向 `device_activation` 插入/更新激活记录，更新凭证状态为 `activated`。
- **响应 (HTTP 200)**：`{"success": true, "message": "device activated successfully"}`。

### 5.2 用户绑定 API (`POST /user-api/device/bind`)

- **请求体**：
  ```json
  {
    "code": "123456",
    "sn": "optional-for-without-sn-mode",
    "hmac": "optional-for-without-sn-mode"
  }
  ```
- **处理分支**：
  - **带 SN 流程**（OTA 已提供 SN 并通过硬件验证）：根据 Code 找到 SN，写入 `device_activation` 与 `device_user_ref`（绑定当前 `user_id`），生成 32 字节随机 Hex Token 写入 `device_access_token`（`has_exposed = 0`），清理 Code 缓存。
  - **无 SN 流程**（硬件未提供 SN）：请求必须提供 `code`、`sn`、`hmac`；服务端校验 HMAC-SHA256 后，写入激活表、绑定表、凭证状态并生成 Access Token，清理 Code 缓存。
- **响应 (HTTP 200)**：
  ```json
  {
    "success": true,
    "serial_number": "SN...",
    "device_id": "...",
    "client_id": "...",
    "user_id": 1
  }
  ```

### 5.3 管理 API (`POST /admin-api/device-hmac-credential/generate`)

- **请求体**：`{"count": 10}`（取值范围 1 ~ 1000，默认 1）。
- **业务逻辑**：批量生成 32 字符 hex 格式 SN 和 32 字节 (64 hex) HMAC Key，存入 `device_hmac_credential`（`auth_method = efuse_hmac`, `credential_status = enabled`）。
- **响应 (HTTP 200)**：返回生成的凭证列表及明文 HMAC Key。

## 6. WebSocket v1 协议与会话控制

### 6.1 握手与鉴权
- **路径**：`GET /xiaozhi/v1/`
- **鉴权**：请求头必须携带 `Authorization: Bearer <access_token>`。服务端查询 `device_access_token` 表，验证 Token 存在、未撤销（`revoked_at IS NULL`）且未过期（`expires_at IS NULL OR expires_at > NOW()`）。
- **协议版本**：要求请求头 `Protocol-Version: 1`。
- **并发保护**：活跃会话数达到 `max_concurrent_sessions` 时在握手阶段直接返回 HTTP 503。

### 6.2 客户端/服务端 Hello
- 客户端在升级后必须在 `hello_timeout`（默认 10s）内发送 `hello` 消息，协商音频参数（16000 Hz, 单声道, 60 ms Opus）。
- 服务端响应 `hello`，下发 `session_id` 及服务端音频参数（24000 Hz, 单声道, 60 ms Opus）。

### 6.3 文本消息集
- **设备上行**：`hello`、`listen`（`start`/`stop`/`detect`）、`abort`、`mcp`。
- **服务端下行**：`hello`、`stt`、`tts`（`start`/`sentence_start`/`stop`）、`mcp`。

### 6.4 二进制音频流
- 每个 WebSocket 二进制消息承载单帧 60 ms Opus 包。
- 上行音频按 16000 Hz 解码为 PCM，送入百炼流式 ASR。
- 下行音频按 24000 Hz 编码为 60 ms Opus 帧，按实时节奏推送到设备。

## 7. 工具调用与编排（MCP & Server Tools）

### 7.1 工具类型与优先级
1. **服务端内置工具（Server Tools，最高优先级）**：
   - `server.get_current_time`：获取系统当前日期、时间、星期、时区及 UTC 偏移。
   - `server.close_session`：关闭当前会话并断开 WebSocket 连接。
2. **设备 MCP 工具（JSON-RPC 2.0）**：
   - 设备在连接建立后或通过 `mcp` 消息提供声明。
   - 服务端通过 `tools/list` 分页获取设备工具定义。
   - 大模型触发调用时，服务端通过 `tools/call` 发送 JSON-RPC 2.0 请求（默认超时 10s）并等待设备回包。

### 7.2 工具编排规则
- **工具去重**：服务端工具优先，设备同名工具被忽略。
- **多轮流式编排**：大模型生成 Tool Call -> 服务端/设备执行工具 -> 工具执行结果组装为 `tool` 消息追加到上下文 -> 再次驱动 LLM 生成最终语音文本回复 -> 触发分句 TTS。

## 8. 状态机与并发资源边界

### 8.1 会话状态机

```text
CONNECTED -> READY -> LISTENING -> PROCESSING -> SPEAKING
                 ^                       |           |
                 +-----------------------+-----------+
```

- 任何状态均可响应 `abort`、错误或连接断开直接进入清理逻辑。
- 异步任务均绑定 Generation 代次，过期代次的结果直接丢弃。

### 8.2 资源边界与超时总表

| 配置项 / 边界 | 类型 / 单位 | 默认值 | 合法区间 | 超限 / 失败行为 |
| --- | --- | --- | --- | --- |
| `max_concurrent_sessions` | 整数（个） | 必填 | `[1, 10000]` | 达到上限时握手返回 HTTP 503 |
| `max_http_body_bytes` | 整数（字节） | `65536` (64 KiB) | `[1024, 10485760]` | 超限中止并返回 HTTP 413 |
| `max_http_header_bytes` | 整数（字节） | `1024` | `[128, 8192]` | 超限返回 HTTP 400 或 431 |
| `max_ws_text_message_bytes` | 整数（字节） | `32768` (32 KiB) | `[4096, 524288]` | 超限发送 1009 并关闭连接 |
| `max_opus_packet_bytes` | 整数（字节） | `1024` | `[128, 4096]` | 超限或空包发送 1003/1008 并关闭 |
| `asr_pcm_queue_capacity` | 整数（帧） | `100` | `[20, 500]` | 队列满背压丢弃；阻塞超 5s 取消 ASR |
| `tts_pcm_queue_capacity` | 整数（块） | `100` | `[20, 500]` | 队列满暂停读取；阻塞超 5s 取消 TTS |
| `downlink_opus_queue_capacity` | 整数（包） | `100` | `[20, 500]` | 队列满触发背压，主动关闭设备连接 |
| `max_listening_duration` | 时长 | `30s` | `[5s, 120s]` | 超限取消收音并关闭连接 |
| `hello_timeout` | 时长 | `10s` | `[3s, 30s]` | 超限发送 1008 并关闭连接 |
| `asr_connect_timeout` | 时长 | `10s` | `[3s, 30s]` | 超限取消 ASR 并记录 ERROR |
| `tts_connect_timeout` | 时长 | `10s` | `[3s, 30s]` | 超限取消 TTS 并记录 ERROR |
| `llm_first_token_timeout` | 时长 | `15s` | `[3s, 30s]` | 超限取消 LLM 并记录 ERROR |
| `llm_overall_timeout` | 时长 | `60s` | `[10s, 180s]` | 超限取消 LLM（需 > 首 token 超时） |
| `tts_first_audio_timeout` | 时长 | `10s` | `[3s, 30s]` | 超限取消 TTS 并记录 ERROR |
| `tts_sentence_timeout` | 时长 | `15s` | `[5s, 60s]` | 超限取消 TTS 并记录 ERROR |
| `shutdown_timeout` | 时长 | `10s` | `[1s, 60s]` | 优雅退出最大宽限期 |
| `max_history_turns` | 整数（轮） | `6` | `[1, 50]` | FIFO 滚动淘汰最旧完整对话 |
| 数据库连接数 (`max_open_conns`) | 整数 | `1` (SQLite) / 动态 | `[1, 1000]` | 超过连接池限制排队等待 |
| 数据库 Ping 超时 (`ping_timeout`) | 时长 | `3s` | `[500ms, 30s]` | 启动握手失败拒绝启动 |

## 9. 配置与环境变量

### 9.1 YAML 配置结构

```yaml
server:
  listen_addr: ":8080"
  websocket_url: "wss://example.com/xiaozhi/v1/"
  max_concurrent_sessions: 10
  shutdown_timeout: 10s
  http_read_timeout: 15s
  http_write_timeout: 30s
  http_idle_timeout: 60s
  max_http_body_bytes: 65536
  max_http_header_bytes: 1024

session:
  hello_timeout: 10s
  max_ws_text_message_bytes: 32768
  max_opus_packet_bytes: 1024
  max_listening_duration: 30s
  asr_pcm_queue_capacity: 100
  tts_pcm_queue_capacity: 100
  downlink_opus_queue_capacity: 100
  max_history_turns: 6
  system_prompt: "你是小智，一个智能语音助手。请用简明、友好的中文回答，回答适合直接语音朗读。"
  listen_prompt_enabled: true

ai:
  bailian:
    ws_endpoint: "wss://llm-hi9nns9y8jekpmpt.cn-beijing.maas.aliyuncs.com/api-ws/v1/inference"
    llm_endpoint: "https://llm-hi9nns9y8jekpmpt.cn-beijing.maas.aliyuncs.com/compatible-mode/v1"
    asr_model: "qwen-audio-3.0-asr-flash-streaming"
    llm_model: "qwen3.7-flash"
    tts_model: "qwen-audio-3.0-tts-flash"
    tts_voice: "longanlingxi"
    asr_connect_timeout: 10s
    tts_connect_timeout: 10s
    llm_first_token_timeout: 15s
    llm_overall_timeout: 60s
    tts_first_audio_timeout: 10s
    tts_sentence_timeout: 15s

proxy:
  enabled: false
  url: ""

database:
  driver: "sqlite"                      # sqlite / mysql / postgres
  max_open_conns: 1
  max_idle_conns: 1
  connection_max_lifetime: 0s
  connection_max_idle_time: 0s
  ping_timeout: 3s
```

### 9.2 环境变量
- `DASHSCOPE_API_KEY`：阿里云百炼统一 API 密钥。
- `DATABASE_DSN`：数据库连接串。

## 10. 安全限制、脱敏与未决事项

### 10.1 脱敏与日志规范
- 常量时间比较（`subtle.ConstantTimeCompare`）校验 HMAC 及 Access Token。
- 严禁在日志中输出 `Authorization`、Access Token、HMAC Key、系统提示词或原始音频 PCM/Opus。
- 设备标识与未识别 Payload 日志输出严格按 64 字符截断。
- 异常诊断日志强制每秒 1 条限频（burst 3）。

### 10.2 未决事项（明确标记）

1. **用户认证与授权体系（未决事项）**：
   - 当前 `/user-api/device/bind` 使用硬编码 Mock 用户（`MockCurrentUserID = 1`），生产级多用户体系（如 JWT / OAuth / Session）及权限校验尚未确定与接入。
2. **管理接口鉴权模型（未决事项）**：
   - 当前 `/admin-api/device-hmac-credential/generate` 未挂载身份认证中间件，当前阶段必须仅部署于受信任内网或由外部反向代理（如 Basic Auth / mTLS）进行访问保护。
3. **生产数据库升级与兼容策略（未决事项）**：
   - 生产环境下的平滑热迁移、多实例并发 DDL/DML 锁机制以及回滚兼容策略尚未最终制定。
4. **重新激活与绑定事务原子性收敛（未决事项）**：
   - 重新激活时旧凭据级联失效与并发绑定事务边界处于持续收敛优化阶段。
