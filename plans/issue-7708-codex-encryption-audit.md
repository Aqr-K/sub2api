> 本文是实施前的源码审计快照；随后实际 Codex 0.162.0 协议测试和实现范围见 [第二批实现](issue-7708-plaintext-collaboration.md)。

Codex 工具密文与 sub2api 明文适配审计

审计日期：2026-10-10。范围：公开 Codex 源码、四个 release tag，以及 sub2api 已提交修复的边界；未修改业务代码，未执行真实模型加解密实验，也未运行 Rust 测试。全文的“支持”指已检查源码中的协议/执行分支，不等于任意模型、账号和客户端组合均已实测。

**结论**

“Codex 只有三个工具使用密文，外部工具不会使用密文”不成立。在本次固定的 Codex main 中，找到七个内置工具的显式加密参数声明：三个 collaboration 工具和四个 history/notes 工具。此外，history/notes 和外部 MCP 都有加密结果进入模型上下文的实际转换路径。动态 function/MCP 输入 schema 也能携带加密声明，因此七个不是可扩展工具生态的上限。

三工具名单真正限定的是客户端一条专门的明文协作接收分支。不能把该名单当作所有密文来源的清单，也不能把这条接收分支推广成所有工具通用的降级协议。

**固定版本与复查方式**

| 检查对象 | commit | 说明 |
| --- | --- | --- |
| main 快照 | `806d9732c974bc8a51b8317c1bd8985544fe627c` | 本报告默认链接的源码 |
| rust-v0.162.1 | `092d3acd6bec3e3a14bdc7e7a2810ab628ab759d` | 已发布版本对照 |
| rust-v0.162.0 | `c1382380de69521303b416720a52f42d51af6248` | 用户终端确认使用的版本，已补充直接源码检查 |
| rust-v0.154.0 | `6b9826e3aa83b1a5947db50f4332cb9c65f1b340` | 较早版本对照 |
| rust-v0.144.6 | `5d1fbf26c43abc65a203928b2e31561cb039e06d` | 助手命令执行上下文中 `/usr/bin/codex` 的版本，不代表用户终端版本 |

源码位于 `/tmp/sub2api-7708-codex-audit-20261010/source`，四个 tag 已获取。用 `git show TAG:文件路径` 复查，不需要切换工作树。用户提供的终端输出为 `codex-cli 0.162.0`，当前用户场景以此为准。助手执行工具中的 `command -v codex` 指向 `/usr/bin/codex` 并报告 0.144.6，不能据此把用户终端判断为旧版；两种命令上下文的具体差异未定位。其他下游客户端版本仍需分别确认。

**一、显式声明加密参数的内置工具**

| 工具 | 参数 | 接收端机制 |
| --- | --- | --- |
| `collaboration.spawn_agent` | `message` | 支持特定明文分支，受客户端版本和返回元数据约束 |
| `collaboration.send_message` | `message` | 同上 |
| `collaboration.followup_task` | `message` | 同上 |
| `history.search_contents` | `query` | 转发到独立 Codex backend API |
| `notes.search_contents` | `query` | 同上 |
| `notes.append_to_file` | `text` | 同上 |
| `notes.write_file` | `text` | 同上 |

前三者使用 `.with_encrypted()`；后四者在 JSON schema 写入 `"encrypted": true`。这是本次对固定源码生产代码的显式声明盘点，不包含测试数据，也不把仅消费密文的代码重复计作新工具。

