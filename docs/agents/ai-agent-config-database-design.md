# ASR、LLM、TTS 与 Agent 数据库配置改造方案

> 目标读者：负责实现、审查、迁移、排错和验证本改造的 Agents  
> 状态：计划中，尚未修改生产代码  
> 适用项目：`xiaozhi-esp32-golang-server`  
> 上游事实基线：[架构与需求详细事实基线](requirements-and-architecture.md)  
> 确认范围：ASR、LLM、TTS、Agent 配置数据库化及运行时装配

## 1. 目的

把当前 YAML 中的 ASR、LLM、TTS、系统提示词和音色迁移到数据库管理，并满足以下能力：

1. ASR、LLM、TTS 各自可以保存多条配置。
2. `agent_config` 自由组合一条 ASR、一条 LLM 和一条 TTS 配置。
3. Agent 独立保存系统提示词和音色。
4. 相同组件配置可以被多个 Agent 复用。
5. 全局使用唯一一条 `enabled = true` 的 Agent 作为当前 Agent。
6. 现有会话固定使用建连时快照，新会话读取最新数据库配置。

本方案只定义改造设计与验收基线，不代表当前代码已经实现。

## 2. 当前事实

根据当前生产代码：

- `internal/config/config.go` 从 YAML 读取 `ai.bailian.*`。
- `DASHSCOPE_API_KEY` 从环境变量读取。
- `session.system_prompt` 保存全局系统提示词。
- `ai.bailian.tts_voice` 保存全局 TTS 音色。
- `cmd/server/main.go` 启动时一次性构造 ASR、LLM、TTS 客户端。
- `session.Handler` 将同一组客户端注入所有 WebSocket 会话。
- 数据库支持 SQLite、MySQL 8、PostgreSQL，使用 Goose 方言迁移。
- `/admin-api` 当前没有应用内鉴权。

## 3. 已确认决策

### 3.1 数据表

本次只新增四张表：

1. `asr_config`
2. `llm_config`
3. `tts_config`
4. `agent_config`

禁止为本需求增加：

- Agent 选择表；
- 通用键值配置表；
- JSON 配置表；
- Agent 与组件的额外映射表；
- 配置历史表。

### 3.2 字段约束

四张表统一遵守：

- `name` 不唯一，只作为展示标签；
- 记录身份和关联只使用主键 ID；
- 不增加配置 `version` 字段；
- 不使用乐观锁和 `expected_version`；
- 不增加 `active_slot`；
- 更新采用按 ID 覆盖，最后一次成功提交生效；
- 保留 `created_at`、`updated_at`，但不把时间戳作为并发版本。

### 3.3 当前 Agent

- `agent_config.enabled = true` 表示当前 Agent。
- 正常应用写入路径必须通过事务保持恰好一个 Agent 为 `enabled = true`。
- 数据库不增加额外唯一列保证该规则。
- 启动或新会话解析时发现零个或多个 enabled Agent，必须报告配置错误。

### 3.4 API Key

- ASR、LLM、TTS 分别保存自己的明文 API Key。
- 三个 Key 可以相同，也可以不同。
- Key 不返回给管理 API 调用方，不进入日志、错误、指标或追踪。
- 数据库账号、数据库备份、快照和导出文件必须按 secret 管理。

### 3.5 系统提示词和音色

- `system_prompt` 保存到 `agent_config`。
- `voice` 保存到 `agent_config`（表示 Agent 当前使用的具体音色）。
- `tts_config.voices` 保存该 TTS 配置支持的音色列表。
- 同一个 TTS 配置可以被多个 Agent 引用，并由各 Agent 使用不同音色。

## 4. 非目标

本期不实现：

- 百炼之外的 AI 供应商；
- 按设备、用户或会话选择 Agent；
- 多个 Agent 同时处于当前状态；
- 配置版本、配置历史、审计记录和一键回滚；
- 配置硬删除 API；
- 第三方服务连通性测试；
- API Key 加密存储；
- 前端管理页面；
- 后台轮询热加载。

## 5. 数据关系

