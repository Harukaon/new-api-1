# SparkAI fork 说明

本仓库 fork 自 [QuantumNous/new-api](https://github.com/QuantumNous/new-api)（AGPL-3.0），
为 SparkAI（https://ai.sparkai.si）使用。按 AGPL-3.0 要求公开全部修改。

- 分支：`sparkai`（基于上游 `v1.0.0-rc.40`）
- 镜像：`ghcr.io/harukaon/new-api-1:sparkai-latest`，以及每次提交的 `:1.0.0-rc.40-sparkai-<短提交号>`
  （`.github/workflows/sparkai-image.yml` 自动编译，只做 linux/amd64）

## 改了什么：返回里的模型名改回用户请求的名字

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

## 改动文件

新增（升级时不会冲突）：
- `common/response_model_rewrite.go`（+ `_test.go`）：改写函数，按字节区间替换，保留原字段顺序和格式
- `middleware/response_model.go`（+ `_test.go`）：包装 ResponseWriter；JSON 整段缓存后改写并更新
  Content-Length，SSE 按完整行改写，其它类型透传
- `relay/responses_websocket_sparkai.go`（+ `_test.go`）：WebSocket 事件改写
- `.github/workflows/sparkai-image.yml`、本文件

改动上游文件（都带 `SparkAI fork` 注释，升级时重点看这几处）：
- `router/relay-router.go`：`playgroundRouter`、`httpRouter`、`relayGeminiRouter` 在 `Distribute()` 后加 `ResponseModelRewrite()`
- `router/task-plugin-protocol-router.go`：`/v1/responses`、图片、视频等插件协议路由在 `Distribute()` 后加
- `router/plugin-router.go`：任务插件路由在 `Distribute()` 后加
- `relay/responses_websocket.go`：会话加 `publicModel` 字段；`runCall` 记下模型名；`writeClient` 和终止事件写出前改写

## 升级上游的步骤

```bash
git fetch upstream --tags          # upstream = https://github.com/QuantumNous/new-api.git
git switch sparkai
git rebase <新版本标签>              # 冲突一般只会出现在上面 4 个上游文件
# 新版本若新增了经过 Distribute() 的路由，也要在其后加 ResponseModelRewrite()：
grep -rn "Distribute()" router/
go test ./common/ ./middleware/ ./relay/ ./router/
# 改 .github/workflows/sparkai-image.yml 里的 BASE_VERSION，然后推送（只推分支，不要推标签）
git push --force-with-lease fork sparkai
```
