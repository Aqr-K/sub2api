# #7708 源码证据摘要

取证时间：2026-10-09。官方基线 `3a6fd1c9db07203ca308aaba69e502bc1f35b307`；报告部署 `5de5e2bed035d43591a2e10e51f420ef6a84eb98`。

## 官方与相关记录

- [官方 #7708](https://github.com/Wei-Shaw/sub2api/issues/7708)：HTTP 200 SSE 加密工具结果错误；尚未确定坏项与首次失效原因。
- [官方 #7703](https://github.com/Wei-Shaw/sub2api/pull/7703)：2026-10-08 合入，修复 HTTP 400 thinking_signature_invalid 的 reasoning 恢复。
- [APIs-Gateway #291 对 #7708 的审计](https://github.com/APIs-Gateway/sub2api/issues/291#issuecomment-5916959848)：分类 C/暂缓，明确未实现安全的工具历史恢复。
- [官方 #7506](https://github.com/Wei-Shaw/sub2api/issues/7506)：相同加密协作消息在原生成后端成功，其他被测后端失败。不能直接当成 #7708 的现场复现。
- [官方 #7520](https://github.com/Wei-Shaw/sub2api/pull/7520)：明文协作实验选项提案，关闭未合并。假上游 canary 不证明真实模型所有调用路径已成功。

## 源码链接

- [HTTP 400 恢复入口](https://github.com/Wei-Shaw/sub2api/blob/3a6fd1c9db07203ca308aaba69e502bc1f35b307/backend/internal/service/openai_gateway_forward.go#L1117)
- [SSE 错误处理](https://github.com/Wei-Shaw/sub2api/blob/3a6fd1c9db07203ca308aaba69e502bc1f35b307/backend/internal/service/openai_gateway_response_handling.go#L525)
- [错误分类](https://github.com/Wei-Shaw/sub2api/blob/3a6fd1c9db07203ca308aaba69e502bc1f35b307/backend/internal/service/openai_gateway_passthrough.go#L1602)
- [reasoning sanitizer](https://github.com/Wei-Shaw/sub2api/blob/3a6fd1c9db07203ca308aaba69e502bc1f35b307/backend/internal/service/openai_gateway_request_body.go#L428)
- [lineage 类型白名单](https://github.com/Wei-Shaw/sub2api/blob/3a6fd1c9db07203ca308aaba69e502bc1f35b307/backend/internal/service/openai_encrypted_content_lineage.go#L32)
- [粘性调度](https://github.com/Wei-Shaw/sub2api/blob/3a6fd1c9db07203ca308aaba69e502bc1f35b307/backend/internal/service/openai_account_scheduler.go#L495)
- [agentMessageText 仅拼接内容](https://github.com/Wei-Shaw/sub2api/blob/3a6fd1c9db07203ca308aaba69e502bc1f35b307/backend/internal/pkg/apicompat/chatcompletions_responses_bridge.go#L657)

## 已执行的局部验证

从两个版本原样提取实际 Go 函数，使用独立标准库模块执行 `GOTOOLCHAIN=local go test -v -count=1 ./...`。每个版本六个清理样例与五种类型判断，均 exit 0。

| 样例 | 观察结果 |
| --- | --- |
| reasoning 顶层密文 | 移除密文，保留 summary |
| compaction 密文 | 移除该项 |
| function_call_output 携带合成 encrypted_content 字段 | 原样保留 |
| agent_message 携带合成顶层 encrypted_content 字段 | 原样保留 |
| agent_message.content[] 嵌套密文 | 原样保留 |
| reasoning + agent_message 混合历史 | 只清理 reasoning |

lineage 判断 reasoning/compaction/compaction_summary 为 true，function_call_output/agent_message 为 false。合成字段用于验证函数边界，不能当作已知真实故障 body。

两个版本以下函数分别逐字相同，其 SHA-256 为：

| 函数 | SHA-256 |
| --- | --- |
| trimOpenAIEncryptedReasoningItems | `2c73fda59871cd129fab4a576e985c5e5dc9d107c24f39e3e0b9b2b6df3258ce` |
| sanitizeEncryptedReasoningInputItem | `cf552ca4b63efeda4479e3608d8638aafe59bfa7926128e228a810de07cb34f7` |
| openAIEncryptedLineageItemType | `f527b372cdd5137c9ec035e5d76c5fb0a85b0a73ec7eae4897b2bfe4f30d4bff` |

这是辅助函数级证据，未运行完整 sub2api 测试，未执行真实上游密码学解密或现场请求复现。临时完整取证保存在本地 `/tmp/sub2api-7708-audit/`，不把该临时目录作为其他工程师执行计划的前置依赖。
