# internal 目录导览

本文说明当前源码实际职责，不将未来设计当成已经实现。Go 的 `internal/` 是导入范围限制：这些包只供本后端及其允许的父目录代码使用，不是对外发布的SDK。

## 1. 全部目录的职责与状态

| 目录 | 当前职责 | 状态 / 主要入口 |
|---|---|---|
| `app` | 读取环境配置、构造具体模块、注入Repository/加密工具、注册chi路由，启动并停止HTTP与采集Worker | 已实现；`config.go`、`app.go`、`router.go` |
| `users` | 应用用户CRUD、bcrypt密码、会话认证、管理员权限、CSRF、登录限流、初始管理员、嵌入管理页面 | 已实现；`service.go`、`routes.go`、`web.go`、`admin.html` |
| `httpkit` | HTTP公共工具：严格JSON请求解析、统一JSON响应/错误、可信用户上下文 | 已实现；`httpkit.go`；不负责登录或具体业务 |
| `contract` | 平台边界协议：`Adapter`、`Scope`、`Session`、`Record`、资源类型、平台依赖及可选讨论/认证生命周期接口 | 已实现；`platform.go`、`discussion.go`；不含SQL/HTTP路由 |
| `model` | 中央数据对象、业务存储接口和安全错误；用户、平台连接、归档、预览、回复者等对象 | 已实现；`model.go`、`platform.go`、`preview.go`、`course_preview.go`、`reply_users.go` |
| `dbbridge` | 唯一PostgreSQL实现：连接池、迁移、SQL、事务、用户/会话、DWS及学习平台Repository、租约、去重、预览统计 | 已实现；`db.go`、`migrate.go`、`users.go`、`dws.go`、`platform.go`、`preview.go`等 |
| `credential` | AES-256-GCM加解密，按用户/连接绑定；官方CLI工作区的安全打包/解包 | 已实现；`vault.go`、`archive.go`；不负责平台登录，不自行持有数据库 |
| `platform` | 学习通/MOOC/U校园的公共应用服务：授权、状态轮询、会话加密、资源同步、讨论课程分区、回复者按需归档和HTTP入口 | 已实现；`service.go`、`http.go`、`discussion.go`、`reply_users.go`、`import.go` |
| `chaoxing` | 学习通上游协议适配：二维码、Cookie会话、课程/作业/考试安排/进度/通知/讨论/资料目录及按需回复用户读取 | 已实现；`adapter.go`、`read.go`、`discussion.go`、`materials.go`、`discussion_reply_users.go`；具体覆盖见本目录README |
| `mooc` | 中国大学MOOC/SPOC上游协议适配：二维码、课程与期次、安全任务目录、明确源进度字段 | 已实现；`client.go`、`auth.go`、`sync.go`；公告未支持，真实覆盖见本目录README |
| `ucampus` | U校园AI上游协议适配：SSO二维码、WebSocket状态、票据/账户校验、门户课程和受限教材任务/进度 | 已实现；`adapter.go`、`qr.go`、`cookies.go`；内容端annotator要求见本目录README |
| `dws` | 当前钉钉实现：包装固定官方DWS CLI，隔离Profile/凭据工作区，设备授权、群消息轮询、最新状态upsert、上下文和附件读取 | 已实现；`service.go`、`runner.go`、`link.go`、`link_runner.go`、`messages.go`、`http.go` |
| `preview` | 面向管理页的只读预览API：连接/资源抓取概览、归档分页、学习通课程分组过滤 | 已实现；`http.go`；只读数据库，不发起上游抓取 |
| `bridge` | 将来模块间通信的进程内Port与版本化请求/响应协议 | 本包保留 `demo.echo`；`agent.Service` 另实现同一 Port 的 Agent 调度和后端服务接入，无消息总线或真实通知渠道 |
| `trigger` | 独立业务变更Port：事务提交后尽力投递安全引用和ChangedFields，默认Noop；覆盖除DWS message外的已接入业务采集变更 | 已实现端口；`trigger.go`；自定义Handler可订阅Created/Updated/Deleted，Deleted仅协议预留，当前不判断上游删除；无Agent、数据库trigger、outbox、队列或重试 |
| `jwxt` | 教务系统的工作区及后续开发约定 | 仅README，无Go实现；未接入运行服务 |
| `agent` | 通用框架的内部服务和 Bridge 适配 | 已实现；`service.go`；主体在 `../../../agent/framework`，无业务 Agent 或 HTTP 接线 |
| `handle` | 未来统一用例编排层的工作区 | 当前空目录；现阶段编排分别在`platform`、`users`、`dws`，尚未统一迁入handle |

