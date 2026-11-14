# HDU-Study-Helper Backend v0.1

Go 1.24 + chi + PostgreSQL 模块化单体。产品范围见 `../PROJECT.md`；本版实现用户管理、DWS，以及学习通/MOOC/U校园适配器和中央采集存储，另提供随 App 初始化的通用 Agent Framework 和临时 Agent 管理后台；具体业务 Agent 和真实主动通知渠道仍未实现。复用 chi、pgx、bcrypt；使用 x/net/html 用于 HTML 解析、coder/websocket 用于 U校园官方 SSO、go-qrcode 用于扫码 URI 渲染。

`internal` 各目录职责、实现状态、依赖和修改入口见 [internal/README.md](internal/README.md)。

数据库表结构、各平台实际输出字段、课程关联和读取入口汇总见 [DATA-CATALOG.md](DATA-CATALOG.md)。该文件描述当前实现；DATA-MODELS.md 保留未来完整契约草案。

## 实现和验证状态

| 模块 | 已实现 | 验证范围 |
|---|---|---|
| db-bridge (`internal/dbbridge`) | 集中连接、参数化 SQL、事务迁移账本、User/DWS Repository；租约 fencing、业务最新状态增量upsert与原子水位提交 | 真实独立 PostgreSQL 集成测试，包括回滚、重试去重、跨用户拒绝、撤权及旧任务不能提交 |
| 用户管理 (`internal/users`) | 用户增删查改、bcrypt、登录/登出、修改密码、管理员权限、最后管理员保护、会话撤销、CSRF、登录限流、中文 Web 管理页 | 单元测试、race 检查及真实 HTTP+PostgreSQL 冒烟；浏览器视觉和交互尚未人工验证 |
| DWS (`internal/dws`) | 共享固定官方二进制、每连接 Profile/Token 加密工作区、显式授权群、持久化任务、定时轮询、固定时间窗分页、按对象最新状态去重更新、规则分类、同群上下文、附件读取/归档 | 官方 help/本地 schema/源码和脱敏 fake executor；真实 HTTP 验证连接创建/权限。没有使用真实账号读取，真实群/附件/双账号刷新恢复尚未验证 |
| Agent Framework (`../agent/framework` + `internal/agent`) | 通用 Runtime、20 个工具、内部 Bridge 调度、Memory V0.2 三种归属/关联、Workflow 版本和 V9 迁移 | Mock Provider + 真实 PostgreSQL 与浏览器验收；含临时管理页，不含业务 Agent；[详细说明](../agent/framework/README.md) |
| bridge (`internal/bridge`) | v1 请求/响应、进程内 Port 接口、认证 Demo echo | 单元及 HTTP 测试；Agent Port 实现位于 internal/agent，无消息总线 |

DWS 不是全部钉钉能力已验证的承诺；官方调研记录群历史可能受服务端字段映射或上游权限影响。错误会明确返回，不用伪造数据替代。详细范围、协议差异、限制见 `internal/dws/README.md`。

## 目录与边界

```text
cmd/api/main.go           启动、信号、运维导入子命令
internal/app/            配置、依赖组装、chi 路由、HTTP/轮询生命周期
internal/model/          数据对象、Repository 接口、安全错误
internal/dbbridge/       唯一 PostgreSQL/SQL/事务实现、版本迁移
internal/users/          用户服务和嵌入管理页
internal/credential/     AES-256-GCM、按用户/连接绑定的工作区归档
internal/dws/            官方 CLI Runner、采集和归档服务
internal/bridge/         进程内通信契约和 Demo
internal/trigger/        采集业务变更 Port，默认 Noop、提交后尽力投递
internal/httpkit/        JSON/错误/可信 Actor
scripts/smoke.py         隔离数据库上的真实 HTTP 冒烟
```

依赖方向为 `app → users/dws/bridge → model`，`dbbridge → model + pgx`，`dws → credential + 官方 CLI`。业务模块不直接操作 PostgreSQL。chaoxing/mooc/ucampus/jwxt 通过 `contract.Adapter` 暴露只读登录与同步端口，`internal/platform` 管理加密凭据、chi API 和轮询，`dbbridge` 独占 SQL。handle、完整学业状态和 Message 服务仍未实现；通用 Agent 通过内部 Go 服务显式装配，尚无具体业务 Agent。

