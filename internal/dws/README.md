# DWS v0.1

负责范围 `internal/dws/**`。依赖固定 `model.DWSRepository`、`credential.Vault`、`httpkit` 和已有 chi；无 SQL、通知或模型调用。官方 CLI 是唯一平台客户端，不重写认证或协议。

## 启动接口

```go
service, err := dws.New(repo, vault, dws.Config{
    BinaryPath: "/absolute/path/to/dws",
    BinarySHA256: "部署时计算并固定的小写SHA256",
})
// main 对 /api/v1/dws 挂 users 身份和写请求 CSRF middleware。
r.Mount("/api/v1/dws", service.Routes())
// 由 main 管理生命周期；构造函数不会启动循环。
err = service.Run(ctx)
```

`Config` 可选 WorkspaceRoot（默认 `/dev/shm`，必须已存在且为 tmpfs）、PollInterval（1分钟）、CommandTimeout（30秒，最多1分钟）、Overlap（5分钟）、InitialLookback（24小时，最多30天）。Linux 专用。二进制必须绝对路径、常规可执行文件且摘要匹配；不运行时安装或升级。运维应令二进制及其父目录对服务账号只读。研究固定二进制 v1.0.65、commit `92c4f8c355c749ec470c68f998d862a00d7d1709`。版本切换必须重新验证 legacy JSON 契约。

Run 通过显式 worker-only ListPollTargets 分页读取 enabled/active 连接，先恢复 ListPendingJobs，再按 allowlist 创建轮询任务。POST sync 创建持久化 queued job，返回202；内存队列用于立即唤醒，进程重启后持久 pending 由下一轮扫描恢复。任务执行前获得连接独占 fencing lease 并 StartJob；成功事务 CommitSync，失败记录安全错误码。连接删除/禁用由 Repository fencing 拒绝旧执行者提交。Service.Disconnect 先数据库禁用，再按精确 owner+connection 取消本进程在途执行，最多等待12秒完成进程终止、工作区清理和lease释放；登记在取得独占lease后，登记后重读连接以拒绝与断开竞争的disabled连接。多进程部署中其他进程依靠DB fencing拒写，取消映射仅覆盖当前进程。单轮顺序执行，慢连接影响其他连接延迟；首版没有并发调度和退避策略。

## 授权与隔离

只支持 personal；school 返回 unsupported。后台首次绑定使用 `POST /api/v1/dws/link`，无请求体，无需填写企业或钉钉用户 ID。服务器创建（或复用）当前 owner 的无凭据、全空身份 pending 连接；连接 scope ID 由服务器生成，和上游官方 corp/user ID 分离。旧 `POST /connections` 手动创建路径仍要求精确 profile、corp_id、upstream_user_id，profile 严格等于 `corp:user`，不接受 CSV、多 profile 或 current profile fallback。owner 永远取应用会话，不能由 HTTP 请求选择。群 allowlist 默认为空，不自动发现或采集所有群。

后台 Link 已接入固定官方 `auth login --device --no-browser -f json`，只有用户点击授权后才启动命令。首次授权由新 `/link` 入口创建全空身份 pending；已有连接继续使用 `/connections/{id}/link` 重授权，pending、active、expired、error 连接均可发起，disabled 不可。仅 pending 允许全空身份；部分空身份被拒绝。每次从空私有 tmpfs 工作区开始，重新授权失败不覆盖旧工作区。专用认证执行器不受普通命令30/45秒超时限制；服务硬截止4分钟、连接租约5分钟，上游挑战有效期更短时取较短值。只解析已核对官方 `device_flow.go:dfPrintDeviceAuthorization` 的中文 stderr 标签，固定 `DWS_LANG=zh`、`NO_COLOR=1`、`DWS_NO_UPDATE_CHECK=1`，读取完整URL行；接受 HTTPS 的 dingtalk.com 及其严格子域，拒绝 userinfo/显式端口及其他域，不假定path/query。官方 invalid_grant 重试时清除旧挑战并展示新挑战，仍受原4分钟截止限制。最终 stdout 必须是官方成功 JSON，success/token_valid 为 true，corp_id/user_id 非空并构成合法 `corp:user`。已有身份必须精确匹配；全空 pending 身份从最终结果采纳。随后用该最终 profile 执行 readonly auth status，再核实 authenticated、身份和 access token 有效。Vault 加密后由 Repository CompleteLink 在同一事务重新验证 owner 未 disabled、scope/lease 未撤销、连接身份仍全空 pending（且无凭据）或与最终身份完全相同，原子保存 profile/corp/user、工作区密文和 active。取消、失败和事务拒绝不会产生中间身份或新凭据；原有连接的旧凭据保留。错误只返回安全错误码，原始 stdout/stderr 不进响应或日志。

