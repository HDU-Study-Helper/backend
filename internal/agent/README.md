# Agent 内部集成

框架主体与交付说明在 [agent/framework](../../../agent/framework/README.md)。本包只适配既有 `bridge.Port`，不注册 HTTP 路由或创建后台进程。

日程宿主工具通过可选 `Dependencies.Schedules` 注入，提供查询、详情、创建、修改、删除及独立权限；契约和验证见 [SCHEDULE-TOOLS.md](SCHEDULE-TOOLS.md)。

`New(Dependencies)` 返回带 Registry、Tools、Runtime 的 Service。宿主注入同一个 `dbbridge.DB` 的 `Agents()`、Provider 映射、可选 SearchProvider 及 BackendServices；后者接入通知服务和 `DB.AgentData()`。

可信调用方使用 `Run(ctx, ownerID, request)` 或 `Call(ctx, bridge.Request{Version:"v1", OwnerID:ownerID, Operation:"agent.run", Payload:...})`。OwnerID 不能来自模型或未验证请求体。调度通过 DispatchBridge 复用此 Port，父预算/祖先信息通过私有 Go context key 保留，不能从 JSON 伪造。

`agent_dispatch`支持统一Worker参数 `workflow:{workflow_id,version}` 与 `context.event`；工具检查同用户、active和版本及workflow.read权限后为子调用绑定可信上下文，父调用权限不变。Run记录保留版本快照，返回仍为run_id/output/error。无workflow的旧调用兼容。完整契约见 [DISPATCH.md](../../../agent/framework/DISPATCH.md)。

[example_test.go](example_test.go) 提供只编译、不连接真实服务的装配示例。完整 PostgreSQL + Mock Provider 验收位于 `dbbridge/agent_test.go` 的 `TestFrameworkAcceptancePersistedBridgeDispatch`。真实通知服务缺失时明确 NOT_IMPLEMENTED，Mock 收据不代表移动端交付。