```text
asr_config ──┐
llm_config ──┼──> agent_config
             │      ├── system_prompt
             │      ├── voice
             │      └── enabled（当前 Agent）
tts_config ──┘
```

一个组件可以被多个 Agent 引用：

```text
ASR #1 ──> Agent #1
       └─> Agent #2

TTS #3 ──> Agent #1（voice = longanlingxi）
       └─> Agent #2（voice = longxiaochun）
```

## 6. 逻辑表结构

三种数据库使用各自方言实现相同逻辑结构。字段长度是应用校验和数据库容量边界，不构成名称唯一约束。

### 6.1 `asr_config`

| 字段 | 逻辑类型 | 约束 | 说明 |
| --- | --- | --- | --- |
| `id` | BIGINT | 主键、自增 | 配置身份 |
| `name` | VARCHAR(128) | NOT NULL，非唯一 | 展示名称 |
| `provider` | VARCHAR(64) | NOT NULL DEFAULT '' | 服务提供商/平台（如 `bailian` / `volcengine` / `openai` 等） |
| `endpoint` | VARCHAR(1024) | NOT NULL | ASR WebSocket Endpoint |
| `api_key` | VARCHAR(1024) | NOT NULL | 明文 API Key；迁移默认记录初始为空 |
| `model` | VARCHAR(255) | NOT NULL | ASR 模型 |
| `hotwords` | TEXT | NOT NULL DEFAULT '' | 热词配置，采用 JSON 格式存储（如字符串数组或自定义对象），支持保存大量文本 |
| `connect_timeout_ms` | BIGINT | NOT NULL | 连接超时，毫秒 |
| `enabled` | BOOLEAN | NOT NULL DEFAULT TRUE | 是否允许 Agent 引用 |
| `created_at` | 时间类型 | NOT NULL | 创建时间 |
| `updated_at` | 时间类型 | NOT NULL | 更新时间 |

不建立 `name` 唯一索引。

### 6.2 `llm_config`

| 字段 | 逻辑类型 | 约束 | 说明 |
| --- | --- | --- | --- |
| `id` | BIGINT | 主键、自增 | 配置身份 |
| `name` | VARCHAR(128) | NOT NULL，非唯一 | 展示名称 |
| `provider` | VARCHAR(64) | NOT NULL DEFAULT '' | 服务提供商/平台（如 `bailian` / `openai` / `deepseek` / `ollama` 等） |
| `endpoint` | VARCHAR(1024) | NOT NULL | LLM HTTP Endpoint |
| `api_key` | VARCHAR(1024) | NOT NULL | 明文 API Key；迁移默认记录初始为空 |
| `model` | VARCHAR(255) | NOT NULL | LLM 模型 |
| `first_token_timeout_ms` | BIGINT | NOT NULL | 首 Token 超时，毫秒 |
| `overall_timeout_ms` | BIGINT | NOT NULL | 总超时，毫秒 |
| `enabled` | BOOLEAN | NOT NULL DEFAULT TRUE | 是否允许 Agent 引用 |
| `created_at` | 时间类型 | NOT NULL | 创建时间 |
| `updated_at` | 时间类型 | NOT NULL | 更新时间 |

跨字段约束：

```text
overall_timeout_ms > first_token_timeout_ms
```

### 6.3 `tts_config`

| 字段 | 逻辑类型 | 约束 | 说明 |
| --- | --- | --- | --- |
| `id` | BIGINT | 主键、自增 | 配置身份 |
| `name` | VARCHAR(128) | NOT NULL，非唯一 | 展示名称 |
| `provider` | VARCHAR(64) | NOT NULL DEFAULT '' | 服务提供商/平台（如 `bailian` / `volcengine` / `openai` 等） |
| `endpoint` | VARCHAR(1024) | NOT NULL | TTS WebSocket Endpoint |
| `api_key` | VARCHAR(1024) | NOT NULL | 明文 API Key；迁移默认记录初始为空 |
| `model` | VARCHAR(255) | NOT NULL | TTS 模型 |
| `voices` | TEXT | NOT NULL DEFAULT '' | 支持的音色列表（JSON 格式） |
| `connect_timeout_ms` | BIGINT | NOT NULL | 连接超时，毫秒 |
| `first_audio_timeout_ms` | BIGINT | NOT NULL | 首音频超时，毫秒 |
| `sentence_timeout_ms` | BIGINT | NOT NULL | 单句超时，毫秒 |
| `enabled` | BOOLEAN | NOT NULL DEFAULT TRUE | 是否允许 Agent 引用 |
| `created_at` | 时间类型 | NOT NULL | 创建时间 |
| `updated_at` | 时间类型 | NOT NULL | 更新时间 |