## 启动

需要 Go 1.24+、Docker Compose；DWS 功能需要 Linux、固定版本官方 CLI 和 tmpfs。无 Docker 权限的当前账号使用 `sudo docker compose`，Go 服务使用普通账号运行。

```bash
# 首次配置；已有 .env 不要覆盖。
cp -n .env.example .env
chmod 600 .env
# 编辑 .env，更换 POSTGRES_PASSWORD 并同步 DATABASE_URL 中的密码。
sudo docker compose up -d --wait postgres

set -a
. ./.env
set +a
# 首次启动明确创建管理员，无内置默认密码；密码 12–72 字节。
export BOOTSTRAP_ADMIN_USERNAME=admin
read -r -s -p '初始管理员密码: ' BOOTSTRAP_ADMIN_PASSWORD
printf '\n'
export BOOTSTRAP_ADMIN_PASSWORD

go run ./cmd/api
```

打开 `http://127.0.0.1:2610/admin` 登录管理用户。`GET /api/v1/health` 返回 `{"status":"ok"}`，它是存活检查。服务启动会通过 dbbridge 显式迁移并验证数据库连接。已有任何用户时 Bootstrap 不会增加管理员或覆盖密码；后续启动移除 Bootstrap 环境变量即可。

初次迁移创建 users、sessions、DWS 连接/群/租约/水位/消息当前记录/附件/任务及迁移账本，消息与水位同事务提交。PostgreSQL 仅绑定 `127.0.0.1:5432`；卷保存数据。停止服务用 Ctrl+C，停止数据库用 `sudo docker compose down`，不带 `-v` 保留数据。

`.env` 已被忽略；API 只读进程环境，不自动加载文件。Go module 暂用 `academic/backend`，正式仓库路径确定后统一改名。

## 主要配置

| 变量 | 说明 |
|---|---|
| `DATABASE_URL` | 必填；本地可用 sslmode=disable，远程按部署要求启用 TLS |
| `HTTP_ADDR` | 默认 `:2610` |
| `API_ONLY` | 默认 false；true 时保留完整 HTTP、平台授权及显式手动操作，不启动 Agent 批次执行器或全局信息刷新时钟，可与已有后台任务实例共用数据库 |
| `PUBLIC_ORIGIN` | 可选固定浏览器 Origin；反向代理必须设真实外部 Origin，无路径/末尾斜杠 |
| `COOKIE_SECURE` | HTTPS 必须 true；本地 HTTP false |
| `BOOTSTRAP_ADMIN_USERNAME/PASSWORD` | 首次创建管理员，必须同时提供；不重置已有账号 |
| `DWS_BINARY` | 官方 CLI 的固定绝对路径；空则关闭 DWS，其他 API 可独立运行 |
| `DWS_BINARY_SHA256` | 固定二进制的完整小写 SHA-256 |
| `CREDENTIAL_KEY` | base64 编码的随机32字节 AES 密钥，启用 DWS 必填，须长期稳定并单独备份 |
| `DWS_WORKSPACE_ROOT` | 默认 `/dev/shm`，必须 tmpfs；不回退普通磁盘 |
| `DWS_POLL_INTERVAL` | 默认 1m，环境配置最小5s |
| `DWS_COMMAND_TIMEOUT` | 默认45s；CLI 最长1m |

密钥可用 `openssl rand -base64 32` 生成。密钥丢失无法恢复已归档凭据，更换密钥须迁移旧密文；本版无自动轮换。临时明文仅在私有 tmpfs 目录中使用，操作结束回收，崩溃目录需运维清理；生产配置 tmpfs/swap 容量和进程权限。目录隔离不是同 OS 用户下的进程沙箱。数据库备份同时含私人群消息，应按部署访问控制保护。

## 用户 API

