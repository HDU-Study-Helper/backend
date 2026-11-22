# Agent 内部集成

框架主体与交付说明在 [agent/framework](../../../agent/framework/README.md)。本包适配既有 `bridge.Port`，并在宿主层装配Learner暂存编辑及提交，不注册 HTTP 路由或创建后台进程。暂存实现见[learnerstage](../learnerstage/README.md)，不在通用框架中添加角色业务策略。

日程宿主工具通过可选 `Dependencies.Schedules` 注入，提供查询、详情、创建、修改、删除及独立权限；契约和验证见 [SCHEDULE-TOOLS.md](SCHEDULE-TOOLS.md)。

Learner 暂存工具使用同一注册契约供执行和可信宿主目录导出：`RegisterLearnerStageTools` 注册 state/select，state输出完整11字段（含nullable base_version）且标只读。通用工具通过可信 `Builtins.TransformSpec` 在注册前生成暂存专用Schema，禁止initial_private_memories并明确target Workflow PRI、非空source_refs要求，不再只在Provider展示层删除参数。暂存运行复用已装配Search，日程仅注册query/get并经只读Bridge再次检查操作；没有接线任何日程写handler，也不扩大Agent已有权限。

15个宿主工具的可审阅目录为 `tool-catalog.json`，由真实 `RegisterScheduleTools`、`RegisterLearnerStageTools` 和 `actions.RegisterTools` 离线生成，不执行handler、不连接数据库或模型。后端根目录运行 `go run ./cmd/tool-catalog > internal/agent/tool-catalog.json`，以 `go run ./cmd/tool-catalog -check internal/agent/tool-catalog.json` 或该命令包的测试核验。通用21工具目录由framework单独生成，二者并集36个名称，具体运行仍受权限与暂存专用注册控制。

`New(Dependencies)` 返回带 Registry、Tools、Runtime 的 Service。宿主注入同一个 `dbbridge.DB` 的 `Agents()`、Provider 映射、可选 SearchProvider 及 BackendServices；后者接入通知服务和 `DB.AgentData()`。

每次 `Run` 由宿主设置 `Asia/Shanghai` 业务时区，通用 Runtime 将当前 Run 的审计 `StartedAt` 作为可信参考时间注入 system，上下文含 UTC、本地时间、时区和本地日期。父子 Run 及 Learner 临时 Runtime 各自取启动时间；业务输入不能覆盖它。参考时间与来源发布时间、观察时间分开，是否仍有个人行动价值由 Agent 根据证据和任务判断，不由通用框架硬编码过期或个人身份结论。

可信调用方使用 `Run(ctx, ownerID, request)` 或 `Call(ctx, bridge.Request{Version:"v1", OwnerID:ownerID, Operation:"agent.run", Payload:...})`。OwnerID 不能来自模型或未验证请求体。调度通过 DispatchBridge 复用此 Port，父预算/祖先信息通过私有 Go context key 保留，不能从 JSON 伪造。

`agent_dispatch`支持统一Worker参数 `workflow:{workflow_id,version}` 与 `context.event`；工具检查同用户、active和版本及workflow.read权限后为子调用绑定可信上下文，父调用权限不变。Run记录保留版本快照，返回仍为run_id/output/error。无workflow的旧调用兼容。完整契约见 [DISPATCH.md](../../../agent/framework/DISPATCH.md)。

[example_test.go](example_test.go) 提供只编译、不连接真实服务的装配示例。完整 PostgreSQL + Mock Provider 验收位于 `dbbridge/agent_test.go` 的 `TestFrameworkAcceptancePersistedBridgeDispatch`。真实通知服务缺失时明确 NOT_IMPLEMENTED，Mock 收据不代表移动端交付。
