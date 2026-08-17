# xiaozhi-esp32-golang-server 项目概览

> 目标读者：需要快速了解项目的人类读者  
> 状态：已确认  
> 更新日期：2026-08-17  
> 详细基线：[项目需求与架构约束](../agents/requirements-and-architecture.md)  
> 文档规范：[docs/README.md](../README.md)

## 项目目标

为当前 `xiaozhi-esp32` 固件提供一个可私有部署的 Go 服务端，在不修改 v1 固件的前提下完成设备激活、WebSocket 双向语音和 ASR -> LLM -> TTS 对话。

首期面向单租户、单实例部署，最多支持 10 路同时活跃的语音会话。

## 首期能力

- OTA/配置发现，以及设备 WebSocket 地址和凭证下发。
- v1 激活码与管理员确认流程。
- v2 challenge/HMAC 协议 Mock。
- WebSocket 二进制协议 v1、v2、v3。
- Opus 音频、Silero VAD、语音识别、流式对话和语音合成。
- 对话打断、取消、重连和有限文本历史。
- 安全管理 API、OpenAPI 文档、健康检查、日志和指标。

## 关键方案

| 项目 | 方案 |
| --- | --- |
| 服务形态 | Go 1.26 模块化单体，单进程、单可执行程序 |
| 部署 | Linux 容器，支持 `amd64` 和 `arm64` |
| 设备传输 | WebSocket；首期不支持 MQTT + UDP |
| AI 链路 | OpenAI 兼容 ASR、流式 Chat Completions 和 TTS |
| 音频 | 上行 16 kHz Opus；下行固定 24 kHz PCM 转 60 ms Opus 帧 |
| 数据库 | MySQL 8；PostgreSQL 仅保留未来扩展边界 |
| 设备认证 | 每台设备独立 Bearer Token |
| 管理认证 | HTTP Basic，TLS 由反向代理终止 |
| 数据留存 | 只保存文本和必要指标，不保存原始音频 |

## 激活与安全边界

- v1 设备通过激活码由管理员确认，设备声明本身不能证明身份。
- v2 首期只硬编码一组测试序列号和 HMAC Key，用于验证协议流程。
- v2 Mock 不能用于生产身份认证；拿到源码的人能够伪造该测试设备。
- 正式部署必须使用 HTTPS/WSS，Token、AI Key 和管理密码不得进入日志或数据库明文。
- v2 量产密钥管理是后续最高优先级工作，接入后必须完整删除 Mock 值和分支。

## 首期不包含

MCP、MQTT + UDP、服务端 AEC、固件文件服务、图像能力、管理 Web UI、多租户、RAG、长期记忆、原始音频存储、多 AI 供应商切换、GPU VAD 和 PostgreSQL 运行支持均不在首期范围。

## 完成标准

- v1 当前固件无需修改即可完成真实设备语音对话。
- v1/v2 激活流程和 WebSocket 二进制协议 v1/v2/v3 自动化测试通过。
- MySQL migration、仓储、安全和 10 路并发测试通过。
- `amd64`、`arm64` 镜像能够启动并通过模型与依赖自检。
- 无数据竞争、会话串音、无界资源增长或敏感信息泄露。

详细协议、依赖、数据模型、并发约束和测试矩阵以 [Agents 详细基线](../agents/requirements-and-architecture.md) 为准。
