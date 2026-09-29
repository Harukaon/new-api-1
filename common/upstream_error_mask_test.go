package common

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShouldMaskUpstreamError(t *testing.T) {
	// 每一行是一种真实会遇到的错误，errType / errCode / status 取自后台错误日志里的同名字段。
	cases := []struct {
		name    string
		errType string
		errCode string
		status  int
		want    bool
	}{
		// New API 自己产生的错误：照常给用户看
		{"insufficient balance", "new_api_error", "insufficient_user_quota", 403, false},
		{"token quota pre-consume failed", "new_api_error", "pre_consume_token_quota_failed", 403, false},
		{"no channel for group/model", "new_api_error", "get_channel_failed", 503, false},
		{"bad request parameters", "new_api_error", "invalid_request", 400, false},
		{"local error with default 500", "new_api_error", "invalid_request", 500, false},
		{"request conversion failed", "new_api_error", "convert_request_failed", 500, false},
		{"sensitive words", "new_api_error", "sensitive_words_detected", 400, false},

		// 上游服务商回复的错误：统一隐藏（订阅周限就是这一类）
		{"subscription weekly limit", "openai_error", "rate_limited", 429, true},
		{"upstream error without code", "openai_error", "unknown_error", 429, true},
		{"upstream key invalid", "openai_error", "bad_response_status_code", 401, true},
		{"upstream forbidden", "openai_error", "bad_response_status_code", 403, true},
		{"upstream 500", "openai_error", "bad_response_status_code", 500, true},
		{"upstream 503", "openai_error", "unknown_error", 503, true},
		{"claude style overloaded", "claude_error", "overloaded_error", 529, true},
		{"gemini style error", "gemini_error", "RESOURCE_EXHAUSTED", 429, true},
		{"error body returned with HTTP 200", "openai_error", "insufficient_quota", 200, true},
		// 上游本身是另一个 New API 时，它自己的余额不足对我们的用户来说也是上游错误
		{"upstream New API balance error", "openai_error", "insufficient_user_quota", 403, true},
		// 本地构造但代表「上游回了非 200」
		{"bad_response_status_code typed locally, 429", "new_api_error", "bad_response_status_code", 429, true},

		// 上游返回 400：按需求不拦截
		{"upstream 400", "openai_error", "invalid_request_error", 400, false},
		{"upstream 400 typed locally", "new_api_error", "bad_response_status_code", 400, false},

		// 传输 / 渠道配置 / 解析上游响应失败：不看状态码，一律隐藏
		{"request to upstream failed", "new_api_error", "do_request_failed", 500, true},
		{"request to upstream failed with 400", "openai_error", "do_request_failed", 400, true},
		{"channel has no key", "new_api_error", "channel:no_available_key", 500, true},
		{"channel model mapping broken with 400", "new_api_error", "channel:model_mapped_error", 400, true},
		{"unparsable upstream body", "new_api_error", "bad_response_body", 500, true},
		{"empty upstream response", "openai_error", "empty_response", 500, true},
		{"channel type lacks capability", "new_api_error", "invalid_api_type", 500, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ShouldMaskUpstreamError(tc.errType, tc.errCode, tc.status))
		})
	}
}

