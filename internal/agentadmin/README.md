# Agent 临时管理后台

入口：`/admin/agents`，主后台 `/admin` 已增加导航。App 启动时装配 Agent Framework，共用现有 dbbridge、认证会话、CSRF、取消上下文和后端进程。框架主体仍位于 `agent/framework`，本包承担管理 HTTP 和页面职责。

## 页面能力

- **框架配置**：多个命名模型服务的 Base URL、默认模型、API Key、超时；独立 DeepSeek Search 配置。API Key 不回显，留空保留原值，显式勾选才清除。保存后下一次运行使用新配置；进行中的模型循环继续使用开始时选中的 Provider。
- **智能体管理**：按所属用户列出、新建、修改实例；编辑提示词、模型、工具与权限、启停状态和运行限制。手动输入文本或 JSON 发起真实 Runtime Run，显示输出、调用次数、耗时、最近 30 条运行记录。
- **结构化记忆**：按用户和 Agent 浏览、搜索标题/内容、查看 JSON 树、编辑分类和优先级、添加/删除、复制原记录。最多显示 1000 条，达到上限时提示缩小搜索范围。

页面及管理 API 仅管理员可用。管理员可以从用户选择器切换管理目标；owner_id 由受保护的后端管理入口验证，模型参数不能选择用户。切换用户后实例、记忆和记录均按该 owner 查询。默认没有开放新的客户端 Agent API。

## 模型接口协议

在「框架配置 → 模型服务」可选择 Chat Completions（默认）或 Responses。每个服务独立设置 `api_protocol` 为 `chat_completions` 或 `responses`；旧配置未填写时保持 Chat Completions。Base URL 仍填写 API 根地址，不带 `/responses` 或 `/chat/completions`。服务商和账号必须支持所选接口，不自动回退或关闭推理。

Responses 保留模型的 `reasoning_effort`（映射为 `reasoning.effort`），在同一次运行内将工具输出和供应商返回的推理项一并接续。请求使用 `store:false`，不使用远端对话 ID；供应商推理项不入库。`max_tokens` 映射到 `max_output_tokens`，显式「最大输出 Token」优先；Responses 不接受 `seed`、`presence_penalty`、`frequency_penalty`，返回参数错误而不静默丢弃。

## 长任务与模型请求策略

智能体勾选“不限推理轮数、工具次数与总运行时间”保存`limits.unlimited=true`，父子计数预算和总deadline不再低额度截断。未勾选的旧正数配置保持兼容；调度深度、循环、授权和字节预算保留。普通工具仍有独立超时，子Agent调度继承运行上下文。

模型服务可配置`timeout_seconds`（1–600，推荐600）、`max_concurrent`（1–32，默认2）、`retries`（0–5，推荐3）。同地址+密钥进程内共享请求并发，即使有多个服务别名或热更新也不扩并发；排队可取消。timeout、429、408、5xx及连接故障仅重试模型请求并指数退避，绝不自动重试工具写入。UI与HTTP验证均支持新参数；旧未配置并发默认2，旧重试值0保持无重试。

## 默认模型继承

「框架配置 → 默认模型服务」独立选择框架默认服务（`default_provider`）。选择「框架默认服务」的智能体使用该服务；固定指定其他服务的智能体不变。未显式选择时，只有一个可用服务可以直接使用；有多个可用服务则提示选择，不按列表顺序回退。

`model_config.mode=default` 表示继承所选服务的默认模型，允许 `model` 为空；每次 Run 开始时，后端从同一配置快照解析 Provider 和有效模型。保存框架默认模型后，已有继承实例下一次运行立即使用新模型，进行中的 Run 不切换。`mode=custom` 固定使用 `model`，即使名称等于某次默认模型也不会被覆盖。

兼容旧数据（无 mode）：模型名称等于当前默认或历史默认时按继承处理，其他名称保持自定义。历史默认列表保存在既有加密 Settings JSON 的 `legacy_models` 中，保存配置时由服务端维护，不接受客户端修改，无数据库迁移。无法从旧数据分辨“刻意固定为默认同名模型”的意图，此类实例请明确选择自定义并保存。列表与编辑器显示继承/自定义及当前有效名称。服务删除、缺失 API Key 或继承时默认名称为空会返回明确错误，不静默回退其他服务。

