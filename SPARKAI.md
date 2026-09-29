# SparkAI fork 说明

本仓库 fork 自 [QuantumNous/new-api](https://github.com/QuantumNous/new-api)（AGPL-3.0），
为 SparkAI（https://ai.sparkai.si）使用。按 AGPL-3.0 要求公开全部修改。

- 分支：`sparkai`（基于上游 `v1.0.0-rc.40`）
- 镜像：`ghcr.io/harukaon/new-api-1:sparkai-latest`，以及每次提交的 `:1.0.0-rc.40-sparkai-<短提交号>`
  （`.github/workflows/sparkai-image.yml` 自动编译，只做 linux/amd64）

## 改了什么（一）：返回里的模型名改回用户请求的名字

渠道做了模型映射（对外 `deepseek-v4.1` → 上游 `deepseek/deepseek-v4.1`）时，上游返回里的
`model` 是上游名，原版会原样转给用户。本 fork 在写给用户之前把它改回用户请求的名字。

- 只改「写给用户的字节」：计费、使用日志、选渠道、重试全部不受影响（日志里的 `response_model`
  诊断字段仍记录上游真实返回，方便排查）。
- 改写位置：顶层 `model`、顶层 `modelVersion`（Gemini 原生）、`message.model`（Anthropic 流式首段）、
  `response.model`（Responses 及其 WebSocket 事件）。其它内容逐字节不变。
- 覆盖：所有经过 `Distribute` 的 HTTP 模型接口（普通 JSON、SSE 流式），以及 Codex 使用的
  Responses WebSocket（`GET /v1/responses`）。
- 不覆盖：实时语音 `/v1/realtime`（按需求不处理）；Midjourney 接口；
  `GET /v1/responses/:id`（取回后台响应，没有经过选渠道）。

## 改了什么（二）：上游的报错不原样给用户

原版会把上游服务商的报错文字（订阅周限、5-hour limit、重置时间、密钥失效、上游余额不足……）
连同状态码原样交给用户，等于把渠道暴露出去。本 fork 在写给用户之前，把「含有上游内容」的错误
统一换成 **503 + 一句中性文案**（默认「服务暂时不可用，请稍后重试」）。请求 ID 照常附带，
管理员凭它到后台错误日志查看原文。

- 换掉的：上游返回的非 400 错误（429、401、403、5xx，以及 HTTP 200 却带错误体）；连不上上游、
  解析不了上游响应、渠道配置类错误；上游本身是另一个 New API 时它的「余额不足」；
  流式响应开始后上游中途插入的错误事件（状态码已经是 200，只换文字）。
- 照常显示的：New API 自己产生的错误（用户自己的余额不足、分组/模型不可用、请求参数不对、
  敏感词……）；上游返回的 400（多半是用户参数问题，按需求不拦截）。
- 只改「写给用户的内容」：重试判断、渠道自动禁用、后台错误日志用的都还是原始错误，
  同一个请求的上游重试次数与原版一致。
- 用户自己的「使用日志 → 错误」也做同样处理：数据库和管理员视图保留上游原文，
  用户视图里的文字、`error_code`、`status_code` 换成统一值。
- 环境变量（在容器的 `environment` 里配）：
  - `UPSTREAM_ERROR_MASK_ENABLED`：总开关，默认 `true`；设 `false` 立即恢复原版行为
  - `UPSTREAM_ERROR_PASSTHROUGH_STATUS`：上游返回这些状态码时原样显示，逗号分隔，默认 `400`
  - `UPSTREAM_ERROR_MASK_MESSAGE`：统一文案
- 判断规则和理由写在 `common/upstream_error_mask.go` 顶部注释里。
- 不覆盖：Midjourney 和异步任务（视频等）的失败原因；Responses 的 `response.failed` 事件和上游
  WebSocket 事件；非 SSE 的 Gemini 原生流（JSON 数组）。

## 改动文件

新增（升级时不会冲突）：
- `common/response_model_rewrite.go`（+ `_test.go`）：改写函数，按字节区间替换，保留原字段顺序和格式
- `middleware/response_model.go`（+ `_test.go`）：包装 ResponseWriter；JSON 整段缓存后改写并更新
  Content-Length，SSE 按完整行改写（同时把流式中途的上游错误事件换成统一错误），其它类型透传
- `relay/responses_websocket_sparkai.go`（+ `_test.go`）：WebSocket 事件改写
- `common/upstream_error_mask.go`（+ `_test.go`）：哪些错误算「上游的」的判断规则、环境变量、流式错误事件替换
- `service/upstream_error_mask.go`：`ClientFacingError`，生成写给用户的错误（不修改原始错误对象）
- `model/log_upstream_mask.go`：用户查看自己的错误日志时脱敏
- `.github/workflows/sparkai-image.yml`、本文件

改动上游文件（都带 `SparkAI fork` 注释，升级时重点看这几处）：
- `router/relay-router.go`：`playgroundRouter`、`httpRouter`、`relayGeminiRouter` 在 `Distribute()` 后加 `ResponseModelRewrite()`
- `router/task-plugin-protocol-router.go`：`/v1/responses`、图片、视频等插件协议路由在 `Distribute()` 后加
- `router/plugin-router.go`：任务插件路由在 `Distribute()` 后加
- `relay/responses_websocket.go`：会话加 `publicModel` 字段；`runCall` 记下模型名；`writeClient` 和终止事件写出前改写
- `controller/relay.go`：`Relay` 写错误给用户前（2 行）换成 `service.ClientFacingError`
- `relay/channel/gemini/relay-gemini.go`：Gemini 非流式自己直接写错误的地方（2 行）同样换掉
- `model/log.go`：`formatUserLogs` 里加一行调用 `maskUpstreamErrorLogForUser`

测试加在已有的测试文件里：`service/error_test.go`、`model/log_format_test.go`、`middleware/response_model_test.go`。

## 升级上游的步骤

```bash
git fetch upstream --tags          # upstream = https://github.com/QuantumNous/new-api.git
git switch sparkai
git rebase <新版本标签>              # 冲突一般只会出现在上面列的上游文件
# 新版本若新增了经过 Distribute() 的路由，也要在其后加 ResponseModelRewrite()：
grep -rn "Distribute()" router/
# 新版本若新增了「直接把 NewAPIError 写给用户」的地方，也要套上 service.ClientFacingError：
grep -rn "ToOpenAIError()\|ToClaudeError()" --include=*.go . | grep -v _test
go test ./common/ ./middleware/ ./model/ ./service/ ./controller/ ./relay/ ./router/
# 改 .github/workflows/sparkai-image.yml 里的 BASE_VERSION，然后推送（只推分支，不要推标签）
git push --force-with-lease fork sparkai
```
