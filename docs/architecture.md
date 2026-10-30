# OpenDL 架构：浏览器嗅探 → 桌面下载

复刻 IDM 的"扩展嗅探 + 原生消息 + 桌面下载"三件套：

    浏览器页面                       桌面
    ┌─────────────┐  runtime 消息  ┌──────────────┐  stdio 帧协议  ┌──────────┐  loopback HTTP  ┌──────────────┐
    │ content.js   │ ────────────► │ background.js │ ─────────────► │ idmhost  │ ──────────────► │ OpenDL 窗口  │
    │ 资源嗅探      │ ◄──────────── │ service worker│ ◄───────────── │ (exe)    │ ◄────────────── │ download.    │
    └─────────────┘   popup 列表   └──────────────┘   JSON 应答     └──────────┘   token 鉴权     │ Manager      │
           ▲                             │                                                            └──────────────┘
           │ MutationObserver /          │ chrome.downloads 兜底（未装主机时）
           │ performance.getEntries      ▼
    ┌─────────────┐
    │ popup.html  │  列出嗅探结果 + 逐条/批量下载
    └─────────────┘

## 与 IDM 组件的对应

| IDM | OpenDL | 说明 |
| --- | --- | --- |
| content.js（DOM/媒体嗅探） | extension/content.js | 同样的宽容 URL 正则（pathrec/extract.go 有 Go 版），加 performance 资源表扫描 |
| background.js（规则/转发） | extension/background.js | MV3 service worker，原生端口保活、断线重连、浏览器下载兜底 |
| （同上，Firefox） | extension/background-firefox.js | Firefox 把背景脚本按模块加载，importScripts 不可用；该入口先按序加载 resources.js、i18n.js 再进入 background.js |
| idmcchandler2_64.dll | cmd/idmhost | 4 字节小端长度 + JSON 的原生消息主机，转发到桌面 API |
| IDMan.exe（下载引擎） | download.Manager + api.Server | 队列、多连接 Range 分片、断点文件、取消、命名 |
| 特殊路径掩码/站点规则 | pathrec（mask/rule） | 命名与识别沿用上一轮重构 |

## 资源嗅探来源（content.js）

1. DOM: video/audio/source/embed/object/iframe、a[href]（按扩展名过滤）、srcset。
2. Network: 在可用时通过 PerformanceObserver 监听后续资源，并用 performance.getEntriesByType("resource") 扫描已有记录；筛选 initiatorType=media/video 或可下载扩展名。扩展不会清空或调整页面的性能记录缓冲区，未进入性能记录和不符合筛选条件的请求仍可能遗漏。
3. 文本: 页面 HTML/脚本里的 URL 用 IDM 宽容正则抽（空格、;、~、unicode 路径都保留），
   只留可下载扩展名。
4. 动态: MutationObserver 合并短时间内的新增节点和资源属性变化，批量扫描；打开弹窗时仍会重新扫描当前文档。动态页面中短暂出现并立即消失的资源不保证全部捕获。

弹窗通过 `webNavigation.getAllFrames` 列举当前标签页框架，再按 frameId 查询各自的嗅探结果并按 URL 去重；无法访问的 HTTP(S) 框架会计入提示。扩展通过 `webRequest` 被动读取 HTTP 响应的 Content-Type，补充识别没有常见文件扩展名的 HLS、DASH 清单及媒体元素直接请求的常见音视频文件；普通 fetch/XHR 媒体分片不按完整音视频文件自动识别。资源地址及来源框架保存在扩展会话存储中，不保存响应头或响应体。地址可能包含签名参数；每个标签页最多保留 100 条，框架导航和关闭标签页时清理，浏览器会话结束时清空。会话存储不可用或额度不足时回退到后台内存，此时后台停止后结果可能丢失；后台启动前已发生的请求仍可能遗漏。浏览器受限页面和未注入脚本的框架不能通过页面脚本采集。

M3U8 可选择下载清单，或尝试下载未加密、无不连续段的 TS / fragmented MP4 点播，历史 `mediaMode: "hls-ts"` 标识保持兼容。MP4 初始化段与媒体片段经过容器和相对寻址检查后直接合并，无需转码；分片只在同步及摘要计算后进入检查点。任务记录新增可选 `hls_format: "fmp4"`，允许保存不满足 TS 包对齐的片段，旧 TS 格式及其清单摘要不变。此实现不做音视频转码、独立音轨混流或 DRM 解密；切换初始化段、直播及 MPD 合并仍需后续实现。具体格式限制见 [下载功能差距](download-gap-analysis.md)。

## 消息协议

周期同步功能已从桌面界面和自动调度中移除。旧任务记录中的周期字段保留用于兼容，但不再触发定时同步；本地 API 不再接受非零周期。

插件弹窗中的下载使用 `prompt` 请求：桌面端按顺序显示预填的添加下载窗口，用户确认后才创建任务；取消不会下载。桌面端不可用时，此入口不会回退为浏览器直接下载。浏览器下载接管仍沿用原有下载协议。

原生消息（扩展 ↔ idmhost，4 字节 LE 长度 + JSON）：

| 方向 | 消息 | 字段 |
| --- | --- | --- |
| → host | ping | - |
| host → | 应答 | ok, app, version |
| → host | download | url, pageUrl, title, filename, mediaMode, queue, clientRequestId |
| host → | 应答 | ok, id 或 ok:false, reason |

每个请求及应答携带 `requestId`，用于区分握手和并发下载。主机不可用时扩展可回退浏览器下载；若请求已发出但结果不确定，扩展提示用户先检查桌面任务列表，避免重复下载。主机协议改动需要重新安装或更新原生消息主机，扩展需要重新加载。