// 流式响应开始后上游才插入的错误事件：状态码改不了，只能把整行换成统一错误；其它行一字节不动。
func TestMaskUpstreamErrorSSELine(t *testing.T) {
	weekly := "You've reached your weekly usage limit. Your limit resets at 2026-10-05T00:00:00Z."

	masked := map[string]string{
		"openai style error object":   `data: {"error":{"message":"` + weekly + `","type":"rate_limit_error","code":"rate_limited"}}`,
		"error given as plain string": `data: {"error":"` + weekly + `"}`,
		"anthropic style error event": `data: {"type":"error","error":{"type":"overloaded_error","message":"` + weekly + `"}}`,
		"responses style error event": `data: {"type":"error","code":"rate_limit_exceeded","message":"` + weekly + `","param":null}`,
		"no space after data colon":   `data:{"error":{"message":"` + weekly + `"}}`,
	}
	for name, line := range masked {
		t.Run(name, func(t *testing.T) {
			out, changed := MaskUpstreamErrorSSELine([]byte(line))

			require.True(t, changed)
			assert.NotContains(t, string(out), "weekly")
			assert.NotContains(t, string(out), "rate_limit")
			var event struct {
				Type    string `json:"type"`
				Message string `json:"message"`
				Error   struct {
					Message string `json:"message"`
					Code    string `json:"code"`
				} `json:"error"`
			}
			require.True(t, strings.HasPrefix(string(out), "data: "))
			require.NoError(t, UnmarshalJsonStr(strings.TrimPrefix(string(out), "data: "), &event))
			assert.Equal(t, UpstreamErrorMaskMessage(), event.Error.Message)
			assert.Equal(t, UpstreamErrorMaskCode, event.Error.Code)
			if strings.Contains(line, `"type":"error"`) {
				assert.Equal(t, "error", event.Type)
				assert.Equal(t, UpstreamErrorMaskMessage(), event.Message)
			}
		})
	}

	unchanged := map[string]string{
		"normal chunk":                   `data: {"id":"1","choices":[{"delta":{"content":"hello"}}]}`,
		"model text that mentions error": `data: {"choices":[{"delta":{"content":"an \"error\" occurred"}}]}`,
		"null error field":               `data: {"error":null,"choices":[]}`,
		"nested error field only":        `data: {"usage":{"error":"x"},"choices":[]}`,
		"done marker":                    `data: [DONE]`,
		"payload that is not json":       `data: not json "error"`,
		"comment line":                   `: PING`,
		"event name line":                `event: error`,
		"empty line":                     ``,
	}
	for name, line := range unchanged {
		t.Run(name, func(t *testing.T) {
			out, changed := MaskUpstreamErrorSSELine([]byte(line))

			assert.False(t, changed)
			assert.Equal(t, line, string(out))
		})
	}

	t.Run("kill switch", func(t *testing.T) {
		t.Setenv("UPSTREAM_ERROR_MASK_ENABLED", "false")
		out, changed := MaskUpstreamErrorSSELine([]byte(masked["openai style error object"]))

		assert.False(t, changed)
		assert.Equal(t, masked["openai style error object"], string(out))
	})
}

func TestUpstreamErrorMaskSettings(t *testing.T) {
	// common 不依赖 relaykit，类型名靠这条断言保证没有漂移
	assert.Equal(t, string(types.ErrorTypeNewAPIError), NewAPIErrorTypeName)

	assert.True(t, UpstreamErrorMaskEnabled())
	assert.Equal(t, "服务暂时不可用，请稍后重试", UpstreamErrorMaskMessage())

	t.Run("passthrough status list", func(t *testing.T) {
		t.Setenv("UPSTREAM_ERROR_PASSTHROUGH_STATUS", "400, 422")
		assert.False(t, ShouldMaskUpstreamError("openai_error", "x", 400))
		assert.False(t, ShouldMaskUpstreamError("openai_error", "x", 422))
		assert.True(t, ShouldMaskUpstreamError("openai_error", "x", 429))
	})
	t.Run("custom message", func(t *testing.T) {
		t.Setenv("UPSTREAM_ERROR_MASK_MESSAGE", "please retry later")
		assert.Equal(t, "please retry later", UpstreamErrorMaskMessage())
	})
	t.Run("kill switch restores stock behavior", func(t *testing.T) {
		t.Setenv("UPSTREAM_ERROR_MASK_ENABLED", "false")
		assert.False(t, UpstreamErrorMaskEnabled())
		assert.False(t, ShouldMaskUpstreamError("openai_error", "rate_limited", 429))
	})
}
