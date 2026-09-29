package common

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// SparkAI fork: 上游返回的错误不原样给用户。
//
// 原版会把上游服务商的报错文字（订阅周限、5-hour limit、重置时间、账号失效、
// 上游余额不足……）连同状态码原样交给用户，等于把渠道暴露出去，用户还会拿这些
// 话来找我们。这里规定哪些错误可以让用户看到原文，其余统一换成 503 + 一句中性文案。
//
// 只改「写给用户的内容」：重试判断、渠道自动禁用、后台错误日志用的都还是原始错误。
//
// 可以原样显示的（不脱敏）：
//   - New API 自己产生的错误：余额不足、分组/模型不可用、请求参数不对……
//     （错误类型是 new_api_error，且错误码不在下面的「上游侧」名单里）
//   - 上游返回的 400：多半是用户参数有问题，按需求不拦截
//     （放行的状态码可用 UPSTREAM_ERROR_PASSTHROUGH_STATUS 调整）
//
// 环境变量：
//   - UPSTREAM_ERROR_MASK_ENABLED      总开关，默认 true；关掉即恢复原版行为
//   - UPSTREAM_ERROR_PASSTHROUGH_STATUS 上游返回这些状态码时原样显示，逗号分隔，默认 400
//   - UPSTREAM_ERROR_MASK_MESSAGE      统一文案，默认「服务暂时不可用，请稍后重试」

const (
	// UpstreamErrorMaskStatus / UpstreamErrorMaskCode 是用户看到的统一状态码和错误码。
	UpstreamErrorMaskStatus = http.StatusServiceUnavailable
	UpstreamErrorMaskCode   = "service_unavailable"

	upstreamErrorMaskDefaultMessage = "服务暂时不可用，请稍后重试"

	// NewAPIErrorTypeName 是 New API 自己产生的错误的类型名，脱敏后的错误也用它。
	// 与 relaykit/types.ErrorTypeNewAPIError 的取值一致（common 不依赖 relaykit，
	// 由本包的测试断言两者没有漂移）。
	NewAPIErrorTypeName = "new_api_error"
)

// 这些错误码说明问题出在「传输 / 解析上游响应 / 渠道能力」上。错误对象虽然是 New API 自己
// 构造的（类型是 new_api_error），状态码也是它自己定的，但文字里常带着上游的响应内容或渠道
// 细节，所以不看状态码，一律隐藏。带 "channel:" 前缀的渠道配置类错误同理。
// bad_response_status_code 不在这里：它代表「上游回了非 200」，要按状态码决定是否放行。
var upstreamSideErrorCodes = []string{
	"do_request_failed",
	"bad_response",
	"bad_response_body",
	"read_response_body_failed",
	"empty_response",
	"aws_invoke_error",
	"invalid_api_type",
}

// UpstreamErrorMaskEnabled 返回上游错误脱敏总开关（默认开）。
func UpstreamErrorMaskEnabled() bool {
	return GetEnvOrDefaultBool("UPSTREAM_ERROR_MASK_ENABLED", true)
}

// UpstreamErrorMaskMessage 返回用户看到的统一文案。
func UpstreamErrorMaskMessage() string {
	return GetEnvOrDefaultString("UPSTREAM_ERROR_MASK_MESSAGE", upstreamErrorMaskDefaultMessage)
}

// ShouldMaskUpstreamError 判断一个错误是否含有上游的内容、不能原样给用户看。
// 参数取错误对象里的 errorType / errorCode / 状态码；后台日志里存着同样三项，
// 所以用户查看自己的错误日志时也用它。
func ShouldMaskUpstreamError(errType, errCode string, status int) bool {
	if !UpstreamErrorMaskEnabled() {
		return false
	}
	if strings.HasPrefix(errCode, "channel:") || slices.Contains(upstreamSideErrorCodes, errCode) {
		return true
	}
	// 走到这里只剩两类：New API 自己的错（放行），以及上游服务商自己回复的错。
	if errType == NewAPIErrorTypeName && errCode != "bad_response_status_code" {
		return false
	}
	for part := range strings.SplitSeq(GetEnvOrDefaultString("UPSTREAM_ERROR_PASSTHROUGH_STATUS", "400"), ",") {
		if passthrough, err := strconv.Atoi(strings.TrimSpace(part)); err == nil && passthrough == status {
			return false
		}
	}
	return true
}