统一前缀 `/api/v1`。JSON 写请求须 `Content-Type: application/json`；登录须同源 `Origin`，登录后写请求还须 cookie `academic_csrf` 对应的 `X-CSRF-Token`。会话 cookie 为 HttpOnly、SameSite=Strict，数据库仅保存随机会话 token 的 SHA-256。每请求读取最新用户角色/禁用状态。

| 方法 | 路径 | 权限/输入 |
|---|---|---|
| POST | `/auth/login` | `{username,password}` → 用户及 Cookie |
| GET | `/auth/me` | 当前登录用户 |
| POST | `/auth/logout` | 当前会话，204 |
| POST | `/auth/password` | `{current_password,password}`，撤销所有会话，204 |
| GET/POST | `/users` | admin；列表 limit 默认20、最大100；创建 username/display_name/password/role/disabled |
| GET/PUT/DELETE | `/users/{id}` | admin；更新 display_name/password/role/disabled，用户名不可改 |
| POST | `/bridge/demo` | 当前用户；`{payload:{任意小型JSON}}` → v1 demo.echo |

用户名3–32位 ASCII 字母数字及 `_.-`，首位字母数字；显示名1–80字；密码12–72字节；role 为 admin/user。禁用、角色或密码变更撤销会话。不能删除、禁用或降级最后可用管理员。无公开注册、密码找回、邮箱验证或 OAuth。

## DWS 首次连接

1. 配置 DWS_BINARY、SHA256 和 CREDENTIAL_KEY 后启动 API。
2. 当前用户在后台点击 Link to DWS（`POST /api/v1/dws/link`，无需请求体），打开返回的官方设备授权链接。企业 ID、用户 ID 与 Profile 由官方登录结果自动识别，并与加密凭据一起原子保存，无需用户填写。
3. 打开 `/admin#dwsGroupsPanel` 的“DWS 采集群配置”，选择已授权连接，点击“读取我的群”，勾选需要采集的群，点击“保存采集群”。群名称和 CID 自动读取，无需手动填写。对应 `PUT /api/v1/dws/connections/{id}/groups`，每次整体替换白名单，最多100群；空列表停止采集但保留归档。读取接口为 `GET /api/v1/dws/connections/{id}/available-groups?cursor=...`，分页返回已加入的群；读取本身不修改白名单。
4. 连接已授权且修改已保存后，可点击每行“手动试采”，面板跟踪排队、运行、成功或失败；202 仅表示排队。统一计划任务默认每十分钟采集白名单，间隔、暂停及统计在 `/admin/tasks` 管理。授权期间先不要替换群列表（替换会失效当前租约）。也可由运维使用下面的隔离工作区授权并离线导入；HTTP 不接受服务器目录或用户明文 Token。

```bash
# 普通服务账号执行；DWS_BINARY 已设为固定绝对路径。
DWS_AUTH_ROOT=$(mktemp -d /dev/shm/dws-auth.XXXXXX)
chmod 700 "$DWS_AUTH_ROOT"
mkdir -p "$DWS_AUTH_ROOT"/{home,config,keychain,tmp,xdg/config,xdg/cache,xdg/data}
chmod -R 700 "$DWS_AUTH_ROOT"
(
  cd "$DWS_AUTH_ROOT" || exit 1
  env -i PATH=/usr/bin:/bin LANG=C.UTF-8 \
    HOME="$DWS_AUTH_ROOT/home" DWS_CONFIG_DIR="$DWS_AUTH_ROOT/config" \
    DWS_KEYCHAIN_DIR="$DWS_AUTH_ROOT/keychain" TMPDIR="$DWS_AUTH_ROOT/tmp" \
    XDG_CONFIG_HOME="$DWS_AUTH_ROOT/xdg/config" XDG_CACHE_HOME="$DWS_AUTH_ROOT/xdg/cache" \
    XDG_DATA_HOME="$DWS_AUTH_ROOT/xdg/data" \
    "$DWS_BINARY" auth login --device --no-browser -f json
)
# 用已创建连接的真实应用用户ID和连接ID替换；保持同一DATABASE_URL/CREDENTIAL_KEY。
go run ./cmd/api dws-import --owner '<应用用户ID>' --connection '<连接ID>' --workspace "$DWS_AUTH_ROOT"
# 成功导入后清理原授权工作区；导入失败可保留片刻用于核对后重试。
rm -rf -- "$DWS_AUTH_ROOT"
```