## 思考强度与额外模型参数

模型服务可配置默认思考强度：不指定（使用供应商默认）、关闭、low、high、max。所有模型按同一套规则处理，没有按模型名硬编码的默认值。智能体未设 `reasoning_effort` 就继承所选服务（包括自定义模型名称）；显式设置优先。智能体关闭保存为 `null`，编辑时与继承区分。

“更多模型参数与运行限制”中的额外参数编辑框接受 JSON 对象，支持 `top_p`（0–1）、`seed`（安全整数）、`presence_penalty` / `frequency_penalty`（−2–2）、`max_tokens`（正安全整数）；留空表示 `{}`。这里的 `max_tokens` 是提供商额外参数，与原有“最大输出 Token”配置分别保存。`reasoning_effort` 统一由选择框管理，若在 JSON 中同时填写会明确拒绝，避免两处设置互相覆盖。已有其他参数会先原样显示，未知字段、非对象、null、数组和无效 JSON 都会提示错误并保留编辑内容；最终能力和数值仍由服务端校验。

切换服务或模型只更新默认值提示，不重置选择框或 JSON 编辑内容。保存时生成独立参数对象，保持当前实例的其他运行限制，不修改尚未保存的原实例。`model_parameters_ui_test.go` 用 Node 执行页面真实内联 JavaScript 的离线契约测试，覆盖完整脚本编译、各思考强度、null/缺省区别、JSON错误与冲突、旧参数保留和运行限制；测试不访问后台或调用模型。

## 配置持久化和启动

v6 新增 `agent_framework_settings` 单行配置表：encrypted_settings 为现有 credential.Vault（AES-GCM）产生的完整密文，version 做并发版本校验；不建立新连接池或存储明文密钥。服务端使用既有 `CREDENTIAL_KEY`，必须长期稳定；没有该密钥时页面可浏览，但不允许保存配置。已存在密文而密钥丢失/错误时启动明确失败。

从现有配置启动本地后台：

```bash
cd backend
set -a
. ./.env
set +a
AGENT_DEMO_ENABLED=true HTTP_ADDR=127.0.0.1:2610 go run ./cmd/api
```

访问 `http://127.0.0.1:2610/admin/agents`，使用现有管理员账号登录。没有固定新增账号，不修改已有账号密码。关闭后端时同步取消运行请求，Run 结束状态按框架既有机制持久化。

`AGENT_DEMO_ENABLED` 默认 false；true 时开放 `demo` Provider 和“创建本地自测智能体”。此模式执行真实的 memory_list 工具、权限检查和 PostgreSQL 持久化，但输出由确定性本地 Provider 生成，明确标记 local_demo，不是外部模型推理。真实调用需在页面保存有效 API Key，并为实例选择对应服务与模型。

管理接口同步执行单次 Run；受限模式沿用默认180秒及后台正数上限，不限模式不设置总运行截止时间，仍支持请求取消。后台批次处理由受控Worker及持久化队列管理。Runtime、工具循环和 A→B 调度仍走内部 Go / Bridge。通知服务未实现时工具明确返回 NOT_IMPLEMENTED；DeepSeek 远程 Fetch 保持默认关闭。

App另注入现有日程Service，注册 `schedule_query/get/create/update/delete` 五个宿主工具；权限分别为 `schedule.read/write/delete`，通过工具目录显示真实Schema。Filter只开放query/get。详细语义与验证见 [日程工具契约](../agent/SCHEDULE-TOOLS.md)。

## API（挂载 /api/v1/agents，全部经过登录、管理员及写入 CSRF 校验）

