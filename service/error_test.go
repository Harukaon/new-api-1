package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResetStatusCode(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name             string
		statusCode       int
		statusCodeConfig string
		expectedCode     int
	}{
		{
			name:             "map string value",
			statusCode:       429,
			statusCodeConfig: `{"429":"503"}`,
			expectedCode:     503,
		},
		{
			name:             "map int value",
			statusCode:       429,
			statusCodeConfig: `{"429":503}`,
			expectedCode:     503,
		},
		{
			name:             "skip invalid string value",
			statusCode:       429,
			statusCodeConfig: `{"429":"bad-code"}`,
			expectedCode:     429,
		},
		{
			name:             "skip status code 200",
			statusCode:       200,
			statusCodeConfig: `{"200":503}`,
			expectedCode:     200,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			newAPIError := &types.NewAPIError{
				StatusCode: tc.statusCode,
			}
			ResetStatusCode(newAPIError, tc.statusCodeConfig)
			require.Equal(t, tc.expectedCode, newAPIError.StatusCode)
		})
	}
}

func TestRelayErrorHandlerTruncatesInvalidJSONBodyInLog(t *testing.T) {
	withDebugEnabled(t, false)

	body := strings.Repeat("b", common.LocalLogContentLimit+256)
	var logBuffer bytes.Buffer

	common.LogWriterMu.Lock()
	oldWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &logBuffer
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultErrorWriter = oldWriter
		common.LogWriterMu.Unlock()
	})

	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	newAPIError := RelayErrorHandler(context.Background(), resp, false)

	require.NotNil(t, newAPIError)
	require.Equal(t, "bad response status code 500", newAPIError.Error())
	require.Contains(t, logBuffer.String(), "[truncated")
	require.Contains(t, logBuffer.String(), fmt.Sprintf("original_length=%d", len(body)))
	require.NotContains(t, logBuffer.String(), strings.Repeat("b", common.LocalLogContentLimit+1))
}

func TestRelayErrorHandlerKeepsStructuredErrorMessage(t *testing.T) {
	message := strings.Repeat("c", common.LocalLogContentLimit+256)
	body := `{"message":"` + message + `"}`
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	newAPIError := RelayErrorHandler(context.Background(), resp, false)

	require.NotNil(t, newAPIError)
	require.Equal(t, message, newAPIError.Error())
}

func TestRelayErrorHandlerKeepsOpenAIErrorMessage(t *testing.T) {
	message := strings.Repeat("d", common.LocalLogContentLimit+256)
	body := `{"error":{"message":"` + message + `","type":"server_error","code":"server_error"}}`
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	newAPIError := RelayErrorHandler(context.Background(), resp, false)

	require.NotNil(t, newAPIError)
	require.Equal(t, message, newAPIError.Error())
}