后台 `/admin` 已提供 **Link to DWS**：点击按钮直接由 API 启动官方设备授权，无需填写企业 ID 或用户 ID，展示授权链接/验证码并轮询结果。也可继续使用上述运维离线导入。设备授权由官方 CLI 完成；不要将该终端输出写入业务日志。CLI 的 Token 刷新继续由官方实现。授权导入验证精确 profile/企业/用户身份，工作区加密绑定应用 user+connection；不同用户不共享 current profile。重启从 PostgreSQL 恢复采集，学校统一采集账号模式仅保留 mode 扩展点，当前返回 unsupported，不共享私人消息。

首次绑定 API 为 `POST /api/v1/dws/link`（202，无请求体）；已有连接重新授权为 `POST /api/v1/dws/connections/{id}/link`，进度为 `GET /api/v1/dws/connections/{id}/link/{challenge}`，取消使用同路径 DELETE。均须登录，写操作须 CSRF。挑战按当前用户和连接隔离，只有 pending 状态返回官方授权链接和验证码；服务重启后挑战失效，连接凭据则在授权成功后加密持久化。首次绑定采纳官方返回且经本地会话再次核验的身份；已有连接重新授权时拒绝更换为其他身份。

HTTP 同步入口为 `POST /api/v1/dws/connections/{id}/sync`，body `{group_id}` 返回202任务，按连接内 `/jobs/{id}` 查询。消息列表、上下文及附件路由详见 `internal/dws/README.md`。轮询采用完成水位+5分钟重叠窗口，不保证旧消息编辑/撤回的永久 changefeed。单窗口最多100页、10000条、16MiB；超预算明确失败，不推进水位。附件最多8MiB，首次归档当前观察并计算 SHA-256，不声称它就是历史消息发布时的原版；无 PDF/Office 内容解析或 OCR。

本版 DWS 使用专用路由而非未来统一 `/connections`；`after` 中心 ID 分页尚未改成签名 cursor，创建/同步尚无通用 Idempotency-Key 重放协议，消息按用户、连接、群和原生ID定位当前对象，相同内容不写入，变化时更新content/content_hash并保留中心ID。没有完整消息总线、通知投递、Agent分析或完整学业状态实现。

## 杭电教务（手动采集）

后台 `/admin` 学习平台区域新增统一认证账号密码登录，成功后将账号和 Cookie 会话通过已有 CREDENTIAL_KEY 加密保存在数据库；密码仅用于本次登录，不保存、不自动重登录。教务默认关闭定时采集，本版提供手动获取，无定时开关：登录后选择学年起始年份/学期，点击“获取课表”或“获取已选课程”，数据进入 platform_records 与已有预览窗口；课表还会自动展开并刷新 schedules。请求失败保留旧记录和 checkpoint；会话过期需重新登录。

- 登录：POST `/api/v1/platforms/jwxt/login`，JSON `{username,password}`，返回201连接信息。
- 获取：POST `/api/v1/platforms/connections/{id}/sync`；课表请求需 resource=schedule、academic_year、term 及 calendar（首周周一日期和节次起止时间），一次收齐完整快照并事务刷新课表日程；已选课程 resource=course 仍逐页提交。学期1/2映射源端3/12。配置格式见 [日程模块](internal/schedule/README.md)。
- 查看：GET 同连接 `/records?resource=schedule` 或 `/records?resource=course`；数据预览来源选择“杭电教务（手动采集）”。预览10秒自动刷新仍只读数据库。

**验证范围：教务协议仍仅离线模拟，未访问真实教务或提交真实账号；日程模块已用独立PostgreSQL测试库验证迁移、CRUD和同步回滚。** 迁移v7允许 jwxt 来源，v8新增 schedules，启动时由既有迁移流程执行。课表保留周次/节次原文，不猜具体作息；选课结果接口依据参考实现，杭电兼容性待验收。详见 [jwxt/README](internal/jwxt/README.md)。

