# 第一版后端架构 v0.1

状态：按用户最新任务实现可运行 v0.1 模块化单体。已包含 dbbridge Repository/迁移、users/会话/管理页、dws 官方 CLI 封装/受控轮询/归档、bridge 进程内 Demo；实际范围与验证见 README。三个学习平台Adapter、统一platform采集与preview预览也已实现；下面Handle、Agent等完整架构仍是后续目标，不能视为均已实现。

当前运行依赖：`cmd/api → app → users / platform / dws / preview / bridge`；业务服务通过 `model` 的Repository接口调用 `dbbridge → PostgreSQL`。`platform → contract.Adapter → chaoxing / mooc / ucampus`，`dws → credential + 官方CLI`；学习平台会话/游标也由credential加密。只有dbbridge执行SQL。app管理依赖及HTTP/Worker生命周期，main不承载业务逻辑。通用contract.Adapter已用于三个学习平台；DWS仍保留专用服务和路由，handle 当前无 Go 实现；通用 Agent Framework 已在 `../agent/framework` 实现，由 `internal/agent` 适配 Bridge，尚未装配到 HTTP App，详见其 README。完整目录说明见 `internal/README.md`。

## 1. 分层与依赖

```text
Android / HTTP API → handle.Service ← Worker
                      ├─→ contract.Adapter registry
                      │     ├─ jwxt
                      │     ├─ dws
                      │     ├─ chaoxing
                      │     ├─ ucampus
                      │     └─ mooc（中国大学 MOOC，含 SPOC）
                      │          平台实现 → credential + 平台 HTTP/CLI
                      └─→ agent.Service
                            └─→ agent.Tools（按用户和 run 绑定）
                                  └─→ 受控工具网关 → 检索 / Adapter registry
```

目录同级不等于相互依赖。平台包只依赖共享 `contract`、受限 `credential` 和各自客户端。通用 Agent Framework 依赖自己的接口契约，`internal/agent` 适配进程内 Bridge；Agent 不导入平台实现、凭据、SQL 或 shell。`handle` 可以导入 `agent` 和 `contract`，通过依赖注入调用平台接口。工具网关在后续基础设施包实现并由启动入口组装，不能让 agent 与 handle 循环导入。

`cmd/api` 调用 app.LoadConfig/New/Run，app 显式打开 dbbridge、执行版本化迁移、构造用户服务、可选 DWS 服务与 bridge Demo。当前 DWS Worker 在同一进程内显式启动，停止时取消并等待；不创建独立 worker 二进制。数据库驱动 pgx v5；SQL 集中于 dbbridge，当前无需引入 sqlc。DWS jobs 持久化在 PostgreSQL，内存 channel 仅唤醒执行，重启可恢复 pending；不构建完整消息总线、通知或 Agent。

## 2. 两种数据路径

### 同步路径

1. Handle 认证用户，校验连接归属、状态和平台能力，持久化同步任务，返回 Job。
2. Worker 先通过 ListPartitions 分页发现资源的可读课程/群/账户流并创建分区任务；再按 `(user, connection, resource, partition)` 获取带 fencing token 的租约，一个分区同一时刻仅一个有效执行者。
3. Adapter 执行一页读取，返回 Record 与不透明 NextCursor；不得自行推进中心 checkpoint。
4. 业务层在同一事务提交原始记录版本、待分析 outbox 和 checkpoint；提交失败继续使用旧 checkpoint 重试。
5. 检索/Agent 异步处理已提交证据；模型候选先经输出校验再进入学业事件表。

不得把云端任务实现为 HTTP 请求结束后无人管理的 goroutine。空页可以有 NextCursor；HasMore 与 NextCursor 的细则见 CONTRACTS.md。

### Agent 路径

