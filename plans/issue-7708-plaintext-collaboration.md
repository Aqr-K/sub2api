# #7708 第二批：新会话的明文协作预防

日期：2026-10-10。实现分支：`fix/7708-plaintext-collaboration`。基于 fork 主线 `7eabdaf052a47fd904c6fe1e84b677a3e95ac53c`；第一批错误分类和已知用量保留行为继续有效。

本批实现默认关闭的账号选项，使支持的 Codex collaboration 调用在生成前使用普通 function 参数，并在响应中恢复 Codex 的明文接收标志。它预防这类新协作消息生成密文，不解密旧历史，也不保证所有来源的 `invalid_encrypted_content` 消失。#7708 现场密文首次失效的具体原因仍未被复现。

## 启用

在 OpenAI 账号的创建、编辑或批量编辑中打开 **“明文协作消息（实验性）”**。对应配置：

```json
{
  "extra": {
    "openai_responses_plaintext_collaboration": true
  }
}
```

缺省或 `false` 保持既有转发行为。创建/编辑关闭时删除这个键；批量编辑只有勾选该项才修改，明确关闭时写入 `false`。不需要数据库迁移。

适用 OpenAI API Key、OAuth 和 Setup Token 账号，以及原生 Responses HTTP/JSON、SSE、WebSocket ctx_pool、passthrough 和 HTTP bridge 链路。强制 Chat Completions、Anthropic 等出口不支持，启用时明确拒绝。请求实际选出的非 Responses 协议也会拒绝。compact 请求不执行该转换。

只有实际发生转换时才要求入站 User-Agent 声明稳定版官方 Codex，版本至少 `0.154.0`；`0.162.0` 已直接检查源码并运行客户端协议测试。没有版本、旧版本和预发布版本不会被网关的出站版本头掩盖，而是在调用上游前拒绝。该版本门槛是已检查协议的保守下界，不代表所有后续客户端都经过实测。

先用实际部署的模型和上游验证，再对同一池中的账号一致启用，并新建 Codex 会话。既有 HTTP 会话没有新增集中策略存储，不承诺在混合开关账号间迁移无感；关闭开关也应以新会话为边界。

## 请求与响应如何转换

1. 独立扫描 `tools`、`additional_tools`、`tool_search_output` 中的 function 参数 schema，发现 `encrypted: true` 的位置。此扫描不以三个工具名为范围；未知命名空间、外部 function/MCP 和未来工具仍可被发现。只遍历 schema 的结构关键词，不搜索用户正文、示例或工具结果。
2. 当前经过客户端协议验证的适配器只处理 collaboration 的 `spawn_agent`、`send_message`、`followup_task`，且仅允许其字符串 `message` 参数带加密声明。把它们变为 `collaboration__<name>` 普通 function 并移除该参数的加密标志。若同一工具还有其他加密参数、声明冲突或别名碰撞，原子拒绝，不猜测协议。
3. 同步转换工具选择和相关历史调用的寻址。保留参数文本、call ID、工具结果；明确带非空加密参数标记的旧调用保留原身份。控制工具及其他协议保持原有语义。
4. 回程在 JSON、SSE、WS 的已知工具位置恢复 `namespace: "collaboration"`、原工具名，并明确补上 `encrypted_function_args: []`。客户端由此走 `DirectPlaintextMessage` → `AgentMessage::Plaintext` 的投递分支。参数本身不经解密或重写。
5. WS 缓存客户端最后一次被接受的原始工具声明；省略 tools 时继承，显式空数组/null 会替换它。账号切换时重新生成映射，非法请求不覆盖已接受的声明。

```text
Codex 声明 message encrypted:true
  → sub2api 转成普通 function
  → 上游按普通 function 生成参数
  → sub2api 恢复 collaboration 名称 + encrypted_function_args:[]
  → Codex 通过自身明文分支投递 agent_message
```

**“通用发现”和“协议适配”分开。** 三工具名单是当前可转换协议的范围，不是密文来源总清单。新增工具需增加经过验证的适配器；发现未知加密声明时原样保留，并在请求 context 的 `openai_responses_unadapted_encrypted_tools` 留下名称和参数路径清单。该清单不包含参数值，也不证明没有其他加密输出来源。

history/notes 有独立后端协议，MCP 可通过 `_meta["codex/encryptedContent"]` 返回加密内容；这些不能靠删除声明通用转换。本批保留其内容和协议，也不删除 reasoning 密文。

## 异常保护

