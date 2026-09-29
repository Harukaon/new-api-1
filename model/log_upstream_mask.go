package model

import (
	"encoding/json"
	"fmt"

	"github.com/QuantumNous/new-api/common"
)

// SparkAI fork: 用户查看自己的错误日志时，来自上游的错误文字同样不给看。
//
// 后台错误日志（type=5）的 content 存的是上游原文，管理员要靠它排查，所以数据库里保持原样；
// 只在用户侧的查询出口（formatUserLogs）把这类日志改写成和接口返回一致的统一 503 文案，
// 并把 other 里公开的 error_type / error_code / status_code 一并换掉（上游的错误码本身
// 也会暴露渠道）。判断依据就是这三项，规则与接口返回完全一致（common.ShouldMaskUpstreamError）。
//
// 没有 error_type 的错误日志（老版本写的）无法判断来源，保持原样。
func maskUpstreamErrorLogForUser(entry *Log) {
	if entry.Type != LogTypeError {
		return
	}
	var fields map[string]json.RawMessage
	if err := common.UnmarshalJsonStr(entry.Other, &fields); err != nil {
		return
	}
	var errType, errCode string
	var status int
	// 缺字段时解析会失败，保持零值即可，下面靠 errType 是否为空来判断有没有资格判断
	_ = common.Unmarshal(fields["error_type"], &errType)
	_ = common.Unmarshal(fields["error_code"], &errCode)
	_ = common.Unmarshal(fields["status_code"], &status)
	if errType == "" || !common.ShouldMaskUpstreamError(errType, errCode, status) {
		return
	}

	for key, value := range map[string]any{
		"error_type":  common.NewAPIErrorTypeName,
		"error_code":  common.UpstreamErrorMaskCode,
		"status_code": common.UpstreamErrorMaskStatus,
	} {
		raw, err := common.Marshal(value)
		if err != nil {
			return
		}
		fields[key] = raw
	}
	other, err := common.Marshal(fields)
	if err != nil {
		return
	}
	entry.Content = fmt.Sprintf("status_code=%d, %s", common.UpstreamErrorMaskStatus, common.UpstreamErrorMaskMessage())
	entry.Other = string(other)
}
