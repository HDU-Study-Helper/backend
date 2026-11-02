# U 校园 AI 只读 Adapter

负责范围仅 `internal/ucampus/**`。实现 `New(contract.Dependencies) (contract.Adapter,error)`、Describe、BeginAuth、PollAuth、Sync；无 SQL、路由、自建 goroutine 或调度循环。父模块负责加密 Session/cursor、存储、幂等和 Worker。WebSocket 依赖 `github.com/coder/websocket v1.8.14` 由核心负责人加入共享 go.mod。

## 能力与验证边界

| 能力 | 状态 |
|---|---|
| 官方 APP 二维码生成 | **真实匿名验证通过**：POST SSO code，得到 HTTPS 官方 mobile-default 扫码 URI，无账号、无扫码 |
| WebSocket Engine.IO 握手与 `/sso` namespace acknowledgement | **真实匿名验证通过**；HTTP polling 网关实测400，因此使用 WebSocket |
| pending/scanned/expired/cancelled 与登录 ticket 回传 | 官方公开 JS 协议实现、httptest 脱敏 Mock；**未真实扫码确认** |
| ticket→UAI JWT→账户接口验证→authorized | 官方公开 JS 协议实现、Mock；**未用真实账号验证**，任何交换/账户失败显式报错 |
| course | 首页课程接口实现、Mock；未使用真实账户验证 |
| assignment / progress | **需额外受控导入合法官方 annotator 会话**；教材单元 leaf 任务和原始状态实现、Mock。QR 本身不返回 annotator，缺失返回 ErrUnsupported，不能声称扫码后已支持这两项 |
| 独立教师作业、AI 门户正式考试、公告通知 | **未支持**。没有充分只读请求/结构证据，明确 ErrUnsupported，不用空数组代替 |
| 自动刷新 JWT、内容/答案解密、开始考试、提交/作答、刷时长 | **未实现且不调用** |

`assignment` 仅代表 `task_type=textbook_task` 教材任务；`progress` 输出同一批 leaf 的 state/duration/strategies 源观察，不把教材状态当独立教师作业或应用完成状态。不获取答案、试题、内容解密密钥，不制造 annotator 签名。

## 官方认证证据与临时连接生命周期

公开来源：

- https://sso.unipus.cn/sso/login
- https://sso.unipus.cn/sso/resources/js/config_82ab5c9.js
- https://sso.unipus.cn/sso/resources/js/login_22ed3aa.js
- https://birdflock.unipus.cn/rel/fe/usso/1.12.1/usso.min.js
- https://res.unipus.cn/production/portal-platform/portal/static/js/main.f5f21752.js

BeginAuth 调用 `POST /sso/7.0/sso/code`，JSON `{deviceId,service:"https://uai.unipus.cn"}`。`rs.content` 为真实扫码 URI：`https://sso.unipus.cn/sso/mobile-default.html?target=<32hex>@https://uai.unipus.cn@<ms>@ssoScanLogin`。通过二维码展示端把 QRURL 编码为图像，不能把 URI 当图片地址。二维码内容只给当前连接用户，不写学业记录或日志。

同一次 Begin 建立 `wss://umcs.unipus.cn/umcs/?EIO=4&transport=websocket&appId=1000&openId=sso&devId=...`，收 Engine.IO open 后写 `40/sso,`。PollAuth 每调用只读一个消息（最多35秒），收到 ping 写 pong；`msg_sso` 的 `eventType=code`：7202→scanned、7201/7205→expired（取消也按契约expired）。`eventType=login` 读取 ticket/openId，GET `/sso/serviceTicket/validate?service=...&ticket=...`，使用官方返回 JWT GET UAI `/api/account/user/info` 校验后才 authorized。没有猜测或本地签发令牌。

保持临时 WebSocket 是收到异步扫码事件的必要条件。adapter 私有 map 仅保存随机挑战 ID、scope、deadline、socket、busy，不保存全局账号/token。全局上限128、每用户最多1；Begin 替换该用户旧挑战；清理在 Begin/Poll 显式进行。没有自建后台循环。父服务必须持续 Poll（建议3秒；单请求可能等心跳35秒），调用 CancelAuth(ctx,scope) 断开连接、Close(ctx) 关闭服务。父HTTP请求超时应超过35秒。挑战本地保守期限2分钟，期限不是宣称官方精确TTL。进程重启/取消丢失socket后下一次Poll明确expired；网络Read失败返回ErrUpstream且清除socket，下一次Poll expired。调用ctx取消返回ctx错误并关闭socket。Socket库内部运行细节属于库，不创建采集循环。