`*_test.go` 为测试，不是运行入口。上述“已实现”表示存在代码并接入相应流程，不代表所有外部平台、所有账号和所有资源都经过真实验证。实际能力和限制以平台README与根`DATA-CATALOG.md`为准。

## 2. 最容易混淆的边界

### app 和 platform

- `app` 决定程序使用哪些具体模块：打开数据库、构造学习通适配器、把适配器和Repository交给platform、挂载路由。
- `platform` 执行共同用例：解密某连接Session、调用对应Adapter读一页、加密更新会话、提交记录与游标。
- 启动入口 `cmd/api/main.go` 调用app，不堆放平台协议或SQL。

### contract 和 model

- `contract` 描述“怎么调用一个外部平台”。例如 `Adapter.Sync(...)` 返回来源 `Record` 和敏感 `Session`。
- `model` 描述“中央系统存什么、如何通过存储接口读写”。例如 `PlatformRepository.Commit(...)` 和 `PlatformRecord`。
- 两者目前有少量相近概念，例如Scope与DWSScope。这是现阶段接口演进的实际状态，不代表已经统一成一套通用领域模型。

### platform 和各平台包

- `chaoxing/mooc/ucampus` 只理解各自上游请求、Cookie/签名、响应和分页。不写SQL、不注册中心HTTP路由、不自行启动轮询循环。
- `platform` 不应复制上游解析，而是使用注入的Adapter；它负责中央权限范围、持久化和采集调度。
- `platform/import.go` 当前为U校园合法会话导入直接引用ucampus，属于现有专用导入路径，不能误称整个协调层完全不依赖具体平台。

### dbbridge 和 credential

- `dbbridge` 负责把会话密文、游标、来源记录写进PostgreSQL，并保证事务和租约。
- `credential` 负责密文与明文之间的转换，以及CLI工作区的安全归档；它不直接运行SQL。
- 密钥由app配置并注入，不能随数据库备份一起当作普通业务字段返回。

### preview 和 platform/http.go

- `preview` 的GET只读取已归档数据，刷新预览不触发上游访问。
- `platform/http.go` 有登录、同步、断开和回复者抓取等实际操作入口。
- 页面“刷新回复用户”明确调用POST，会请求上游；普通“刷新数据库预览”不会。

### dws 的独立调用方式

钉钉相关代码和规范统一放在 `dws`。当前通过受控官方CLI子进程读取，因此保留专用服务、路由、Worker和存储，没有实现与三个HTTP平台完全相同的Adapter。后续扩展钉钉能力仍在本目录维护，不再新增第二个钉钉模块。

### trigger、bridge 和 agent

`trigger`是独立采集业务变更Port。dbbridge在事务提交后best effort调用Handler，默认`app.New`注入Noop；可通过`app.NewWithTrigger(ctx,cfg,handler)`接入自定义Handler，启动接线使用`dbbridge.DB.SetTriggerHandler(handler)`。覆盖学习平台采集业务与DWS附件，排除DWS message，以及用户/会话/连接授权/凭据/workspace/游标/租约/jobs/抓取状态等管理数据。Handler可按Created/Updated/Deleted协议操作订阅；Deleted仅预留，当前不判断上游删除也不发出删除事件。payload只含安全业务引用、时间和ChangedFields字段名，不含credentials或历史正文。无数据库trigger、outbox、队列、重试或实际Agent，失败不回滚已提交数据。详见[trigger/README.md](trigger/README.md)。

### bridge 和 agent 的内部接入

`agent.Service` 已实现 `bridge.Port` 的 `agent.run`，支持受控同步跨 Agent 调度。框架工具经 BackendBridge 调用通知/数据服务；dbbridge 提供按当前用户和 active 平台连接限定的只读数据投影，不暴露任意 SQL 或凭据。当前 HTTP App 仍使用 Demo echo，不自动触发 Agent。详见 [框架说明](../../agent/framework/README.md)。

## 3. 当前运行时调用关系

