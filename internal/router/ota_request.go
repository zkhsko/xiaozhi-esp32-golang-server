package router

import (
	"errors"
	"net/http"
	"strings"
)

const (
	// DefaultMaxBodyBytes 默认请求体上限（64 KiB）。
	DefaultMaxBodyBytes = 64 * 1024

	// MaxSingleHeaderBytes 单个请求头键或值的最大长度（1024 字符）。
	MaxSingleHeaderBytes = 1024

	// MaxTotalHeaderBytes 所有请求头键值对累计最大长度（8192 字符）。
	MaxTotalHeaderBytes = 8192

	// OTAPath 配置发现的固定路由路径。
	OTAPath = "/xiaozhi/ota/"
)

// DeviceHeaders 提取自 OTA 请求头的公共设备标识字段。
type DeviceHeaders struct {
	DeviceID          string
	ClientID          string
	SerialNumber      string
	ActivationVersion string
	UserAgent         string
	AcceptLanguage    string
}

// extractDeviceHeaders 从 HTTP 请求头中提取并修剪设备相关字段。
func extractDeviceHeaders(r *http.Request) DeviceHeaders {
	return DeviceHeaders{
		DeviceID:          strings.TrimSpace(r.Header.Get("Device-Id")),
		ClientID:          strings.TrimSpace(r.Header.Get("Client-Id")),
		SerialNumber:      strings.TrimSpace(r.Header.Get("Serial-Number")),
		ActivationVersion: strings.TrimSpace(r.Header.Get("Activation-Version")),
		UserAgent:         strings.TrimSpace(r.UserAgent()),
		AcceptLanguage:    strings.TrimSpace(r.Header.Get("Accept-Language")),
	}
}

// validateHeaders 校验请求头键值是否超出单项与总计长度上限。
func validateHeaders(headers http.Header, maxSingle, maxTotal int) error {
	totalLen := 0
	for key, values := range headers {
		if len(key) > maxSingle {
			return errors.New("header key exceeds limit")
		}
		totalLen += len(key)
		for _, val := range values {
			if len(val) > maxSingle {
				return errors.New("header value exceeds limit")
			}
			totalLen += len(val)
			if totalLen > maxTotal {
				return errors.New("total headers size exceeds limit")
			}
		}
	}
	return nil
}