### 6.4 `agent_config`

| 字段 | 逻辑类型 | 约束 | 说明 |
| --- | --- | --- | --- |
| `id` | BIGINT | 主键、自增 | Agent 身份 |
| `name` | VARCHAR(128) | NOT NULL，非唯一 | 展示名称 |
| `asr_config_id` | BIGINT | NOT NULL | 引用 `asr_config.id` |
| `llm_config_id` | BIGINT | NOT NULL | 引用 `llm_config.id` |
| `tts_config_id` | BIGINT | NOT NULL | 引用 `tts_config.id` |
| `system_prompt` | TEXT | NOT NULL | Agent 系统提示词 |
| `voice` | VARCHAR(128) | NOT NULL | Agent 使用的 TTS 音色 |
| `enabled` | BOOLEAN | NOT NULL DEFAULT FALSE | TRUE 表示当前 Agent |
| `created_at` | 时间类型 | NOT NULL | 创建时间 |
| `updated_at` | 时间类型 | NOT NULL | 更新时间 |

索引：

- `idx_agent_config_asr_config_id`
- `idx_agent_config_llm_config_id`
- `idx_agent_config_tts_config_id`
- `idx_agent_config_enabled`

不建立：

- `name` 唯一索引；
- `active_slot`；
- `version`；
- enabled 唯一索引。

## 7. 应用校验规则

### 7.1 通用字段

| 字段 | 校验 |
| --- | --- |
| `name` | 去除首尾空白后非空，UTF-8 字节数不超过 128；允许重复 |
| `provider` | 可选，UTF-8 字节数不超过 64；缺省为 `''` |
| `endpoint` | 非空；ASR/TTS 只允许 `ws`、`wss`；LLM 只允许 `http`、`https` |
| `api_key` | 运行时必须非空，字节数不超过 1024；错误不得回显原值 |
| `model` | 非空，字节数不超过 255 |
| `voices` | 必须为合法 JSON 格式（空值默认规范化为 `[]`），UTF-8 字节数不超过 1048576 (1MB) |
| `system_prompt` | 去除首尾空白后非空，UTF-8 字节数不超过 16384 |
| `voice` | 去除首尾空白后非空，UTF-8 字节数不超过 128 |

### 7.2 超时范围

沿用当前配置规则：

| 字段 | 合法范围 |
| --- | --- |
| ASR `connect_timeout_ms` | 3000 ～ 30000 |
| LLM `first_token_timeout_ms` | 3000 ～ 30000 |
| LLM `overall_timeout_ms` | 10000 ～ 180000，且大于首 Token 超时 |
| TTS `connect_timeout_ms` | 3000 ～ 30000 |
| TTS `first_audio_timeout_ms` | 3000 ～ 30000 |
| TTS `sentence_timeout_ms` | 5000 ～ 60000 |

### 7.3 Agent 引用

保存或启用 Agent 时必须确认：

- 三个组件 ID 均存在；
- 三个组件均为 `enabled = true`；
- 系统提示词和音色合法；
- 不复制组件字段到 Agent 表。

## 8. 当前 Agent 选择语义

### 8.1 正常状态

数据库中必须恰好存在一条：

```text
agent_config.enabled = true
```

其他 Agent 为 false。

### 8.2 激活事务

`ActivateAgent(agentID)` 固定执行：

1. 开启写事务。
2. 锁定 `agent_config` 当前全部记录，保证激活操作串行执行。
   - MySQL/PostgreSQL 使用行锁查询；
   - SQLite 依靠写事务串行化。