`Service.Run(ctx)` 显式绑定服务父context，HTTP请求完成不会取消授权；Run 退出时调用 CloseLinks 取消并等待全部认证执行结束。Run 尚未启动/停止时 Link 返回409。每应用用户最多一个尚未退出的授权进程，全局最多16个；状态缓存最多256条。LinkNew 在创建连接前先检查这些 admission 限制；Repository 通过用户行锁串行复用该 owner 的全空身份、无凭据 pending 连接，失败/取消后可重试，重复点击不会不断新增空连接。取消/终态/到期立即清除URL与code；Run定时清理过期条目，进程重启清空所有挑战（404后需重新点击授权）。断开连接复用 active operation 注册取消与DB fencing，完成进程组 kill/wait、tmpfs清理及lease释放才结束等待，不能断开后复活。多进程部署需固定路由到挑战所属进程；挑战不持久化。

仍保留运维离线授权：先用 `ROOT=$(mktemp -d /dev/shm/dws-auth.XXXXXX)` 创建独立 tmpfs 工作区，在 ROOT 内，设置 `HOME=$ROOT/home`、`DWS_CONFIG_DIR=$ROOT/config`、`DWS_KEYCHAIN_DIR=$ROOT/keychain`、`TMPDIR=$ROOT/tmp`、三个 XDG 路径（`$ROOT/xdg/config`、`cache`、`data`），cwd 为 ROOT，清空其他环境后执行固定官方二进制 `auth login --help` 核对所需授权选项，再运行官方交互登录。不要把认证链接、二维码或 token 写入业务日志。登录后由 root main 的运维子命令调用：

```go
err = service.ImportWorkspace(ctx, ownerID, existingPendingConnectionID, root)
```

导入仅接受运维可信参数；HTTP 不接受服务器目录、token 或任意加密工作区。导入先安全 pack/unpack 到服务独立 tmpfs，通过 `auth status --profile corp:user --readonly -f json` 检查 authenticated、token_valid、corp_id 和 user_id，再由 Vault 封装并按 owner+connection 绑定保存，最后置 active。导入要求当前 access token 有效；若仅 refresh token 有效请先在离线官方 CLI 刷新/重新登录。session/check 可返回 access失效但refresh有效的真实状态。操作完成后运维必须删除原离线 ROOT；服务只负责删除自己的临时副本。

每次 CLI 调用使用独立连接工作区：HOME、CONFIG、KEYCHAIN、cwd、TMPDIR、XDG 全覆盖，不继承宿主 token/proxy/plugin 环境。Linux `DWS_CONFIG_DIR` 单独不能隔离凭据；`DWS_KEYCHAIN_DIR` 内实际 `dws-cli` 子目录中的 enc 与明文 dek 一起由中心 AES-GCM 加密归档。恢复、读取、刷新、导入和附件操作共享连接级 DB 租约，最长5分钟执行且租约6分钟，CLI 子进程组超时/取消杀死，stdout/stderr 每路最多1MiB。工作区没有磁盘 fallback。独立目录不构成同 OS 账号进程间沙箱，部署需可信只读官方二进制；tmpfs 也应配置适当 swap 策略。

## HTTP 路由（相对 /api/v1/dws）

| 方法 | 路径 | 输入/输出 |
|---|---|---|
| GET/POST | `/connections` | 当前用户连接列表 / 创建 pending |
| GET/DELETE | `/connections/{id}/` | 详情 / 断开并保留历史 |
| GET/PUT | `/connections/{id}/groups` | allowlist / `{groups:[{id,name}]}`，最多100 |
| POST | `/link` | 无请求体（非空 body 返回400）→ 202 LinkChallenge，创建/复用服务器 pending 连接并启动官方登录 |
| POST | `/connections/{id}/link` | 无请求体 → 202 LinkChallenge，已有连接重新授权 |
| GET | `/connections/{id}/link/{challenge}` | 200 当前owner的LinkChallenge；建议每2秒轮询 |
| DELETE | `/connections/{id}/link/{challenge}` | 取消并等待清理 → 200 LinkChallenge |
| POST | `/connections/{id}/session/check` | 安全身份状态 |
| POST | `/connections/{id}/sync` | `{group_id}` → 202 SyncJob |
| GET | `/connections/{id}/jobs/{job}` | 持久化任务状态 |
| GET | `/connections/{id}/messages?group_id=...&after=0&limit=20` | 不可变观察，最多100，after 为中心递增ID |
| GET | `/connections/{id}/messages/{message}/context?group_id=...` | 同群前后各3条观察 |
| POST | `/connections/{id}/messages/{message}/attachments/{attachment}/archive?group_id=...` | 首次当前附件归档 |
| GET | `/connections/{id}/messages/{message}/attachments/{attachment}` | 已归档 bytea 的安全下载 |

