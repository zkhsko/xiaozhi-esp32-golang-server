# 语音轮次终极流式架构设计

## 1. 架构总览

本架构为 `xiaozhi-esp32-golang-server` 的语音链路终极架构，采用 Actor 模型与单向流式并发管道，将语音交互的全链路串联：

```text
客户端 Opus
    │
    ▼
Turn 级 Opus Decoder (16 kHz)
    │ PCM 16 kHz / mono / 16-bit
    ▼
ASR 阶段 (auto VAD 或 manual finish)
    │ final text
    ├──────────────> STT 下行
    ▼
LLM 流式生成与工具调用
    │ text chunks
    ▼
增量分句 (SentenceSplitter)
    │ sentences
    ▼
单 Turn、单 TTS Session、逐句串行合成
    │ PCM 24 kHz / mono / 16-bit
    ▼
单 Turn 级连续 Opus Encoder (24 kHz)
    │ 60 ms Opus frames
    ▼
60 ms 节拍转发 (PaceForward)
    │
    ▼
TurnOutput
    │
    ▼
Outbound Actor
    │
    ▼
WebSocket 底层连接
```

---

## 2. 模块职责与依赖边界

### 2.1 依赖拓扑

```text
internal/session (Session Actor, Outbound Actor)
      │
      ▼
internal/voice (TurnEngine, ASRStage, ResponseStage, EncoderStage, PaceForward, Splitter)
   ├──> internal/ai (ASRClient, LLMClient, TTSClient)
   └──> internal/audio (Opus Decoder, Opus StreamEncoder)
```

- `internal/voice` 为独立的语音轮次编排包，不反向导入 `internal/session`。
- Session 与 TurnEngine 之间不共享任何可变业务状态，通过 Context、Channel 和不可变快照协作。
- 零互斥锁跨层共享：编排层不使用共享锁，并发所有权由 Actor 与 Goroutine 独占。

---

## 3. Session Actor 设计

### 3.1 四态状态机

Session 只保留四个权威状态：

```text
AwaitHello ──> Ready ──> TurnActive ──> Ready
                           │
                           ▼
                         Closed
```

1. **StateAwaitHello**：连接已升级，等待客户端握手首包 `hello`；
2. **StateReady**：握手完成或前一轮次处理完毕，等待收音；
3. **StateTurnActive**：单轮问答处于活跃状态（涵盖收音、ASR、LLM 生成、工具调用或回答播报）；
4. **StateClosed**：连接关闭或会话终结。

### 3.2 Actor 独占状态

Session Actor 单协程独占以下状态：
- 当前 Session 状态机与 `sessionId`；
- 当前 `turnId`；
- 当前 Turn 取消根 `turnCancel`；
- 当前输入流通道 `turnInputCh` 与输入闭环标记 `turnInputClosed`；
- 暂存的下一轮 `pendingTurn`（包含模式、手动结束标记及有界 Opus 预缓冲）；
- 独占对话历史 `history`（非并发安全，启动轮次传只读切片快照，轮次完整交付后追加）；
- 定时器：握手超时定时器与单轮最大收音超时定时器。

### 3.3 轮次生命周期与暂存机制

- **Auto 模式**：收到 `listen.start` 立即启动 Turn 开始收音，由 ASR 云端/服务端 VAD 产出最终文本并闭环输入。
- **Manual 模式**：收到 `listen.start` 启动收音，收到 `listen.stop` 时关闭当前 Turn 输入流并排空已入队音频，获取最终结果。
- **打断 (Abort)**：
  1. 取消当前 Turn Context；
  2. 通知 Outbound Actor 精准失效当前 `turnId` 未开始写入的消息；
  3. 等待当前 Turn 完整终结退出；
  4. 回到 Ready 或启动暂存的新轮次。
- **下一轮暂存 (Pending Turn)**：在当前轮输出播报阶段到达的 `listen.start` 和上行音频，作为 `pendingTurn` 暂存（预缓冲有界，超出容量关闭会话）；旧轮次完整交付或打断退出后，自动提取并启动新轮次。

---

## 4. TurnEngine 与流式管道设计

### 4.1 TurnEngine 契约

```go
func (e *TurnEngine) HandleTurn(
    ctx context.Context,
    req TurnRequest,
    input AudioStream,
    output TurnOutput,
) TurnResult
```

- 同步阻塞执行，返回前通过 `errgroup` 确保创建的所有 Worker Goroutine 完整退出，不泄漏后台协程。
- 单一终局收口：通过类型化 `TurnResult` 返回终态（`TurnCompleted`、`TurnAborted`、`TurnFailed`、`TurnNoSpeech`）。

### 4.2 流式阶段细节

1. **ASR 阶段**：
   - 单轮独立 16 kHz 解码器，边解码边向 ASR 客户端投递 PCM；
   - ASR 产出最终文本后通知输入闭环，解除解码器等待；
   - manual 空文本识别直接返回 `TurnNoSpeech`，不进入后续阶段。
2. **LLM 与分句阶段**：
   - 接收 LLM 流式文本 chunk，喂入增量分句器 `SentenceSplitter`（中英文标点断句、最少 5 字、最多 80 字强制切分）；
   - 工具迭代切换与流结束时刷新分句残余，避免丢字；
   - finalText 兜底保证无流式 chunk 时仍能产出回复。
3. **TTS 阶段**：
   - 单 Turn 懒创建且仅创建 1 个 `TTSSession` 长连接；
   - 多个句子在该长连接上严格串行合成；
   - 单句带超时控制，非空句子零 PCM 视为异常。
4. **连续 Opus 编码阶段**：
   - 单 Turn 级连续 24 kHz `StreamEncoder`；
   - 跨句 PCM 连续拼接，不补零，句首字幕标记绑定首个包含该句 PCM 的 Opus 包；
   - 仅在整个回答的 PCM 流结束时 Flush 尾包。
5. **60 ms 节拍转发**：
   - 首帧立即下发；
   - 后续帧使用单次 Timer 保持单调 60 ms 间隔；
   - 网络写入耗时计入间隔，落后时不突发追赶。

---

## 5. Outbound Actor 与下行写入设计

### 5.1 唯一写入者与消息作用域

只有 Outbound Actor 协程独占调用底层 `conn.Write`：
- **Session 作用域**（`turnId = 0`）：Hello 响应、MCP 发现等会话级消息；
- **Turn 作用域**（`turnId > 0`）：STT、TTS start/sentence/audio/stop 等轮次级消息。

### 5.2 原子 Batch 与 TTS 协议状态

首帧通过不可分割的内部 Batch 原语原子写出：
- `tts.start` + `sentence_start` (一个或多个) + 首包 Opus 二进制帧；
- 后续帧：`sentence_start` (若有) + Opus 二进制帧。

TTS 协议生命周期由单轮 `TurnOutput` 唯一管理：
- `tts.start` 未真实写出时，`End()` 绝对不发送 `tts.stop`；
- `tts.start` 已真实写出时，`End()` 无论正常完成、Abort 还是错误，均严格且仅补发一次 `tts.stop`（使用独立的超时 Context）。

### 5.3 精准失效

当发生打断或轮次切换时，调用 `InvalidateTurn(turnId)`：
- 队列中已积压的该 `turnId` 待写入项直接丢弃，等待者立即唤醒返回 `ErrTurnAborted`；
- 允许底层正在执行的一次 WebSocket Write 完成；
- Session 作用域消息不受影响。