## 受控 Session 导入

导出 `ImportSession(ctx,scope,raw []byte)`；可注入受控 HTTP transport 的 `ImportSessionWithDependencies(ctx,scope,raw,deps)`。raw是敏感JSON `SessionInput`：

```json
{"authorization":"<official-jwt>","open_id":"<authorized-sso-id>","annotator":"<optional-official-existing-token>","portal_cookies":[],"content_cookies":[],"expires_at":"2030-01-01T00:00:00Z"}
```

cookie 数组为 `http.Cookie` JSON对象（保留 Name/Value/Domain/Path/Expires/Secure/HttpOnly/MaxAge）。不得读取研究 live 凭据，不能接受模型输入或普通metadata，不能记录raw/Session。父运维导入才调用这个函数。该函数私有jar GET官方账户接口，要求appUserId存在，若传入open_id且账户ssoId存在则必须一致，绑定到可信scope。JWT头是裸token，不自动加Bearer。此检查证明会话能访问账户，不证明应用用户与学校身份的现实归属；后者依赖父授权操作。

Session opaque JSON由父凭据层加密；内含版本、scope、账户ID、授权信息、私有cookie与临时QR state。每次恢复均复制注入HTTPClient并创建私有jar；禁止继承共享jar。trackedJar保存Set-Cookie完整属性，避免root查询遗漏`/api`或`/sso`路径cookie。所有重定向拒绝，防止跨域泄凭据。不会全局缓存token/currentuser。

## 数据与分页

协议源码证据：`../../../research/unipus-ai/02-接口与源码证据.md`，`repos/UnipusAI/src/api/course.rs`、`session.rs`，`repos/UnipusHelperPro/.../UnipusRequest.java`。首页 `GET /api/cmgt/course/getHomeCourseListByStudent` 成功码1，严格要求 `value.courseList[].courseResourceList[]`；未实现书架fallback，首页为空只表示这个首页端点没有课程，不宣称覆盖全部书架。

教材 course_progress 成功码0，先GET `/course/api/v2/course_progress/{instance}/{openId}/default` 发现units，再单个GET `.../{instance}/{unit}/{openId}/default/` 读取leafs。每次Sync最多3个读取请求（首次首页+单课程units+单unit）；单页最多500条、响应4MiB，超限明确parse错误。请求失败不返回部分页或推进cursor。

- Course ExternalID：base64url(JSON `[courseId,classId,instanceId,courseResourceId]`)；ParentID为前两个ID。
- Task/progress ExternalID：上述四项再加 `[unitId,taskId]`；ParentID为前五项。完整原生ID另保留白名单Fields。
- Fields保留原始 `state.pass/pass2/perm`、duration/tab_type、嵌套 `strategies.start_time/end_time/required/min_score_pct/statistic_mode_out`；时间原样保留，不把未知值转换1970或假设时区。课程时间保留start_time_raw/end_time_raw。上游无任务标题则不编造标题。
- SourceVersion保留publish_version。Fields不复制JWT、Cookie、openId、flag、签名URL或完整上游响应。
- Cursor是base64url JSON，含版本、scope、resource、Session哈希绑定、剩余course/unit、加载阶段；父必须加密保存，不对模型开放。不得混用scope/resource或旧Session。
- 无changefeed；结束NextCursor为空，下轮全扫，父按原生复合ID+内容/版本幂等。未出现记录不代表删除。Empty units可以返回空Records+推进游标。

## 测试

默认全部httptest，无真实账号和网络：二维码状态/成功票据链/跨scope拒绝、两页教材任务、完整复合ID、更新、私有cookie双会话隔离、错误/限流/取消/unsupported/解析异常。Mock通过不是实际平台账号能力已验证。

```sh
gofmt -w internal/ucampus
go test ./internal/ucampus
go vet ./internal/ucampus
go test -race ./internal/ucampus
```

可选匿名公开探测（不扫码、不输出QR内容或票据）：

```sh
UCAMPUS_PUBLIC_PROBE=1 go test ./internal/ucampus -run TestAnonymousOfficialHandshake -count=1 -v
```

本次此探测通过：官方二维码创建、Engine.IO WebSocket握手、namespace ack。真实扫码授权、账户数据、跨真实用户与官方annotator获得方式均未验证。没有读取任何`research/**/live`凭据，没有使用真实已登录账号。