3. 按 ID 查询目标 Agent。
4. 校验目标 Agent 的三个组件存在且 enabled。
5. 将全部 Agent 的 enabled 更新为 false。
6. 将目标 Agent 的 enabled 更新为 true。
7. 在事务内再次统计 enabled Agent，必须恰好为一条。
8. 提交事务。

不接收 expected version。并发激活采用最后一次成功提交生效，但每次成功提交后必须保持恰好一个 enabled Agent。

### 8.3 异常状态

若直接 SQL 或其他绕过应用的写入造成零个或多个 enabled Agent：

- 进程启动时拒绝就绪；
- 新 WebSocket 会话拒绝建立；
- 已建立会话继续使用自己的快照；
- 管理员通过受保护激活接口或直接修复数据库恢复状态。

## 9. 数据访问接口职责

数据访问层按稳定业务能力提供显式方法，不提供通用 CRUD 或通用配置仓库。

### 9.1 组件方法

每种组件分别提供：

- `List...Configs`
- `Find...ConfigByID`
- `Create...Config`
- `Update...ConfigByID`

更新规则：

- 按主键 ID 覆盖；
- 不按名称更新；
- 不比较 version；
- 更新结果影响行数为 0 时返回 Not Found；
- 更新 API Key 时，调用方先合并“省略则保留”的 write-only 语义。

### 9.2 Agent 方法

提供：

- `ListAgentConfigs`
- `FindAgentConfigByID`
- `CreateAgentConfig`
- `UpdateAgentConfigByID`
- `ActivateAgent`
- `FindActiveAgentRuntimeSnapshot`

`FindActiveAgentRuntimeSnapshot` 使用 Agent JOIN 三个组件，返回单个完整快照；不得分四次查询后在事务外拼装。

## 10. 运行时快照

逻辑结构：

```text
RuntimeSnapshot
├── Agent
│   ├── ID
│   ├── Name
│   ├── SystemPrompt
│   └── Voice
├── ASRConfig
├── LLMConfig
└── TTSConfig
```

快照不包含配置 version。

## 11. 服务启动流程

```text
严格加载基础 YAML 与环境变量
        |
        v
打开数据库并执行 00002 迁移
        |
        v
必要时执行默认 API Key 一次性初始化
        |
        v
JOIN 读取唯一 enabled Agent 与三个组件
        |
        v
完整校验 Agent 快照
        |
        v
使用 Agent.voice 构造 TTS 客户端并验证依赖可构造
        |
        v
启动 HTTP 服务
```

失败行为：

- 当前 Agent 数量不是 1：拒绝启动；
- Agent 引用不存在或禁用组件：拒绝启动；
- API Key 为空：拒绝启动；
- 系统提示词或音色非法：拒绝启动；
- 客户端构造失败：拒绝启动；
- 不回退到旧 YAML 或内置 AI 配置。

## 12. API Key 初始化

### 12.1 迁移默认值

Goose 迁移创建：

- 默认 ASR 配置；
- 默认 LLM 配置；
- 默认 TTS 配置；
- 默认 Agent。

默认组件 Endpoint、模型和超时与当前示例配置保持一致。API Key 初始为空，默认 Agent：

- 引用三个默认组件；
- `system_prompt` 使用当前默认系统提示词；
- `voice` 使用当前默认音色 `longanlingxi`；
- `enabled = true`。

### 12.2 一次性 bootstrap

`DASHSCOPE_API_KEY` 改为可选 bootstrap 输入：

- 三个默认 Key 均为空且环境变量非空：在事务中把同一 Key 写入三个默认组件；
- 三个数据库 Key 已完整配置：忽略环境变量；
- 三个 Key 部分为空：拒绝自动初始化并拒绝启动；
- 初始化完成后运行时只读取数据库。

运维也可以在首次启动前直接写入三个独立 Key。

## 13. 新会话装配

由于没有配置 version，本期不维护版本缓存。

每个新 WebSocket 会话在协议升级前：

