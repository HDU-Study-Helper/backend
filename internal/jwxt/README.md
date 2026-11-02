# 教务模块工作区

包名 `jwxt`，只在本目录开发。先读 `../../AGENTS.md` 与 `../../CONTRACTS.md`。

当前状态：仅接口约定，没有平台实现；不宣称真实认证或同步已完成。

第一批任务：实现 `contract.Adapter`，约定 `New(deps Dependencies) (contract.Adapter, error)`。优先授权/会话探测、课程与课表，后续考试安排；不实现外部数据修改。

- dependencies：注入连接绑定的 credential.Factory、带超时 HTTP 客户端和必要配置，不使用包级会话。
- 身份：所有读取按 Scope 的 user/connection 隔离，学校 SSO 的票据保留在加密会话内部。
- 分区：实现前在此补充每个 resource 的 partition_id 格式；统一约定不能遗漏学期/教学班标识。
- 数据：保留教学周、周几、节次和学期信息；未拿到学期起始日或上课节次时间时不猜具体时间。
- 必交：编译断言、脱敏 fixture、授权失效/分页/更新/超时测试、已验证能力表和测试命令。
- 入口：实现后由核心负责人在统一启动入口注册；本目录不注册 HTTP 路由。

能力表在实现后填写：认证方式、course/schedule/exam 各项的“真实验证 / Mock 通过 / 未支持”，以及账号级或课程级分区、增量限制。