## 学习通 / MOOC / U校园

后台 `/admin` 新增“学习平台连接”：点击平台按钮获取二维码，使用对应官方 App 扫码并确认。仅当前应用用户可读取二维码、连接与记录。二维码与扫码状态属于临时挑战，仅由页面顺序轮询，后台采集 Worker 不抢占待登录连接；U校园依赖活 WebSocket，重启或断开后需重新扫码。成功后凭据与游标使用统一 CREDENTIAL_KEY 加密，后台启动时扫描已登录连接，此后每轮完成后等待10分钟，对已登录且启用的非教务平台的已声明资源读取一页并事务保存；无真实增量的平台采用全量重扫与内容哈希比较，每个对象仅保留最新content/hash；相同内容不写入，变化时原行更新并保留稳定中心ID，A→B→A也会更新。长分页需要多个轮询周期，失败不推进 checkpoint。界面可手动同步一页、查看已归档原始记录。

| 平台 | 本次实现 | 限制及验证 |
|---|---|---|
| 学习通 | 官方二维码图片、状态轮询；课程、逐课程作业/讨论话题、账号考试安排、任务点进度、通知分页 | 依据现有调研及脱敏 HTTP 测试；本 Go 模块真实账号未验证；考试仅读安排，不进入试卷；进度是任务点汇总 |
| 中国大学 MOOC | 官方二维码图片与确认后换 Cookie；MOOC/SPOC 课程、作业/考试安全目录、源端明确进度字段 | 公告未支持；SPOC源期次映射有已知限制；没有调用开卷或计时接口；本 Go 模块真实扫码和账号采集未验证 |
| U校园 AI | 官方扫码 URI、WebSocket 状态、票据转换/账户校验、门户课程；合法已授权会话导入后的教材任务与进度 | 匿名二维码/WS握手真实验证通过；扫码成功链仅Mock；二维码会话没有内容端 annotator，教材任务/进度需额外合法会话，缺少返回unsupported；独立教师作业、考试和通知未支持；不生成签名Token |

共有 API 前缀 `/api/v1/platforms`，需已登录用户，写操作沿用 Origin + CSRF：

| 方法 | 路径 | 行为 |
|---|---|---|
| GET | `/sources` | 实际能力与limitations |
| GET | `/connections` | 当前用户连接列表 |
| POST | `/{source}/auth` | source=chaoxing/mooc/ucampus；返回连接、二维码图片及过期时间 |
| GET | `/connections/{id}/auth` | 轮询上游状态，保存私有会话；2秒后再请求，U校园可能长轮询35秒 |
| GET | `/connections/{id}/qr` | 当前用户有效二维码PNG/JPEG（挑战仅内存，重启需新建） |
| DELETE | `/connections/{id}` | 断开并失效租约/删除凭据，保留已有当前记录 |
| POST | `/connections/{id}/sync` | `{resource:course/assignment/exam/progress/notice/discussion}`，只同步一页；不支持返回422 |
| GET | `/connections/{id}/records?resource=course&after=0&limit=20` | 原始观察与 next_after，用户隔离 |

数据库新增版本2：platform_connections、platform_checkpoints、platform_records；迁移从已有v1向前升级，不改写旧版迁移。凭据不出现在普通记录或API响应中；禁用/删除用户后不能提交旧租约，重新授权创建不同连接避免混入另一个平台账号的历史。平台返回的原始文本只作证据，不自动成为已确认任务。

U校园合法内容会话仅通过运维导入，不提供普通网页Token输入：`go run ./cmd/api ucampus-import --owner '<应用用户ID>' < /受保护位置/session.json`。输入格式见 `internal/ucampus/README.md`，输入从stdin读取并在官方账号接口验证，内容不得提交到Git。没有合法annotator时不要声称已覆盖作业/进度。

详细协议、复合ID、分页与每项未验证状态见三个模块 README。新增依赖均已锁定 go.mod/go.sum。没有自动做题、提交、刷进度、启动考试、消息推送或Agent分析。

## 数据预览与抓取情况

