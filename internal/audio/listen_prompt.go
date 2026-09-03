package audio

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"

	"github.com/hraban/opus"
)

//go:embed prompt.opus
var rawPromptOpusFile []byte

var (
	promptOnce        sync.Once
	promptOpusPackets [][]byte
	promptInitErr     error
)

// initPromptData 在首次调用时解码内嵌的 prompt.opus 文件并自适应生成标准 PCM 与预编码 Opus 数据包。
func initPromptData() {
	promptOnce.Do(func() {
		rawPCM, err := decodeEmbeddedPrompt(rawPromptOpusFile)
		if err != nil {
			promptInitErr = fmt.Errorf("decode embedded prompt opus: %w", err)
			return
		}

		if len(rawPCM) == 0 {
			promptInitErr = errors.New("decoded prompt pcm is empty")
			return
		}

		// 使用标准 PCMFramer 自动按 60ms (2880 字节) 分帧，尾部残余自动补齐
		framer := NewPCMFramer()
		frames := framer.Feed(rawPCM)
		flushFrames := framer.Flush()
		allFrames := append(frames, flushFrames...)

		if len(allFrames) == 0 {
			promptInitErr = errors.New("no valid pcm frames produced from prompt audio")
			return
		}

		enc, err := NewEncoder(DefaultMaxOpusPacketBytes)
		if err != nil {
			promptInitErr = fmt.Errorf("create prompt opus encoder: %w", err)
			return
		}
		defer enc.Close()

		packets := make([][]byte, 0, len(allFrames))

		for i, frame := range allFrames {
			pkt, err := enc.Encode(frame)
			if err != nil {
				promptInitErr = fmt.Errorf("encode prompt frame %d: %w", i, err)
				return
			}
			packets = append(packets, pkt)
		}

		promptOpusPackets = packets
	})
}

// decodeEmbeddedPrompt 从 Ogg Opus 格式数据中提取 raw opus 包并解码为 24 kHz 单声道 16-bit PCM。
func decodeEmbeddedPrompt(oggData []byte) ([]byte, error) {
	if len(oggData) == 0 {
		return nil, errors.New("embedded prompt audio data is empty")
	}

	dec, err := opus.NewDecoder(DownlinkSampleRate, DownlinkChannels)
	if err != nil {
		return nil, fmt.Errorf("initialize opus decoder: %w", err)
	}

	var allPCM []int16
	outBuf := make([]int16, 5760)

	offset := 0
	packetIdx := 0
	for offset < len(oggData) {
		if !bytes.HasPrefix(oggData[offset:], []byte("OggS")) {
			break
		}
		if offset+27 > len(oggData) {
			break
		}
		segments := int(oggData[offset+26])
		segTable := oggData[offset+27 : offset+27+segments]
		bodyOffset := offset + 27 + segments
		bodyLen := 0
		for _, s := range segTable {
			bodyLen += int(s)
		}
		if bodyOffset+bodyLen > len(oggData) {
			break
		}
		body := oggData[bodyOffset : bodyOffset+bodyLen]

		pOffset := 0
		pLen := 0
		for _, s := range segTable {
			pLen += int(s)
			if s < 255 {
				packet := body[pOffset : pOffset+pLen]
				pOffset += pLen
				pLen = 0

				// 忽略前两个包（OpusHead 和 OpusTags）
				if packetIdx >= 2 {
					n, err := dec.Decode(packet, outBuf)
					if err != nil {
						return nil, fmt.Errorf("decode packet %d: %w", packetIdx, err)
					}
					allPCM = append(allPCM, outBuf[:n]...)
				}
				packetIdx++
			}
		}
		offset = bodyOffset + bodyLen
	}

	// 跳过 Opus 标准前导 pre-skip (312 采样点)
	const preSkip = 312
	if len(allPCM) <= preSkip {
		return nil, fmt.Errorf("decoded samples %d less than pre-skip %d", len(allPCM), preSkip)
	}

	trimmedPCM := allPCM[preSkip:]

	// 施加尾部淡出（最后 240 采样点 / 10 ms），消除突变毛刺
	const fadeOutSamples = 240
	if len(trimmedPCM) > fadeOutSamples {
		startFade := len(trimmedPCM) - fadeOutSamples
		for i := 0; i < fadeOutSamples; i++ {
			factor := float64(fadeOutSamples-i) / float64(fadeOutSamples)
			trimmedPCM[startFade+i] = int16(float64(trimmedPCM[startFade+i]) * factor)
		}
	}

	pcmBytes := make([]byte, len(trimmedPCM)*2)
	for i, s := range trimmedPCM {
		binary.LittleEndian.PutUint16(pcmBytes[i*2:i*2+2], uint16(s))
	}

	return pcmBytes, nil
}

// GetListenPromptOpusPackets 返回内嵌提示音自适应分帧预编码的 24 kHz 60 ms Opus 数据包列表。
// 返回值为切片拷贝，确保跨并发安全。
func GetListenPromptOpusPackets() ([][]byte, error) {
	initPromptData()
	if promptInitErr != nil {
		return nil, promptInitErr
	}
	res := make([][]byte, len(promptOpusPackets))
	for i, pkt := range promptOpusPackets {
		copied := make([]byte, len(pkt))
		copy(copied, pkt)
		res[i] = copied
	}
	return res, nil
}
