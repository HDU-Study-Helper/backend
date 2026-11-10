# bridge v0.1

同一 Go 进程通过 `Port.Call(ctx, Request)` 依赖注入通信，不提供全局总线。Request 包含协议 Version、可信 OwnerID、Operation 和小型 JSON Payload；Reply 包含 Version、Operation、Payload。OwnerID 由调用应用层绑定，不能从 HTTP body 或模型参数选取。

当前只有 `Demo` 支持 `v1/demo.echo`，最多4KiB有效JSON，检查取消并复制输出。app 将 `POST /api/v1/bridge/demo` 接入认证/CSRF，HTTP body 为 `{payload:{...}}`。它只是可运行占位，不会启动 Agent、发钉钉消息或推送通知。

未来模块可实现独立 Port 或注入更具体的业务接口；异步需求先考虑 PostgreSQL durable job。本版 DWS jobs 在 dbbridge 中单独管理，不把Demo扩展为企业消息总线。`go test ./internal/bridge` 覆盖协议/身份要求/取消/unsupported。

## Agent 内部 Port 实现

`internal/agent.Service` 实现本接口的 `v1/agent.run`；`DispatchBridge` 通过同进程 Port.Call 将可信 parent ExecutionContext 保留在 Go context 中，目标 Runtime 重新检查 owner、权限、循环和预算。`BackendBridge` 支持 notification.send / data.catalog / data.query，宿主通过 BackendServices 注入服务。现有 Demo 及 HTTP 路由不变；不新增本机 HTTP、RPC 或消息总线。详见 [框架说明](../../../agent/framework/README.md)。

## 日程内部Port

internal/schedule.Service复用Port提供schedule.create/query/get/update/delete；app装配后为非日程操作保留Demo回退。OwnerID由可信后端调用者绑定，创建来源固定AGENT；HTTP手动创建固定USER，不允许payload伪造用户/来源/mod。没有新增任意HTTP Bridge网关或Agent Runtime。见../schedule/README.md。