1. 完成设备 Token 鉴权。
2. JOIN 查询唯一 enabled Agent 与三个组件。
3. 校验完整快照。
4. 使用共享 HTTP Transport 构造该会话的 ASR、LLM、TTS 客户端配置对象。
5. 将系统提示词、音色和三个客户端注入 Session。
6. 再执行会话准入和 WebSocket 升级。

会话语义：

- Session 固定持有建连时的系统提示词、音色和客户端组；
- 管理员覆盖组件或 Agent 后，只影响后续新会话；
- 已有会话不中断、不切换 Agent；
- 不增加后台轮询 goroutine。

## 14. 管理接口鉴权

新增可修改 Endpoint 和明文 Key 的接口前，必须保护整个 `/admin-api`：

- 环境变量：`ADMIN_API_TOKEN`；
- 请求头：`Authorization: Bearer <token>`；
- 使用常量时间比较；
- 缺失或错误返回 HTTP 401；
- Token 不进入日志、响应或数据库。

该变化同时保护现有设备凭证生成接口，属于明确的兼容性变化。

## 15. 管理 API

所有请求严格 JSON 解码，拒绝未知字段；继续使用项目已有请求体和 Header 大小限制。

### 15.1 ASR 配置

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/admin-api/asr-config/list` | 分页查询，支持 `name`、`enabled` 过滤；重名返回多条 |
| POST | `/admin-api/asr-config/save` | 无 ID 创建，有 ID 按 ID 覆盖 |

保存示例：

```json
{
  "id": 12,
  "name": "百炼流式识别",
  "endpoint": "wss://example.com/asr",
  "api_key": "optional-write-only-key",
  "model": "qwen-audio-3.0-asr-flash-streaming",
  "connect_timeout": "10s",
  "enabled": true
}
```

创建时 `api_key` 必填；更新时省略表示保留数据库原值，空字符串非法。

### 15.2 LLM 配置

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/admin-api/llm-config/list` | 分页查询 |
| POST | `/admin-api/llm-config/save` | 创建或按 ID 覆盖 |

请求包含：

- `id`
- `name`
- `endpoint`
- write-only `api_key`
- `model`
- `first_token_timeout`
- `overall_timeout`
- `enabled`

### 15.3 TTS 配置

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/admin-api/tts-config/list` | 分页查询 |
| POST | `/admin-api/tts-config/save` | 创建或按 ID 覆盖 |

请求包含：

- `id`
- `name`
- `endpoint`
- write-only `api_key`
- `model`
- `voices`
- `connect_timeout`
- `first_audio_timeout`
- `sentence_timeout`
- `enabled`

### 15.4 Agent 配置

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/admin-api/agent-config/list` | 查询 Agent 列表 |
| POST | `/admin-api/agent-config/save` | 创建或按 ID 覆盖组合、提示词和音色 |
| GET | `/admin-api/agent-config/active` | 查询当前 enabled Agent 完整脱敏快照 |
| POST | `/admin-api/agent-config/activate` | 按 Agent ID 事务切换当前 Agent |

保存示例：

```json
{
  "id": 8,
  "name": "儿童助手",
  "asr_config_id": 1,
  "llm_config_id": 3,
  "tts_config_id": 2,
  "system_prompt": "你是一个适合儿童使用的语音助手。",
  "voice": "longxiaochun"
}
```

`save` 不直接修改 enabled：

- 新建 Agent 默认 `enabled = false`；
- 当前 Agent 只能通过 activate 接口切换。

激活请求：

```json
{
  "id": 8
}
```

### 15.5 响应脱敏

组件响应：

```json
{
  "id": 12,
  "name": "百炼流式识别",
  "has_api_key": true,
  "model": "qwen-audio-3.0-asr-flash-streaming"
}
```

禁止返回：

- `api_key`；
- Key 掩码、前后缀或长度；
- 数据库 DSN；
- 管理 Token。

Agent 管理接口可以返回完整系统提示词和音色，因为它们是受保护接口所管理的业务配置；日志仍不得输出完整系统提示词。

### 15.6 状态码