- 上游若对映射后的普通 function 返回非空或畸形 `encrypted_function_args`，拒绝该响应；不能把密文重标为明文。畸形 JSON 中的映射别名也不会直接泄漏给客户端。
- 适配失败使用独立兼容性错误，不触发账号换号/重发，不计入账号调度失败；有图片结果也不能把该失败当成功。
- HTTP JSON/SSE/SSE→JSON 保留已经读取的用量，由现有 handler 提交一次用量记录；不建立成功响应绑定。缺失终态的 SSE→JSON 返回兼容性错误。
- WS 在错误结果中保留已知用量。透传在完成回调前检查适配，避免先回调成功再回调失败；后续轮次的异常用量不混入前面已完成的轮次。现有 WS handler 对失败 turn 的结算规则继续保留，不能把本批描述为统一了所有失败结算行为。
- 流已经开始时，已发送的数据无法撤回；转换异常会结束本轮，不自动恢复旧密文或重新执行工具。

## 来源与客户端证据

双向工具映射、传输接线和账号 UI 的起点复用了官方仓库中**已关闭、未合并的 [PR #7520](https://github.com/Wei-Shaw/sub2api/pull/7520)**，固定 head `4fadfa0faf2158b75d184b173d975e88309430d6`。本批在该原型基础上增加通用声明发现、客户端版本检查、额外加密参数保护、旧历史与外部同名工具保护、错误分类/用量回传和 WS 原子状态与失败回调回归验证；没有把原型状态当成官方已验证或已合并。

用户终端版本为 Codex `0.162.0`，源码固定于 [`c1382380de69521303b416720a52f42d51af6248`](https://github.com/openai/codex/tree/c1382380de69521303b416720a52f42d51af6248)。更多协议边界见 [源码审计](issue-7708-codex-encryption-audit.md)。

原生客户端协议测试使用官方 `codex-x86_64-unknown-linux-musl` 0.162.0，下载压缩包 SHA256：`8daf67f6261161aa5939d8d42a516032d760406216779d7ce6040f242140ff73`。上游为本地脚本化 HTTP 服务：

- CLI 发出三项带 message 加密声明的 collaboration 工具；转换后三个别名的 message 均不带标志，三个控制工具保留 namespace。
- 服务返回经过还原的 `send_message` 自发消息，显式携带空加密参数数组。
- 下一次 Responses 请求包含一个 `agent_message`，nonce 出现在 `input_text`，加密块数量为零。
- CLI 在该非 OpenAI provider 配置下重放历史时省略了 `encrypted_function_args`。因此不能要求所有明文历史调用都必须保留 `[]`；空数组的关键作用在客户端接收调用时。

这是实际 CLI 的协议验证，没有执行真实模型推理、跨账号密码学实验或完整的自主 spawn/followup 生命周期；测试没有委派实现工作。

## 验证记录

后端使用 Go 1.27.2。验证命令：

```sh
GOTOOLCHAIN=go1.27.2 GOMAXPROCS=4 go test -p 2 -tags=unit ./...
GOTOOLCHAIN=go1.27.2 GOMAXPROCS=3 go test -p 2 -race -tags=unit \
  ./internal/pkg/apicompat ./internal/service ./internal/handler \
  -run 'Collab.*Plaintext|Plaintext.*Collab|DiscoverResponsesEncryptedTools|EncryptedHistory|PassthroughLifecycle' -count=1
CI=true GOTOOLCHAIN=go1.27.2 GOMAXPROCS=2 go test -p 2 -tags=integration ./internal/repository \
  -run 'TestUsageBillingRepositoryApply|TestUsageLogRepoSuite|TestUsageLogRepositoryCreate_BatchPathDuplicateRequestID|TestBillingCache' -count=1 -v
SUB2API_TEST_NATIVE_CANARY=1 SUB2API_TEST_CODEX_BIN=/path/to/codex-0.162.0 \
  GOTOOLCHAIN=go1.27.2 go test ./internal/pkg/apicompat \
  -run '^TestAdaptResponsesCollaborationPlaintext_NativeCodexCanary$' -v -count=1
```

原生 CLI 测试默认跳过，必须显式设置上述两个环境变量。PostgreSQL/Redis 控制组实际启动隔离容器；它验证已有计费去重和持久化，不是完整线上结算实验。handler 回归使用真实 HTTP 转发和内存用量仓库。

前端检查范围为创建/编辑/批量编辑及语言键一致性：4 个测试文件、188 个测试；另检查 Vue/TypeScript 类型和改动文件 ESLint。后端静态检查使用由 Go 1.27.2 编译的 golangci-lint v2.14.0，没有修改 lint 配置。

最终结果：后端全量 unit、上述相关 race、PostgreSQL/Redis 控制组、实际 Codex 0.162.0 协议测试全部通过；golangci-lint 为 0 issues。前端 188 项测试、类型检查、改动文件 ESLint 及 git diff --check 均通过。尚未进行真实模型自主协作和跨账号复验。
