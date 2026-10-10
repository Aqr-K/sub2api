# #7708 第一批实现记录

日期：2026-10-10。分支：`fix/7708-encrypted-history-errors`。本地目录：`~/project/sub2api`。

这是[设计工作项 1](issue-7708-recovery.md#工作项-1错误分类与完整-sse-失败契约)的第一批实现，**不是 #7708 的完整恢复方案**。基于官方 `3a6fd1c9db07203ca308aaba69e502bc1f35b307` 和设计提交 `dc33516365bd74f24e92e7d7a1a0506f9dd1eb22`；修改源为 Aqr-K/sub2api。

## 已实现行为

- 普通/透传 Responses SSE 和两处 SSE→JSON 转换，识别 `invalid_encrypted_content` 以及符合现有精确信息条件的 `thinking_signature_invalid`。兼容嵌套 `error`、`response.error` 和原生顶层 error 事件；仅作用于 OpenAI 平台。
- 将拒绝传回为独立的 `OpenAIEncryptedHistoryError`，不转成账号池 failover。错误文案包含“try again”、限流或上下文长度关键词时，结构化加密错误码仍优先。
- 保留原始错误对象的 code/message/param；Codex 路径合成 `response.failed` 时也保留错误字段。原生顶层 error 转为 JSON 时移除 SSE 的 type/sequence_number/event_id，并补充 `invalid_request_error` 类别。
- `stream=false` 收到上游 SSE 加密错误时，在尚未提交响应的情况下返回 HTTP 400 JSON，替代通用 502；compact SSE 心跳已提交时继续返回 HTTP 200 SSE 终止事件。
- 从失败事件或此前已读取事件收集的用量，经两条 Forward 路径传回既有 handler 记账入口。失败响应不建立成功 response ID 绑定，也不执行成功路径的配额快照更新。已生成图片的部分结果不再将这类错误归入调度成功。
- 错误不增加调度失败率，不进入账号健康失败路径。typed error 只携带受限错误码，不携带消息正文或密文。
- 既有 Codex reader 暂存的加密错误后出现成功终态时，清除候选错误并保留成功用量；透传的 EOF 合成失败前停止心跳。
- 没有新增重发或密文删除。既有 HTTP 400 reasoning 恢复保持原行为。

## 验证

在 `backend/` 使用 Go 1.27.2。新增测试先在未接入的源码上失败，再在实现后通过。后端完整 unit、下列相关 race 与 PostgreSQL/Redis 集成控制组均通过。

```sh
GOTOOLCHAIN=go1.27.2 GOMAXPROCS=4 go test -p 2 -tags=unit ./...
GOTOOLCHAIN=go1.27.2 GOMAXPROCS=4 go test -p 2 -race -tags=unit ./internal/service ./internal/handler \
  -run 'EncryptedHistory|HTTPThinkingSignature|NonStreaming.*SSEToJSON|Passthrough.*SSEToJSON|StreamingResponse.*Failed|BareError|APIKeyHealth' -count=1
CI=true GOTOOLCHAIN=go1.27.2 GOMAXPROCS=2 go test -p 2 -tags=integration ./internal/repository \
  -run 'TestUsageBillingRepositoryApply|TestUsageLogRepoSuite|TestUsageLogRepositoryCreate_BatchPathDuplicateRequestID|TestBillingCache' -count=1 -v
git diff --check
```

- 服务层 16 个真实 Forward 调用组合：普通/透传 × stream=true/false × 四种加密错误形式；校验只请求一次、工具和 reasoning 历史完整、错误码与用量保留。
- 另测严格分类、非 OpenAI 排除、错误包装、调度统计中立、compact 已提交心跳、Codex 错误后成功终态、已有输出和渐进用量的失败。
- handler 使用 `httptest` HTTP 上游，覆盖四种转发组合。两个可调度账号并开启 pool 模式，仍只请求一次、只提交一条 19 input / 3 output 的用量记录。该测试使用 Simple 模式和内存 repository stub，不能当作真实扣费实验。
- PostgreSQL/Redis 既有集成控制组验证余额/订阅扣费去重、用量持久化去重和账单缓存；使用 `CI=true`，测试确实启动隔离容器，没有以无 Docker 跳过代替通过。此组与新增 handler 测试分开运行。
- 全量 `golangci-lint run --timeout=5m ./...` 通过，使用临时目录下、由 Go 1.27.2 编译的 golangci-lint v2.14.0。机器原有 v2.12.2 及其旧版导出数据解析器不兼容项目的 Go 版本，因此没有降低 go.mod；也没有修改仓库的 lint 配置。

## 剩余边界与下一步

1. **工作项 1 仍未完成。** 本批没有统一普通 API Key 与 Codex 的错误终态生命周期。普通 API Key reader 仍可能在裸 error 后结束，尚不能保证收集随后到达的 `response.failed` 用量；Codex/透传 reader 的有界尾部读取、取消、EOF 和超时需要单独实现和验证。
2. 本批未改造透传的失败尝试 headers 暂存，不能声称已经消除心跳前泄漏账号相关响应头的问题。完整生命周期的碎片 JSON、多行 data、取消、JSON 空白 keepalive、槽位释放和真实结算端到端矩阵仍待补齐。
3. 没有新增 KnownZero/Unknown 计量模型。现有 `OpenAIUsage` 数值为零不证明上游没有消耗，绝不能用它开启自动重放。
4. HTTP 400 原有恢复未扩大到工具消息；HTTP 非 SSE、WebSocket、Chat/Anthropic 桥、producer 来源路由和明文协作选项不在本批新增行为内。
5. 尚无 #7708 现场上游、真实客户端和失效密文的复验。修复了网关对该错误的误分类、错误信息丢失及已知用量丢失；**没有证明首次密文失效的原因，也没有恢复旧工具密文**。

下一批先补齐工作项 1 的生命周期与 headers 契约；工作项 2–6 继续遵守原设计的证据、预算和默认关闭要求。整个实现过程未使用 subagent。