| 方法与路径 | 行为 |
|---|---|
| GET /settings | 返回脱敏配置和可用状态 |
| PUT /settings | 以 version 更新并加密保存配置；冲突 409 |
| GET /tools | 返回已注册工具与输入输出契约 |
| GET /instances?owner_id=&after= | 查询所属用户实例，每页最多 100 条 |
| POST /instances?owner_id= | 新建实例 |
| PUT /instances/{agent}?owner_id= | 修改已有实例；不能跨 owner 改写 |
| GET /instances/{agent}/memories?owner_id=&q= | 搜索当前用户 PUB + 当前 Agent PRI + 当前 Workflow PRI（可选 workflow_id），支持有效性/状态过滤 |
| POST /instances/{agent}/memories?owner_id= | 新建三种归属的记忆 |
| PUT /instances/{agent}/memories/{memory}?owner_id= | PATCH 记忆，省略字段保留；归属变更重新验证 |
| DELETE /instances/{agent}/memories/{memory}?owner_id= | 删除记忆并清理关联 |
| POST /instances/{agent}/runs?owner_id= | 手动执行，body 为 `{input: ...}` |
| GET /instances/{agent}/runs?owner_id= | 最近 30 条运行记录 |

测试见 settings_test.go、dbbridge/agent_console_test.go。实际浏览器验证覆盖登录、实例创建修改、记忆 JSON 树及编辑、自测工具循环、密钥留空/清除与不回显、CSRF、用户隔离、普通用户拒绝、移动端和后端重启持久化。配置的真实 Provider 调用使用 httptest 验证，未消耗真实模型/搜索额度。

## 批量回归（使用已保存的密钥）

在 backend 目录运行；凭据从环境读取，不写入脚本、输出或报告：

```bash
set -a
. ./.env
set +a
python3 scripts/agent_regression.py --live --output ../.build/agent-regression.json
```

默认地址为 `http://127.0.0.1:2610`，可用 `--base-url` 修改；登录可以使用 `AGENT_TEST_USERNAME/PASSWORD` 覆盖 bootstrap 管理员。`--live` 会消耗真实模型及搜索额度；不加时仍批测管理接口和已启用的本地自测。脚本创建临时用户/智能体并在 finally 中清理；框架配置会原样保存一次，用于验证留空密钥仍被保留。不要与其他管理员同时编辑框架配置，否则版本冲突会如实记为失败。

覆盖所有 Agent 管理接口的方法，包括配置读写、工具目录、实例创建/修改/列表、记忆创建/修改/检索/删除、手动运行和历史，以及认证、CSRF、越权和错误配置。真实测试通过模型发起原版18个工具（V0.2新增的关联工具由独立 Runtime + PostgreSQL 测试覆盖）的调用，并检查服务端 `record.tool_executions` 的工具名和实际错误；不会仅凭模型文字判断成功。通知和默认关闭的 Fetch 以返回 NOT_IMPLEMENTED 为符合预期，不代表功能已实现。

`--inspect-only` 只读取脱敏配置和历史。`--repair-demo-timeout` 专门把角色为 demo、已切换至真实模型且仍为 30 秒的旧演示实例改为 180 秒，保留其他字段；普通回归不会修改现有智能体。

受限模式总时限和模型服务单次请求时限独立；不限模式不因模型轮数、工具次数或总时长截断，子调用继承可取消上下文。HTTP不再用6分钟写入时限截断长运行，批次入口推荐异步观察。单次模型请求最多600秒并按配置有限重试。取消返回CANCELLED，实际请求超时返回TIMEOUT，运行记录保留工具名、耗时和安全错误，不保存凭据或完整工具私有结果。

Memory V0.2 增加 `GET /workflows?owner_id=&after=`（每页100），以及 `GET/POST /instances/{agent}/memories/{memory}/relations`。所有记忆接口可选 `workflow_id`，管理员显式选择后仍核验同用户 Workflow。运行接口也支持该查询参数并绑定可信 WorkflowContext；不能在模型 JSON 中伪造。后台记忆页支持三种归属、属性、层级、有效期、JSON 内容/证据、历史筛选及直接关联；旧 PATCH 请求保留新字段。完整权限、迁移和 Schema 见 [Memory V0.2](../../../agent/framework/MEMORY-V0.2.md)。