登录 `/admin` 后可使用“数据预览与抓取情况”窗口：按 DWS/学习通/MOOC/U校园、连接、资源筛选，查看记录数与逻辑对象数、当前内容观察/最近成功/尝试时间、错误码、耗时、上一页条数和是否待续页。记录支持分页、完整 JSON、字段路径与类型树以及复制。支持手动刷新或可选10秒自动刷新，默认关闭；刷新只读数据库，不发起上游请求。

新增只读 API（均需会话认证）：`GET /api/v1/preview/overview`、`GET /api/v1/preview/records?source=ucampus&connection_id=<id>&resource=course&after=0&limit=20`。limit最大50，返回items和next_after；DWS resource为message，附件只展示已归档消息中的元数据，不读取二进制。所有查询限制当前登录用户，不允许通过参数指定其他用户。

学习通额外支持课程视图：`GET /api/v1/preview/courses?source=chaoxing&connection_id=<id>` 返回按 `course_id+class_id` 组合的课程、各资源归档数量和未关联记录数。记录接口可带 `course_key=<返回的key>`；`resource` 留空可同时查看同课课程/作业/进度等所有资源，`course_key=__unassociated__` 查看缺少明确课程标识的账号级考试/通知。学习通 `discussion` 已接入相同轮询、加密分页游标及归档流程，话题按 course_id+class_id 归类；正文中即使包含作业要求，也保持讨论来源类型，不自动冒充已确认作业。当前不采集回复正文和上传文件。讨论轮询按课程独立保存 `discussion:<课程组合键>` checkpoint，一门课失败保留其错误并继续其他课；预览资源仍归为 discussion。定向同步可 POST `/api/v1/platforms/connections/{id}/sync` body `{resource:"discussion",course_id:"...",class_id:"..."}`，首次读取会重新校验课程归属。已用正式接口将工科数学分析目标课程11条话题入库并验证课程过滤。课程筛选仅影响归档预览，采集仍按资源分页；不按名称推断考试/通知所属课程，不将不同教学班混合。界面抓取状态表仍是连接整体统计。