```text
cmd/api/main.go
  └─ app：配置、组装、生命周期
      ├─ users：认证/用户管理/后台HTML
      ├─ preview：只读归档与统计 ─────→ model.PreviewRepository
      ├─ platform：学习平台编排 ─────→ model.PlatformRepository
      │   ├─ contract.Adapter
      │   │   ├─ chaoxing ─→ 学习通HTTP
      │   │   ├─ mooc ─────→ MOOC HTTP
      │   │   └─ ucampus ──→ U校园HTTP/WebSocket
      │   └─ credential：会话/游标加解密
      ├─ dws：独立钉钉服务 ─────────→ model.DWSRepository
      │   ├─ 官方CLI子进程
      │   └─ credential：CLI工作区加解密
      └─ bridge.Demo：仅echo

model中的Repository接口
  └─ dbbridge实现 ─→ PostgreSQL
```

dbbridge对采集业务的成功提交还会调用`trigger.Handler`，默认`Noop`，DWS消息不投递。业务记录只保留最新内容，v4迁移按observed_at/id保留旧数据最新行；内容变化原行更新，预览按稳定ID分页，不是change feed。`record_count/entity_count`兼容保留，`observed_at`不随同内容成功重扫更新，最近一页成功应查`last_success_at`。回复用户仍为曾观察名单，不自动移除缺失者。

这是同一Go进程内的函数/接口调用。浏览器通过chi HTTP API访问后台；服务端模块之间不通过本机HTTP互调。DWS自身的官方二进制作为受控子进程运行。

## 4. 一条学习通讨论如何到页面

1. app创建学习通Adapter、Vault、dbbridge Repository，注入platform服务。
2. platform在用户连接范围内恢复会话，通过chaoxing读取某课程讨论一页。
3. chaoxing解析真实入口和班级tags，返回白名单Record，不返回Cookie到普通数据。
4. platform计算内容哈希、加密更新会话与游标，调用dbbridge事务提交。
5. dbbridge按逻辑对象upsert到platform_records：相同content/hash不写，变更更新原行、保留稳定ID，A→B→A仍更新；同事务保存独立课程checkpoint，记录抓取状态。提交后向trigger尽力投递新增/更新引用，默认Noop。
6. 浏览器通过preview查询，按course_id+class_id选择课程并查看JSON。
7. 查看回复用户时先读已归档名单；点击抓取按钮才经platform调用chaoxing的可选读取接口，保存身份白名单，不保存回复正文或附件。

## 5. 改功能时该去哪里

| 要做的事 | 首先检查 |
|---|---|
| 改环境变量、注册平台、HTTP地址、优雅停机 | app；入口cmd/api |
| 改登录、权限、管理员或管理页交互 | users；公共JSON逻辑在httpkit |
| 改二维码/平台响应解析、上游参数或资源字段 | 对应chaoxing/mooc/ucampus；钉钉为dws |
| 改轮询、加密提交、讨论课程调度 | platform；钉钉调度在dws |
| 新增资源接口或可选平台能力 | contract，然后修所有调用方和实现 |
| 新增中央对象、Repository方法 | model，然后dbbridge及调用服务 |
| 新增表、字段、索引或SQL查询 | dbbridge，新增迁移，不改已应用历史迁移 |
| 改密文格式、Cookie会话加密、CLI归档安全 | credential |
| 改统计和课程过滤查询 | dbbridge + preview；页面在users/admin.html |
| 订阅采集业务变更 | trigger.Handler；app.NewWithTrigger注入；dbbridge提交后投递，默认Noop |
| 加 Agent 工具、跨模块查询通信 | `../agent/framework` 的 ToolRegistry / Runtime；`internal/agent` 的 Bridge 适配；SQL 仍在 dbbridge |

## 6. 文档索引与维护规则

- 根 `README.md`：启动、配置、功能状态与API用法。
- 根 `DATA-CATALOG.md`：**当前**数据库表结构、采集字段、课程关联和读取入口。
- 根 `ARCHITECTURE.md`：当前运行关系及后续架构目标，须区分两者。
- 根 `CONTRACTS.md` / `DATA-MODELS.md`：包含完整目标草案，不能据此认定已有实现。
- 各平台README：上游协议依据、能力和真实验证限制。
- 本文：目录职责、状态、依赖和修改入口。

新增目录必须更新本表；新增功能应更新所属模块说明及相关数据字典。空目录不算已实现模块，Mock通过不算真实平台验收。现有部分目录尚无独立README，本文提供总入口，不能把“有目录”当作“已有服务”。
