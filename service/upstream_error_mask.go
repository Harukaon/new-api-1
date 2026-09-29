package service

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
)

// SparkAI fork: ClientFacingError 返回应当写给用户的那个错误。
//
// 错误里含有上游的内容（判断规则见 common.ShouldMaskUpstreamError）时，返回一个全新的
// 503 + 统一文案；否则原样返回 err。原始错误对象不会被修改，所以重试判断、渠道自动禁用
// 和后台错误日志用的仍是真实原因。
//
// 不能用 err.SetMessage 就地改：上游类型的错误（openai_error 等）在输出时读的是
// RelayError 里的原文，改 Err 挡不住，还会把日志也改掉。
func ClientFacingError(err *types.NewAPIError) *types.NewAPIError {
	if err == nil || !common.ShouldMaskUpstreamError(string(err.GetErrorType()), string(err.GetErrorCode()), err.StatusCode) {
		return err
	}
	return types.NewErrorWithStatusCode(errors.New(common.UpstreamErrorMaskMessage()), common.UpstreamErrorMaskCode, common.UpstreamErrorMaskStatus)
}
