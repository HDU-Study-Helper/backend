# Backend 开发协作约定

先读 `../PROJECT.md`、本目录 README / ARCHITECTURE / CONTRACTS，再读当前任务涉及的实现。当前已按用户要求实现 v0.1 模块化单体：dbbridge 统一 Repository、users 认证和管理页、dws 官方 CLI 包装与受控轮询、bridge 进程内 Demo，以及 chaoxing/mooc/ucampus 的 contract.Adapter 和 platform 统一授权/轮询/存储。详细已实现/未验证/未实现状态以根 README 及各模块 README 为准；其余业务接口仍为草案。仅规范任务只改文档；未经用户要求不得扩展功能。不得把接口约定或 Mock 通过视为真实平台功能已验证。

## 文件所有权

| 角色 | 默认可修改范围 |
|---|---|
| 教务负责人 | internal/jwxt/** |
| 钉钉负责人 | internal/dws/** |
| 学习通负责人 | internal/chaoxing/** |
| U 校园负责人 | internal/ucampus/** |
| 中国大学 MOOC 负责人 | internal/mooc/** |
| Agent 负责人 | internal/agent/**；对共享类型仅提建议 |
| 后端核心负责人 | contract、handle、credential、后续存储/工具网关/HTTP/Worker/启动入口与根配置 |

只处理已分配 Issue，避免并行 Agent 编辑同一文件。新增外部依赖需要在 PR 说明，go.mod/go.sum 由核心负责人统一合并；任务开发可以提交所需依赖变更供审核。跨目录改动与共享 Schema 修改须负责人审核，不能直接重命名字段破坏其他模块。

## 平台开发规则

- 对外实现 `contract.Adapter`，有需要时实现可选 FileReader；构造函数返回接口，内部客户端实现留在自己的包内。
- 不导入 handle / agent / 其他平台包；不直接操作中心数据库、HTTP 路由、队列或通知。
- 认证只通过受限 credential 端口存储；不把 Token 当普通 metadata 暴露。
- 不启动隐式后台循环；由 Worker 调度 Sync。不会的资源返回 unsupported，不能返回伪造数据。
- 测试使用脱敏 fixture，不在默认测试执行中访问真实账号或上游网络。
- README 必须注明实际能力、未实现项、分区/分页约定和验证状态。

## 验收与协作

每个 Issue 写明：负责目录、输入/输出契约、前置依赖、脱敏测试样例、验收命令、已知限制。先交付一个完整读取纵切面，再扩展资源。共享变更 PR 必须搜索调用方并在同一提交修正，说明兼容性。

运行 `gofmt`、`go test ./...`、`go vet ./...`；测试未覆盖实际平台时要明确说明。文档与接口变更同时更新，不得以静态回复替代真实 Tool Calling，不得将推断自动保存为已确认事实。