// 上游返回的错误文字（订阅周限、重置时间……）不能原样给用户，而 New API 自己产生的错误要照常显示。
// 上游错误用真实的 RelayErrorHandler 从响应体构造，避免测试里手写的错误对象和真实代码脱节。
func TestClientFacingErrorHidesUpstreamErrors(t *testing.T) {
	fromUpstream := func(status int, body string) *types.NewAPIError {
		return RelayErrorHandler(context.Background(), &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(body)),
		}, false)
	}
	weeklyLimit := `{"error":{"message":"You've reached your weekly usage limit. Your limit resets at 2026-10-05T00:00:00Z.","type":"rate_limit_error","code":"rate_limited"}}`

	t.Run("upstream 429 with weekly limit text", func(t *testing.T) {
		original := fromUpstream(http.StatusTooManyRequests, weeklyLimit)
		require.Contains(t, original.Error(), "weekly usage limit")

		shown := ClientFacingError(original)

		require.NotSame(t, original, shown)
		assert.Equal(t, http.StatusServiceUnavailable, shown.StatusCode)
		openAIErr := shown.ToOpenAIError()
		assert.Equal(t, common.UpstreamErrorMaskMessage(), openAIErr.Message)
		assert.Equal(t, types.ErrorCode(common.UpstreamErrorMaskCode), openAIErr.Code)
		assert.Equal(t, common.UpstreamErrorMaskMessage(), shown.ToClaudeError().Message)
		// 原始错误对象不能被改：重试判断、渠道禁用、后台日志都靠它
		assert.Equal(t, http.StatusTooManyRequests, original.StatusCode)
		assert.Contains(t, original.Error(), "weekly usage limit")
	})

	t.Run("request id is appended once to the generic message", func(t *testing.T) {
		shown := ClientFacingError(fromUpstream(http.StatusTooManyRequests, weeklyLimit))
		shown.SetMessage(common.MessageWithRequestId(shown.Error(), "req-1"))

		message := shown.ToOpenAIError().Message
		assert.Contains(t, message, common.UpstreamErrorMaskMessage())
		assert.Equal(t, 1, strings.Count(message, "req-1"))
		assert.NotContains(t, message, "weekly")
	})

	t.Run("upstream errors that are not JSON or carry other statuses", func(t *testing.T) {
		for _, status := range []int{http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusNotFound, http.StatusBadGateway} {
			shown := ClientFacingError(fromUpstream(status, "<html>quota exhausted for key sk-secret</html>"))
			assert.Equal(t, http.StatusServiceUnavailable, shown.StatusCode, "upstream status %d", status)
			assert.Equal(t, common.UpstreamErrorMaskMessage(), shown.Error())
		}
	})

	t.Run("upstream New API reporting its own balance problem", func(t *testing.T) {
		body := `{"error":{"message":"用户额度不足, 剩余额度: $0.00","type":"new_api_error","code":"insufficient_user_quota"}}`
		shown := ClientFacingError(fromUpstream(http.StatusForbidden, body))
		assert.Equal(t, http.StatusServiceUnavailable, shown.StatusCode)
		assert.Equal(t, common.UpstreamErrorMaskMessage(), shown.Error())
	})

	t.Run("upstream 400 is shown as is", func(t *testing.T) {
		original := fromUpstream(http.StatusBadRequest, `{"error":{"message":"unsupported parameter: foo","type":"invalid_request_error"}}`)
		assert.Same(t, original, ClientFacingError(original))
	})

	t.Run("failures reaching the upstream or broken channel config", func(t *testing.T) {
		transport := types.NewOpenAIError(errors.New(`Post "https://upstream.example/v1/chat/completions": dial tcp: connection refused`), types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)
		channel := types.NewError(errors.New("no available key"), types.ErrorCodeChannelNoAvailableKey)
		for _, original := range []*types.NewAPIError{transport, channel} {
			shown := ClientFacingError(original)
			assert.Equal(t, http.StatusServiceUnavailable, shown.StatusCode, string(original.GetErrorCode()))
			assert.Equal(t, common.UpstreamErrorMaskMessage(), shown.Error())
		}
	})

	t.Run("errors produced by New API itself are untouched", func(t *testing.T) {
		balance := types.NewErrorWithStatusCode(errors.New("用户额度不足, 剩余额度: $0.00"), types.ErrorCodeInsufficientUserQuota, http.StatusForbidden)
		group := types.NewError(errors.New("no available channel for model in group"), types.ErrorCodeGetChannelFailed)
		params := types.NewError(errors.New("invalid request: messages is required"), types.ErrorCodeInvalidRequest, types.ErrOptionWithStatusCode(http.StatusBadRequest))
		for _, original := range []*types.NewAPIError{balance, group, params} {
			assert.Same(t, original, ClientFacingError(original), string(original.GetErrorCode()))
		}
		assert.Nil(t, ClientFacingError(nil))
	})

	t.Run("kill switch", func(t *testing.T) {
		t.Setenv("UPSTREAM_ERROR_MASK_ENABLED", "false")
		original := fromUpstream(http.StatusTooManyRequests, weeklyLimit)
		assert.Same(t, original, ClientFacingError(original))
	})
}

func TestRelayErrorHandlerKeepsInvalidJSONBodyInDebugLog(t *testing.T) {
	withDebugEnabled(t, true)

	body := strings.Repeat("e", common.LocalLogContentLimit+256)
	var logBuffer bytes.Buffer

	common.LogWriterMu.Lock()
	oldWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &logBuffer
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultErrorWriter = oldWriter
		common.LogWriterMu.Unlock()
	})

	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	newAPIError := RelayErrorHandler(context.Background(), resp, false)

	require.NotNil(t, newAPIError)
	require.NotContains(t, logBuffer.String(), "[truncated")
	require.Contains(t, logBuffer.String(), body)
}

func withDebugEnabled(t *testing.T, enabled bool) {
	t.Helper()

	oldDebug := common.DebugEnabled
	common.DebugEnabled = enabled
	t.Cleanup(func() {
		common.DebugEnabled = oldDebug
	})
}