| 状态码 | 场景 |
| --- | --- |
| 200 | 查询或保存成功 |
| 400 | JSON、字段、URL、超时、提示词或音色非法 |
| 401 | 管理鉴权失败 |
| 404 | 按 ID 查询的配置不存在 |
| 409 | 试图禁用当前 Agent 使用的组件等业务状态冲突 |
| 413 | 请求体超限 |
| 500 | 数据库或内部错误 |

不返回基于 version 的 HTTP 409。

## 16. 禁用规则

### 16.1 组件

组件 `enabled = false` 表示不能被当前 Agent 使用。

更新组件为 disabled 前：

- 如果该组件被当前 enabled Agent 引用，返回 HTTP 409；
- 如果只被非当前 Agent 引用，可以禁用；这些 Agent 之后不能激活，直到更换组件或重新启用组件。

### 16.2 Agent

Agent 的 enabled 同时表示“当前 Agent”，不作为普通保存字段暴露：

- 新 Agent 默认 false；
- save 不允许直接修改 enabled；
- activate 负责切换；
- 不提供单独禁用当前 Agent 的接口，避免出现无当前 Agent。

## 17. 代码影响范围

### 17.1 `internal/config`

- 删除 YAML `AIConfig`、`BailianConfig` 运行来源；
- 删除 `SessionConfig.SystemPrompt`；
- 保留 URL、超时等统一校验能力并迁移到数据库运行类型；
- `DASHSCOPE_API_KEY` 改为可选 bootstrap 输入；
- 新增 `ADMIN_API_TOKEN` 环境变量校验。

### 17.2 `internal/database`

新增：

- 四个 GORM 映射类型；
- 四类显式数据访问方法；
- Agent JOIN 快照读取；
- Agent 激活事务；
- SQLite/MySQL/PostgreSQL 的 `00002_ai_agent_configs.sql`。

### 17.3 `internal/ai/bailian`

- 三个构造函数不再接收整个 `*config.Config`；
- ASR、LLM 各自接收自己的数据库配置；
- TTS 接收 TTS 配置和 Agent 音色；
- API Key 来自对应组件记录。

### 17.4 `internal/session`

- Handler 依赖 Agent 解析器，不再固定持有全局三个客户端；
- 新会话解析数据库 Agent 快照；
- Session 显式持有系统提示词；
- `buildLLMMessages` 使用 Session 的 Agent 提示词；
- 现有会话生命周期和编排状态机不改变。

### 17.5 `internal/router`

- 增加统一 Admin Bearer 鉴权；
- 增加四类管理接口；
- 请求严格解析和 Key 脱敏；
- 复用现有请求体、Header 上限与 JSON 响应能力。

### 17.6 `cmd/server`

装配顺序调整为：

```text
基础配置 -> 数据库 -> bootstrap -> Agent 解析器 -> Admin/Session Handler -> HTTP Server
```

## 18. 数据迁移与发布步骤

### 18.1 发布前

1. 备份数据库。
2. 记录当前 YAML 中的 Endpoint、模型、超时、系统提示词和音色。
3. 准备 `ADMIN_API_TOKEN`。
4. 保留当前 `DASHSCOPE_API_KEY` 供首次 bootstrap。
5. 从部署 YAML 中删除旧 `ai:` 和 `session.system_prompt`，避免严格解析失败。

### 18.2 首次启动

1. Goose 创建四张表并写入默认记录。
2. 服务将 bootstrap Key 写入三个默认组件。
3. 服务解析默认 Agent 并完成启动。
4. 使用受保护 Admin API 写入实际自定义配置。
5. 查询 `/admin-api/agent-config/active` 核对当前 Agent。
6. 用测试设备建立新会话验证 ASR、LLM、TTS、提示词和音色。
7. 验证后再开放设备流量。

不自动从旧 YAML 导入非敏感自定义值，避免长期保留双读和兼容壳。

### 18.3 回滚

1. 停止新二进制。
2. 恢复旧版 YAML 中的 `ai`、`session.system_prompt` 和 TTS 音色。
3. 恢复旧二进制及 `DASHSCOPE_API_KEY`。
4. 启动旧服务并验证设备会话。
5. 新增四张表可以保留；正常回滚不执行 Goose Down。