1. 主动模式由原始记录 outbox 触发；被动模式由调查/对话请求触发。
2. Worker 创建 run，绑定用户权限、预算、Tools，再调用 agent.Service。
3. Agent 优先查已存课程/证据；不足时通过 ReadSource 请求授权来源的有界只读操作。
4. 工具网关检查连接归属与能力，结果先保存为不可变证据，再返回 EvidenceRef。
5. Handle 校验引用归属、版本、字段依据、事件状态和预算；事务保存候选、发现、回答与通知建议，再将 run 标为 succeeded。
6. Notification Service 独立应用去重、偏好、静默时间、投递策略。Agent 不直接发送通知。

## 3. 状态与事务边界

- Raw Record：按用户、连接、原生复合键识别逻辑记录，版本不可变；保留来源与采集时间。
- Academic Event：业务层维护当前版本；事件合并与课程映射由受控逻辑决定，Adapter 不做跨源合并。
- User Task：与事件分离，完成任务不等于改变外部平台作业状态。
- Confirmation：expected_version 乐观锁；冲突返回 409，记录用户确认及其证据版本。
- 同一事件新增证据推翻已确认字段时，新版本回到 pending/conflicted，不能继承旧确认。
- 课程映射按用户保存；不基于课程名自动跨用户共享消息。
- 断开连接：禁用后续读取、取消待执行任务、删除平台凭据；已在途任务的提交必须重新校验连接状态/租约，断开后不能复活 session。
- 删除连接数据：独立显式 purge job，清理原始版本、附件、索引与派生内容；共享多个来源证据的事件需要重算/撤回，不能留下悬空引用。

## 4. 安全和可靠性

- 应用用户认证与学校平台认证是两套系统。首版应用登录方式尚待确定；所有受保护 API 都依赖认证后 Actor。
- 凭据仅在授权处理与平台实现内短暂使用，静态存储统一受中心加密凭据管理。钉钉复用固定版本官方 DWS CLI 二进制，Go 只包装调用、隔离会话与转换结果，不重写协议或认证。CLI 原生凭据由受控加密工作区后端适配，保留官方刷新逻辑；仅独立配置目录不等于完整凭据隔离。子进程使用命令白名单、参数数组、连接专属环境与超时取消，参数/输出必须脱敏。详见 internal/dws/README.md。
- URL、二维码、附件链接可能携带票据，只有白名单挑战字段能发给当前用户；它们不能进入日志、学业记录或模型输入。
- 外部文本和文件均是不可信数据，不能授予额外工具权限或改变系统指令。
- 请求取消、超时、限流、解析异常必须显式返回；解析失败不得伪装成空数据或已完成全量同步。
- 队列至少一次交付；记录入库、分析结果、通知投递都需要对应幂等键。
- 通知只输出必要摘要；详情需重新认证；设备 Token 与平台凭据同样禁止日志输出。

## 5. 平台交付与核心负责人职责

平台成员负责：真实认证流程、平台分页、源字段保真、标准化、安全错误、脱敏 fixture、测试和 README。中心负责人负责：应用认证、凭据实现、存储/租约/outbox、Handle、工具网关、事件确认、通知服务和 HTTP 路由。Agent 负责人负责：模型工具映射、预算、结构化输出、证据关联与可重复测试。

每个模块交付优先一个可验收纵切面，不要求五个平台同时实现全部能力。中国大学 MOOC 独立使用 mooc 模块，通过同一 Adapter 注册与 Handle 编排。

## 通用 Agent Framework 第一阶段

当前已提供可注入的 Runtime、工具注册与权限、私有记忆、OpenAI 兼容模型适配、DeepSeek Search/Fetch 适配、Workflow 定义版本和运行记录。`internal/agent.Service` 实现 `bridge.Port`，A → 工具 → Bridge → Runtime → B；只由现有 dbbridge 执行 SQL。新增 v5 为框架表，不改变现有采集路径。通知通过 Bridge 请求后端独立 NotificationService，未实现时明确报错。没有提前实现 Filter/Worker/Learner、业务事件触发、Workflow 执行引擎或 HTTP 路由。架构、边界和验证详见 [框架说明](../agent/framework/README.md)。