支持 `idempotent-submission` 能力的原生主机还会转发独立的 `clientRequestId`。扩展在浏览器会话内为同一页面、资源及下载选项复用该编号；记录只包含请求摘要、随机编号及发送状态，已尝试发送的记录保留至页面导航或关闭，未发送或首次明确拒绝的记录一天后清理，每页最多 256 条。桌面端在任务记录中保存编号及不含认证头、URL 密码的请求指纹，同编号重试返回原任务编号，下载内容或选项冲突返回 HTTP 409。任务被用户移除后该编号可再次创建任务。旧原生主机沿用原来的不确定状态提示；原生主机会检查桌面端能力后才发送带编号的请求。

扩展发送前还会保存提交尝试状态。若此前请求已被接收或结果仍不确定，即使后续原生主机不可用，也不会自动回退为浏览器下载。首次请求被明确拒绝时可解除本次标记；并发重试或先前不确定的记录会保守保留。会话记录读取失败时提示先检查桌面任务，避免在缺少状态时创建重复下载。

浏览器回退时，仅将安全的单个文件名传给下载 API；不接受路径或不兼容的文件名，遇到同名文件由浏览器生成不冲突的名称。

原生消息协议支持主队列和夜间队列；当前扩展弹窗发送默认队列，队列可在桌面端的添加下载窗口中选择。夜间任务要求原生主机和桌面端均声明支持该能力，避免旧版本忽略队列字段后立即开始下载；桌面端不可用时不会回退为浏览器即时下载。夜间队列仍需在桌面端启用计划或手动开始。

扩展设置提供默认关闭的“使用浏览器登录状态”选项，启用时由浏览器请求可选 Cookie 权限，适用于资源列表中单个和批量发送的任务；浏览器下载接管不使用该选项。该选项仅提取当前标签页所属存储区中、与顶层页面和资源地址同源的非分区 Cookie；跨源资源、分区 Cookie 和需要其他认证方式的站点尚不支持自动传递。Cookie 仅用于当前桌面任务，任务记录不保存该请求头；跨源重定向时引擎移除敏感头，重启后需要重新提供凭据。浏览器自身回退下载仍使用浏览器的 Cookie 策略。设置与授权流程需手动验证。

Loopback API（idmhost/任意本地工具 ↔ 桌面，127.0.0.1:37913，请求头 X-OpenDL-Token）：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | /api/ping | 存活检查及能力声明 |
| POST | /api/download | body: {url, pageUrl, title, filename, mediaMode, queue} → {id} |
| GET | /api/tasks | 任务快照列表 |
| POST | /api/cancel | body: {id} |

桌面端先占用本地端口，再生成并写入 %AppData%/opendl/api-token（0600）；
生成失败时不启动接口。idmhost 每次请求读取当前令牌；未携带或错误返回 401，
第二个实例无法因端口冲突覆盖正在运行实例的令牌。跨来源或降级重定向不转发认证及自定义敏感请求头。

## 安装步骤（Windows）

桌面端启动时自动为当前用户注册 Chrome、Edge 和 Firefox 原生消息主机，主机由桌面程序自身提供，无需安装 Go 或运行注册脚本。浏览器扩展仍需用户在浏览器中安装或授权；开发阶段可从扩展管理页加载 `extension` 目录。Chromium 开发版使用清单中的公钥固定身份，Firefox 使用清单中的 Gecko ID。加载后打开普通网页即可使用；如桌面端注册尚未完成，插件会自动重连。

MyGo 打包配置将 `extension` 目录作为应用资源包含在安装包中；Windows 安装后可在应用安装目录的 `extension` 文件夹找到 Chromium 扩展，并在 Chrome 或 Edge 的扩展管理页通过开发者模式加载。Firefox 使用的 `manifest-firefox.json` 仍需另行替换为 `manifest.json` 并签名，安装目录内的目录不是已签名的 Firefox 扩展。安装器不会自动在浏览器中启用扩展；安装路径和加载流程仍需手动验证。

Chromium 的正式商店发布必须使用与清单公钥对应的发布身份；若商店分配了不同的扩展 ID，需要同步调整桌面端允许的来源。Firefox 的正式发布需要签名。原有注册脚本仍供开发和兼容用途，不是普通用户的安装步骤。

## 下载引擎（download.Manager）

- 并发: 默认 3 个任务并行，每任务 8 连接（>4MiB 且服务器 Accept-Ranges 时分片）。
- 分片: 预建 .part 文件按 Range 并行写入各自区间（offsetWriter），完成后改名。
- 命名: pathrec.DeriveFilename（Content-Disposition RFC 5987/2047 → URL 末段 →
  标题 → MIME 兜底），同名自动 "name (1).ext"（.part 独占创建认领）。
- 取消/错误: cancel 通道中断传输；错误写入快照 Err 显示在行内。
- UI: ui.List + ui.Progress（未知长度显示不定态），Workers 通过 Services.Invalidate
  请求重绘，视图每帧从 Snapshot 读取。

## 验证

- go tool mygo vet . 通过；CGO_ENABLED=0 go test ./... 通过
  （pathrec / download / api / 主界面 UI 测试）。
- idmhost 帧协议冒烟: 发送 {"type":"ping"} 帧 → {"app":"opendl-host","ok":true,...}。

## 后续路线

- 扩展 HLS 支持：画质与独立音轨选择、加密、fMP4 和复杂封装；实现 DASH/MPD 下载。
- 网络层细嗅探：declarativeNetRequest 观察或 CDP 会话抓请求头（Range/Referer 随任务透传，
  download.Request.Headers 已留字段）。
- 站点规则表（pathrec.SiteRule）随桌面端下发到扩展，覆盖无扩展名下载路径。
- 下载前弹确认（对应 IDM 的"开始下载"对话框）与站点登录凭据表。