## 19. 安全要求

- 数据库最小权限访问；
- 数据库备份和导出加密、限权保存；
- 禁止在日志中输出 API Key、管理 Token、完整系统提示词；
- 保存 Endpoint 的接口必须受 Admin Token 保护，防止将 Key 转发到攻击者 Endpoint；
- 错误只返回字段路径和安全文案；
- GORM 模型的 API Key 字段使用 `json:"-"`；
- API DTO 与数据库模型分离；
- 不把 API Key 放入指标 label、追踪属性或 panic。

## 20. 测试方案

禁止新增或保留墓碑测试。测试只验证当前生产契约。

### 20.1 配置校验

- 三类 URL scheme；
- 超时最小、最大和越界；
- LLM 总超时关系；
- 空 Key；
- 空/超长系统提示词；
- 空/超长音色；
- 无效组件引用。

### 20.2 数据库

- 新建和升级 SQLite 临时数据库；
- 原设备数据保持不变；
- 重名记录创建成功；
- 按 ID 查询和覆盖；
- Agent 引用和组件复用；
- 明文 Key、提示词和音色持久化；
- 激活事务结束后恰好一个 enabled Agent；
- 并发激活最后一次成功提交生效且不会留下多个 enabled Agent。

### 20.3 Admin API

- 管理鉴权缺失、错误和成功；
- 严格 JSON 与请求体上限；
- 重名配置；
- 组件创建、覆盖、Key 保留和 Key 轮换；
- Agent 创建、重组、提示词和音色更新；
- 激活 Agent；
- 禁用当前组件冲突；
- 所有响应不包含 API Key。

### 20.4 Session

- 新会话使用 enabled Agent；
- 不同 Agent 复用同一组件；
- Agent 切换后旧会话不变；
- 新会话使用新的系统提示词和音色；
- 零个/多个 enabled Agent 时拒绝新会话；
- 数据库失败和客户端构造失败时拒绝新会话；
- race detector 通过。

### 20.5 最终命令

```bash
go test ./...
go test -race ./...
go build ./...
```

MySQL、PostgreSQL 若提供测试 DSN，应额外执行对应迁移集成测试；没有外部测试环境时必须明确记录未验证项，不得假装已验证。

## 21. 风险与处置

| 风险 | 处置 |
| --- | --- |
| 明文 Key 泄露 | 限制数据库与备份权限，API 永不返回 Key |
| 名称重复导致误选 | UI/API 始终展示和提交主键 ID，名称只作标签 |
| 无 version 导致覆盖更新 | 明确最后一次成功提交生效，不声明冲突检测能力 |
| 无数据库唯一约束保证当前 Agent | 激活事务锁定 Agent 记录并在提交前验证数量；启动和新会话再次校验 |
| 每个新会话构造客户端有额外开销 | 复用底层 HTTP Transport；当前会话规模下优先保证简单正确 |
| 管理员写入恶意 Endpoint | 整个 Admin API 强制 Bearer 鉴权，并限制 URL scheme |
| 直接 SQL 绕过校验 | 启动与新会话都执行完整快照校验，错误时拒绝使用 |
| 旧自定义 YAML 不自动迁移 | 发布前记录，首次启动后通过 Admin API 显式写入 |

## 22. 完成定义

满足以下全部条件才算完成：

- 只新增四张约定表；
- 四张表名称允许重复；
- 四张表没有配置 version；
- `agent_config` 没有 active_slot；
- Agent 保存三个组件 ID、系统提示词和音色；
- TTS 配置不再保存音色；
- YAML 不再保存 AI 配置和系统提示词；
- API Key 明文保存在对应组件表且 API 不返回；
- 正常写入路径保持恰好一个 enabled Agent；
- 新会话读取数据库 Agent，旧会话保持快照；
- Admin API 已鉴权；
- 没有遗留兼容壳、孤儿代码或墓碑测试；
- 普通测试、race test 和 build 全部通过。