证据：[协作工具定义](https://github.com/openai/codex/blob/806d9732c974bc8a51b8317c1bd8985544fe627c/codex-rs/core/src/tools/handlers/multi_agents_spec.rs#L204)、[spawn 参数](https://github.com/openai/codex/blob/806d9732c974bc8a51b8317c1bd8985544fe627c/codex-rs/core/src/tools/handlers/multi_agents_spec.rs#L648)、[history/notes 参数](https://github.com/openai/codex/blob/806d9732c974bc8a51b8317c1bd8985544fe627c/codex-rs/ext/history-notes/src/tools.rs#L177)。

history/notes 并非所有会话默认启用。该扩展注册后，还要求 token budget 配置中的 `use_history_notes_extension` 为真、provider 被识别为 OpenAI，且当前认证使用 Codex backend；此配置默认不启用。不能根据源码存在便断定某个 sub2api 用户已暴露这些工具。

证据：[扩展启用判断](https://github.com/openai/codex/blob/806d9732c974bc8a51b8317c1bd8985544fe627c/codex-rs/ext/history-notes/src/extension.rs#L45)、[配置解析](https://github.com/openai/codex/blob/806d9732c974bc8a51b8317c1bd8985544fe627c/codex-rs/core/src/config/mod.rs#L2889)。

**二、协作明文分支具体保证什么**

`ToolCall::direct_source()` 同时检查 namespace 为 `collaboration`、名称属于上述三个工具，以及 `encrypted_function_args` 为显式空数组 `[]`，才返回 `DirectPlaintextMessage`。字段缺失、null、非空数组均不满足该条件。

执行器把此来源转换为 `AgentMessage::Plaintext`，否则走 `AgentMessage::Encrypted`。投递层保留发送者、接收者以及 QueueOnly/TriggerTurn 语义；因此，明文协作并不从机制上要求关闭多 agent。自动完成通知通过 `InterAgentCommunication::new` 发送普通文本；本次检查未发现这条自动完成路径必须另行加密。

这支持为兼容客户端设计协作明文适配，但不能代替真实模型的完整生命周期验证。代理必须在生成前改变已确认可降级的声明，并确保客户端收到正确的明文元数据。不能把已经生成的密文改标为明文；缺少密钥不会因为改字段而恢复原文。

证据：[精确接收条件](https://github.com/openai/codex/blob/806d9732c974bc8a51b8317c1bd8985544fe627c/codex-rs/core/src/tools/router.rs#L55)、[来源转消息类型](https://github.com/openai/codex/blob/806d9732c974bc8a51b8317c1bd8985544fe627c/codex-rs/core/src/tools/handlers/multi_agents_v2.rs#L55)、[投递语义](https://github.com/openai/codex/blob/806d9732c974bc8a51b8317c1bd8985544fe627c/codex-rs/core/src/agent/control/delivery.rs#L11)、[自动完成通知](https://github.com/openai/codex/blob/806d9732c974bc8a51b8317c1bd8985544fe627c/codex-rs/core/src/agent/control/completion.rs#L115)。

**三、history/notes 的输入和输出都不能套用协作规则**

客户端向 `alpha/history/v2/...`、`alpha/notes/v2/...` 发起独立后端请求。上述四个需要密文参数的操作固定附加 `x-openai-encrypted-tool-arguments: true`；执行器没有依照协作明文标志切换后端协议的逻辑。

因此，只在 Responses 请求里删除 `encrypted: true`，会让“模型输出明文”与“执行器仍声明参数为密文”不一致。是否可同时切换这些独立接口以及后端是否支持等价明文模式，本次源码不能证明，需要客户端/工具后端配合验证。

更重要的是，全部九种 history/notes 操作共享结果转换：后端 JSON 出现 `encrypted_output` 字符串时，将它包装成 `FunctionCallOutputContentItem::EncryptedContent`。没有此字段时使用普通文本。九种操作包括 history 的 list_windows、list_items、read_item、search_contents，以及 notes 的 list_files_by_prefix、read_file、search_contents、append_to_file、write_file。

所以，即使某个工具的输入参数没有加密声明，输出仍可能是密文。这里说明能力和条件，不表示九个工具每次都返回密文。

证据：[后端请求](https://github.com/openai/codex/blob/806d9732c974bc8a51b8317c1bd8985544fe627c/codex-rs/ext/history-notes/src/backend.rs#L47)、[输出转换](https://github.com/openai/codex/blob/806d9732c974bc8a51b8317c1bd8985544fe627c/codex-rs/ext/history-notes/src/tools.rs#L332)。

**四、外部 function/MCP 的真实边界**

外部输入：动态 function 和 MCP 工具使用通用 `parse_tool_input_schema`；schema 类型具有可序列化的 `encrypted: Option<bool>`。常规合法且未被大 schema 压缩裁掉的参数节点，可以保留 `encrypted: true`，没有仅限三个协作工具的通用限制。大 schema 存在有损压缩，不能承诺任意深度和任意大小下所有声明均保留。声明可传递不等于任意 MCP 服务具备密钥，也不等于上游必定支持该声明。

证据：[动态工具解析](https://github.com/openai/codex/blob/806d9732c974bc8a51b8317c1bd8985544fe627c/codex-rs/tools/src/dynamic_tool.rs#L5)、[MCP 解析](https://github.com/openai/codex/blob/806d9732c974bc8a51b8317c1bd8985544fe627c/codex-rs/tools/src/mcp_tool.rs#L39)、[schema 字段](https://github.com/openai/codex/blob/806d9732c974bc8a51b8317c1bd8985544fe627c/codex-rs/tools/src/json_schema/types.rs#L47)、[通用解析](https://github.com/openai/codex/blob/806d9732c974bc8a51b8317c1bd8985544fe627c/codex-rs/tools/src/json_schema.rs#L25)。

MCP 输出：返回的单个 text content block 若含 `_meta["codex/encryptedContent"] == true`，Codex 将该 text 作为不透明密文写入 `encrypted_content`；普通未标记 text 则写为 `InputText`。这是生产转换路径，不只是日志脱敏。只要存在密文内容，转换优先保留 content items，不使用 structuredContent 替代。

```json
{
  "type": "text",
  "text": "<服务提供的密文>",
  "_meta": {"codex/encryptedContent": true}
}
```

该机制不要求输入参数加密，所以“去掉所有工具声明中的 encrypted”仍不能消除此类输出。Codex 在这里转运服务提供的密文，没有把所有普通 MCP 结果自动加密。不能删除元数据后就把不可读密文当成可用结果。

证据：[MCP 标志转换](https://github.com/openai/codex/blob/806d9732c974bc8a51b8317c1bd8985544fe627c/codex-rs/protocol/src/models.rs#L2456)、[结果优先级](https://github.com/openai/codex/blob/806d9732c974bc8a51b8317c1bd8985544fe627c/codex-rs/protocol/src/models.rs#L2414)、[进入 function_call_output](https://github.com/openai/codex/blob/806d9732c974bc8a51b8317c1bd8985544fe627c/codex-rs/core/src/tools/context.rs#L157)。

还应区分 MCP 与 app-server 动态工具返回协议：当前 `DynamicToolCallOutputContentItem` 只列出 InputText/InputImage/InputAudio，不能据 MCP 的能力推断每种外部返回通道都具有同一个密文变体。证据：[动态工具返回类型](https://github.com/openai/codex/blob/806d9732c974bc8a51b8317c1bd8985544fe627c/codex-rs/protocol/src/dynamic_tools.rs#L59)。

**五、其他密文字段不等于新的“密文工具”**

`ResponseItem` 对 Message、FunctionCall、CustomToolCall、LocalShellCall、ToolSearchCall、WebSearchCall、ImageGenerationCall 等支持顶层 `encrypted_content`。这是响应/历史表示能力，不证明每种工具每次都产生加密参数。reasoning 和 compaction 也具有独立加密状态，应与参数、工具结果、agent_message 的嵌套内容分别处理。

非 OpenAI provider 请求中的 `clear_encrypted_content` 只清理相关顶层字段；它不解密 `agent_message.content[]` 或 `function_call_output.output[]` 的嵌套密文。因此更改 provider 身份也不能当作全局明文开关。

另外，Search API 的 `SearchResponse.encrypted_output` DTO 字段存在，但本次没有找到生产工具消费该字段并加入模型上下文的闭环，故没有把它计入已确认的独立工具输出来源。

证据：[响应类型](https://github.com/openai/codex/blob/806d9732c974bc8a51b8317c1bd8985544fe627c/codex-rs/protocol/src/models.rs#L1036)、[顶层清理](https://github.com/openai/codex/blob/806d9732c974bc8a51b8317c1bd8985544fe627c/codex-rs/protocol/src/models.rs#L1300)、[provider 分支](https://github.com/openai/codex/blob/806d9732c974bc8a51b8317c1bd8985544fe627c/codex-rs/core/src/client.rs#L1002)、[Search DTO](https://github.com/openai/codex/blob/806d9732c974bc8a51b8317c1bd8985544fe627c/codex-rs/codex-api/src/search.rs#L298)。

**六、已核对的版本差异**

| 行为 | 0.144.6 | 0.154.0 | 0.162.0 / 0.162.1 / main 快照 |
| --- | --- | --- | --- |
| 三个协作工具的加密参数声明 | 有 | 有 | 有 |
| `[]` 触发协作明文接收 | 无此分支 | 有 | 有 |
| history/notes 四项加密参数和共享密文输出 | 无此扩展目录 | 有 | 有 |
| MCP `codex/encryptedContent` 结果转换 | 无此标志转换 | 有 | 有 |

这是具体 tag 对照，未定位所有功能首次发布版本，也不把任意未来版本自动视作兼容。0.144.6 的协作执行代码直接调用 `new_encrypted`，代理只删声明仍可能将明文任务包装成密文继续投递，制造下一轮失败。

用户使用的 0.162.0 已确认具有 `encrypted_function_args: []` 触发的协作明文接收分支，执行器会选择 `AgentMessage::Plaintext`；上述旧版限制不适用于该版本。该版本同时包含 history/notes 的四项加密声明、共享密文输出以及 MCP 密文结果转换，因此通用工具密文的适配边界仍成立。证据：[0.162.0 明文入口](https://github.com/openai/codex/blob/c1382380de69521303b416720a52f42d51af6248/codex-rs/core/src/tools/router.rs#L44)、[0.162.0 消息类型选择](https://github.com/openai/codex/blob/c1382380de69521303b416720a52f42d51af6248/codex-rs/core/src/tools/handlers/multi_agents_v2.rs#L55)。

证据：[0.144.6 协作投递](https://github.com/openai/codex/blob/5d1fbf26c43abc65a203928b2e31561cb039e06d/codex-rs/core/src/tools/handlers/multi_agents_v2.rs#L55)、[0.154.0 明文入口](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/core/src/tools/router.rs#L44)、[0.162.1 明文入口](https://github.com/openai/codex/blob/092d3acd6bec3e3a14bdc7e7a2810ab628ab759d/codex-rs/core/src/tools/router.rs#L44)。

**七、对 sub2api 方案的修正建议**

建议采用“通用发现 + 按协议适配 + 客户端能力校验”，不要实现统一递归删除 encrypted 字段。

1. 通用发现覆盖发送给模型的工具声明、namespace、延迟加载新增工具和后续返回内容；识别参数声明、调用元数据、嵌套密文结果，不能只扫描三种工具名称。仅凭首次请求也无法穷尽未来工具目录和未来输出。
2. 转换层采用可扩展适配器。首个可验证适配器面向 collaboration 的现有明文协议，在兼容客户端的新会话从首次请求启用，并覆盖子会话和后续回合。精确限定适配器适用范围是协议约束，不是把整个发现机制锁死在三个名字上。
3. history/notes 需要独立后端接口的明文输入/输出支持；MCP 需要工具服务的明文返回能力或明确可用的等价结果。当前没有证据支持代理独自完成这些转换且完全不影响工具行为。
4. 未知工具按显式策略处理。兼容策略保留原协议，并报告未覆盖范围；它无法保证无密文。严格无工具密文策略必须在能力不满足时拒绝或禁用相应工具，会影响使用，不能宣称下游无感。支持这些工具的完整明文协议后，才可能兼得。
5. 本轮不把已有密文转换为明文、不删除工具历史，也不把 reasoning/compaction 的独立状态混入工具降级。对已存在密文，来源约束/路由兼容性需另行设计，不能假设粘性账号足以证明解密必然成功。

当前用户仓库 `Aqr-K/sub2api` 的 main 为 `7eabdaf052a47fd904c6fe1e84b677a3e95ac53c`，本地工作树干净并与已获取的 origin/main 一致。已有修复针对错误分类、错误信息与已知用量保留，以及避免误切账号/处罚账号；它不是生成前的明文预防功能。本次没有改动或推送该仓库。

本审计不能仅凭 #7708 的错误文案判断其坏项来自 collaboration、MCP、history/notes 或其他历史字段，也未证明首次密文失效的密码学原因。下一阶段若实施协作适配，验收须包含真实模型的 spawn/send/followup、完成通知、历史继承、跨回合、流式与非流式路径，以及明确的版本/工具覆盖范围；不能仅用伪上游响应通过代替端到端成功。