学习通新增 `material` 课程资料目录采集，加入现有自动轮询。仅保存文件夹与文件的名称、类型、父目录、路径、课程/教学班及源端明确的大小/作者/时间文案；不下载文件，不读取资料正文。沿用 `platform_records` 当前状态入库、去重更新、加密分页和课程归档预览，无需数据库迁移。手动同步 POST `/api/v1/platforms/connections/{id}/sync` body `{"resource":"material"}`（一次一目录页）；GET 同连接 `/records?resource=material` 可读取已归档记录。目录分页/层级遍历已用脱敏 fixture 验证，解析兼容本地历史资料页；新实现真实续页和子目录请求尚未验证。字段和限额见 [学习通说明](internal/chaoxing/README.md#课程资料目录)。

讨论详情新增“回复用户”面板，自动读取已归档名单；点击“刷新回复用户”才请求上游。接口：GET `/api/v1/platforms/connections/{id}/discussion/{topic}/reply-users?course_id=...&class_id=...`；POST同路径body `{course_id,class_id}` 抓取并归档。仅保存回复ID、话题ID、课程/班级、显示姓名、平台用户ID及明确匿名标识，不采集回复正文、附件、学号或联系方式。覆盖范围为顶层回复（top_level_only），不声称楼中楼全部参与者；匿名用户不反查身份。多个回复按平台用户ID和显示名聚合计数，历史删除不做自动撤回，因此名单是已观察参与者。kind为discussion_reply_user，按话题独立checkpoint，不加入自动全课程轮询。已真实验证工科数学分析话题“上传习题2.4”取得62条顶层回复观察。

数据库版本3新增 `platform_sync_status`，在资源同步前后记录状态，更新受连接租约fencing保护。历史同步可能没有尝试时间、耗时和本页条数；已有checkpoint时间用作历史最近成功依据。一次成功只表示一页提交，不等于完成全量；空页成功也有成功时间。连接 updated_at 不代表抓取成功。DWS使用已有任务与归档统计，不伪造单页耗时或续页情况。

数据库迁移v4将platform_records与dws_messages改为逻辑对象唯一键；旧多版本按observed_at、id降序保留最新行并删除其余行，DWS旧附件合并到最新消息，优先保留最新行已有的同ID附件。每对象只保存当前内容，不提供多版本历史。`observed_at`表示首次入库或最近内容变化时的观察时间，相同内容重扫不更新；`last_success_at`仍表示最近一页成功，空页成功也会更新。

预览JSON字段`content_hash`及完整JSON/字段树保持兼容，`record_count`与`entity_count`兼容保留且可能相等。列表仍以稳定中心ID做`after=id`分页，内容更新可能不改变列表顺序，已越过的ID不会因更新重新出现；预览不是change feed，应刷新或回到首批查看当前内容。未在本次采集出现的对象不自动删除，回复名单仍表示曾观察到的参与者。

## 业务变更trigger端口

新增独立[internal/trigger](internal/trigger/README.md)进程内Port，事务提交后best effort投递已接入采集业务的Created/Updated变更，包含学习平台各资源、按需回复用户和DWS附件，排除DWS message。用户、会话、连接授权、凭据/workspace、游标、租约、jobs及抓取状态等管理数据不触发。payload只含安全业务引用、操作、时间与ChangedFields字段名，不包含credentials或历史正文。

默认`app.New`使用Noop；可通过`app.NewWithTrigger(ctx,cfg,handler)`注入自定义Handler，启动接线使用`dbbridge.DB.SetTriggerHandler(handler)`。Handler可按Created/Updated/Deleted协议操作订阅；Deleted仅协议预留，当前不判断上游删除、不发出删除事件。当前无实际Agent、数据库trigger、outbox、持久队列或重试；提交后调用失败不回滚业务数据，不保证可靠投递。

## 验收

```bash
gofmt -w cmd internal
go test ./...
go vet ./...
go build -o bin/api ./cmd/api
go test -race ./...
```

默认测试不访问数据库或真实教学平台。数据库集成仅在设置 `TEST_DATABASE_URL` 时启用，每个用例创建并清理独立随机 schema。必须使用单独测试数据库，不要指向生产环境：

```bash
sudo docker compose exec -T postgres createdb -U academic academic_test
export TEST_DATABASE_URL='postgres://academic:<本地密码>@127.0.0.1:5432/academic_test?sslmode=disable'
go test -count=1 -v ./internal/dbbridge
```

HTTP 冒烟需要全新可丢弃数据库，会保留测试管理员记录；运行前先编译 bin/api。设置 `SMOKE_DATABASE_URL` 后运行 `python3 scripts/smoke.py`；可选 `SMOKE_DWS_BINARY=/绝对路径/dws` 以验证官方二进制哈希检查与 DWS 路由隔离，不执行真实账号命令。

本次实际通过基础编译/测试/vet、真实 PostgreSQL Repository 集成及 HTTP 冒烟：管理页可服务、登录/用户CRUD/RBAC/CSRF/会话撤销/最后管理员/bridge/DWS跨用户拒绝/优雅退出。真实平台账号、双账号同时刷新、真实附件、浏览器视觉交互尚未验证。

## 单次完整采集实验

先停止正常API进程，加载现有部署环境后运行 `go run ./cmd/collect-once -output ../.build/collection-once-report.json -timeout 30m`。该入口只创建一个刷新轮次，不启动HTTP、周期时钟或Agent处理。已启用且active的学习平台连接逐资源读到 `HasMore=false`，讨论按课程独立续页；每页失败不重试，验证、限流或认证错误停止该连接，已提交页保留。单流最多10000页、总计20000页及30分钟硬上限，预算耗尽标记失败，不声称完整。教务仍只手动、不改日程；回复用户名单等按需资源不扩展采集。DWS复用已有完整窗口轮询。退出前将结果、轮次与待处理批次持久化，报告仅含安全统计；部分失败仍写报告并返回非零退出码。

恢复API时可设置 `REFRESH_PAUSED=true`，从暂停状态启动信息刷新时钟，避免启动立即产生第二轮；页面可显式恢复或手动触发。该变量不影响上述显式单次命令。Agent仍由用户手动启动。

## Agent 临时管理页

访问 `/admin/agents`（主后台有导航），使用现有管理员登录。提供模型/搜索 API 配置、按用户与 Agent 隔离的结构化记忆、智能体创建修改和手动运行。配置密钥使用 `CREDENTIAL_KEY` 加密保存在 v6 配置表，不回显；保存后用于后续运行。`AGENT_DEMO_ENABLED=true` 可启用明确标注的本地自测，无需模型 Key 即可验证真实记忆工具与持久化；默认关闭。详见 [管理后台说明](internal/agentadmin/README.md)。


自动刷新时钟：学习通、MOOC、U校园的已登录且启用连接每轮完成后等待10分钟；教务系统仅手动同步。学习平台与 DWS 共用一个信息刷新时钟，部署设置 `REFRESH_INTERVAL=10m`（兼容旧 DWS_POLL_INTERVAL）；DWS 按现有授权群同步。启动时首次扫描与显式手动同步无需等待该间隔。待登录、过期或停用连接不做定时采集。每轮仍沿用现有分页预算，不代表10分钟内必然完成全量刷新；上游失败不会被视作同步成功。

后台新增[计划任务](internal/scheduler/README.md)入口 `/admin/tasks`：管理员可查看共享信息刷新时钟的运行状态、间隔、下次执行与最近结果，暂停/恢复、调整间隔或手动触发一轮。调整仅当前进程有效，重启恢复部署默认；当前不是持久化任务调度系统。

2026-11-04 学习通反爬修复：同课程低频对照确认持久化旧 `k8s` Cookie 回放会触发202验证页。恢复会话时忽略旧票、保存时不持久化该票，仍正常接收服务端新票；加700ms可取消请求间隔，并用 `verification_required`（HTTP429）标识验证页，停止该连接本轮后续资源/课程扫描。无需清空连接或重新登录。详见 [学习通说明](internal/chaoxing/README.md)；真实批量作业/进度已恢复；随后定位并修复通知末页省略 notices、讨论空末页省略 datas 及可选 lastAuxValue 的兼容问题，通知与23门课程讨论批测均通过。加载本地管理员环境后可用 `python3 scripts/chaoxing_regression.py` 观察一轮现有计划任务并批量检查恢复情况，不额外触发上游刷新。

## 教学周日程

已接入一张 schedules 表及 `/api/v1/schedules` CRUD；按 academic_year + term + teaching_week 查询，亦支持时间范围。time_status 动态计算，event_status 独立更新；mod JSONB记录永久/临时修改前后及上游变更。永久修改作用于同一安排的当前及后续教学周，临时修改仅限选定周。手动教务课表同步需显式提供校历/节次作息，完整读取成功后同事务刷新TIMETABLE，不改USER/AGENT。失败保留旧数据；身份无法匹配的本地调整保留历史并在响应报告。完整API示例、Bridge操作、事务链和验证见 [schedule/README](internal/schedule/README.md)。

后台 `/admin#schedulePanel` 现提供教学周日程面板，可查看、新增、编辑、完成、删除及查看修改历史。教务课表同步支持图形化时间表编辑器：设置节数、每节课时与休息时长，统一生成或从任意节次顺延填充，逐节作息无需手写JSON。编辑器草稿在当前页面按连接和学期隔离，刷新页面后清空；接口和存储结构不变。

Memory System V0.2 已接入原 Agent Runtime：V9 迁移、用户 PUB / Agent PRI / Workflow PRI、有效期、L0–L3、直接关联及8个记忆工具。结构、权限和验证见 [Memory V0.2](../agent/framework/MEMORY-V0.2.md)。

计划任务与运行控制：`/admin/tasks` 展示持久化刷新轮次、分平台 Diff、Trigger/Filter 与 Agent/Workflow 运行。当前为手动调试模式，刷新不自动调用模型；点击“启动本轮处理 / 处理此平台”后才进入处理队列。不同 Workflow 可并行，同一用户同一 Workflow 在后端排队串行。接口与恢复语义见 [runcontrol](internal/runcontrol/README.md)。