LinkChallenge JSON 为 `{id,connection_id,status,expires_at,verification_uri?,user_code?,error_code?}`，expires_at 为 RFC3339；status 为 starting/pending/authorized/failed/cancelled/expired。starting 尚在请求真实挑战，pending 才展示 verification_uri/user_code；终态停止轮询。所有授权响应 `Cache-Control: no-store`，URL/code 仅返回当前 owner，不可保存到业务记录、日志、分析系统或模型。未知/其他owner/进程重启后的challenge统一404；冲突/服务未运行409；上游异常转安全错误码。认证和写请求CSRF由父middleware负责。

附件 ID 为消息 content 内 resourceRefs 的 `attachmentId`，由已存源引用确定性生成，客户端不提供资源地址。Message.Content 存储为 []byte，HTTP DTO 输出为 JSON 对象，保留外部消息ID、群ID、发送者、时间原文、quotedMessage/forwarded 的安全源字段、资源引用、标签及 unconfirmed 状态。列表当前使用简单中心ID after，而不是通用规范建议的签名cursor；每次读取仍由 Repository 验证 scope。创建及sync尚未实现 Idempotency-Key，重复sync可能新增任务，消息按scope+群+原生消息ID保留最新状态，重复内容不更新，变化原位更新且ID稳定。

## 分页、增量、上下文与附件

CLI 精确入口 `chat +chat-messages --group CID --start RFC3339 --end RFC3339 --order asc --limit 100 --profile PROFILE -f json [--page-token TOKEN]`。仅解析 legacy 顶层 `im.message-list.v1`，不把官方分页迁移文档中其他命令的 envelope 套用到本命令。每次窗口 start/end 固定；起点为上次完成终点减 overlap，首次 lookback。要求 paginationKnown、count、messages、hasMore、complete 明确存在；partial、failedCount、failures、decryptFailedCount、错误业务状态、跨CID或时间范围异常均失败，水位不推进。空页有 hasMore 时必须新token，检测循环，最多100页/10000条、内容总计16MiB。支持毫秒、RFC3339 和官方无时区时间字符串（按 Asia/Shanghai 解释，原文保留）。完整窗口才事务保存观察、加密工作区和新的 poll watermark。失败后从旧窗口重新拉取；有效租约内仍加密回存 CLI 已刷新状态，不推进水位；成功 CommitSync 不二次保存工作区。Repository按scope+group+externalID匹配对象，hash/source_version变化时原位更新，不保留旧版本。DWS消息不发trigger事件。

标签仅规则匹配 assignment/exam/notice/file/ordinary，可多标签，verification 始终 unconfirmed。没有自动确认、课程推断、截止时间推断或通知。上下文仅同群中心ID顺序邻居，不声称语义相关；同群消息最多扫描10000观察，超预算明确失败。replyId 源字段保留但未实现引用遍历。消息编辑会更新原行，邻居列表不包含旧内容版本；按中心ID排列不是编辑时间流。

附件仅从已归档消息 resourceRefs 获取 fileId/mediaId；兼容官方 download.arguments 投影，只有确认属于父消息的引用 downloadable，嵌套/转发引用保留metadata但不开放下载；fileId缺owner时绑定已有父消息。归档按 MIME/扩展名提供 image/document/archive/other 规则分类；固定 CLI 下载到 downloads/opaque.bin，监控8MiB上限，验证下载 JSON 回执身份、路径、尺寸，最终仅读常规文件，SHA256 后 PutAttachment 保存 bytea。工作区下载文件在重新加密前移除。归档只写首次当前观察，不把当前字节宣称历史原版；响应明确 `current_observation_not_historical_original`，GET 只返回这次不可变归档。模型未提供观察时间字段，当前归档日期以连接操作记录/外围审计为准；不声称源附件历史版本可证。禁止返回临时资源 URL。5ms监控可能允许短暂超过阈值，最终读取严格拒绝超过8MiB；tmpfs需额外部署容量约束。

## 验证

`gofmt -w internal/dws`、`go test ./internal/dws`、`go vet ./internal/dws`。授权测试使用可注入 fake auth executor，不启动真实登录；官方中文呈现fixture覆盖分块stderr、完整/手动URL、重试换码、fragment及恶意域拒绝；服务测试覆盖Run前拒绝、请求结束不中止授权、跨owner状态/取消拒绝、每用户并发拒绝、短有效期、取消、断开、父context取消、错误身份与lease拒写、成功加密保存。`go test -count=5 -race ./internal/dws` 通过。脱敏 fake executor 覆盖两页固定窗口、重复观察、解密失败、分页循环、取消、fencing拒绝、跨scope和群allowlist、分类；runner测试覆盖隔离环境、进程组取消、输出上限、附件大小和tmpfs约束。测试不访问真实账号、真实网络或私有群。

已读取官方源码和三份研究 Markdown，并以固定官方二进制的 help 核对命令。真实账号未授权：尚未验证双账号同时登录、刷新、重启恢复、群上游权限、真实附件或服务端实际分页完整性。官方 followups 已记录群消息读取可能因服务端字段映射失败，本模块会如实失败，不能承诺真实平台可读。
