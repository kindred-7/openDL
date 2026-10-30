# OpenDL 与 参考下载器 功能对照

更新日期：2026-10-28。依据为当前仓库源码与下列 参考下载器 官方说明，未启动应用、运行构建或测试，也没有进行 参考下载器 与 OpenDL 的实机对测。

**当前结论：尚未全部复刻。** “已有实现”只表示已找到对应的源码路径，不表示行为、性能、浏览器覆盖或异常恢复已经通过验证。没有用 UI 入口代替完整功能，也不以数量百分比表示完成度。

## 下载与存储

对照依据：[参考下载器 功能介绍](https://www.internetdownloadmanager.com/features2.html)、[动态分段说明](https://www.internetdownloadmanager.com/support/segmentation.html)。

| 功能 | OpenDL 源码现状 | 仍需补齐或人工确认 | 源码入口 |
| --- | --- | --- | --- |
| HTTP / HTTPS 下载 | 已有实现 | 真实服务器兼容、性能和大文件压力场景 | `download/manager.go` |
| 动态分段与空闲连接协助 | 已有实现：动态分块队列、空闲连接让出（最大剩余段被更快连接接管）、断点检查点；HTTP 范围与 FTP 分段共用同一驱动 | 与 参考下载器 的连接复用细节不同（接管经取消并重新请求）；实际收益待人工实测 | `download/manager.go`、`download/segments.go` |
| 暂停、继续与重启恢复 | 已有实现：同步分片、摘要、资源版本校验 | 断电、文件被修改、签名地址过期等实机场景 | `download/store.go`、`download/validator.go` |
| 临时网络错误重试 | 已有实现：退避、Retry-After、空闲读取超时 | 按站点自适应连接数及队列级重试策略 | `download/retry.go`、`download/idle.go` |
| 限速与连接额度 | 已有实现：全局速率额度、逐队列速度额度与并发上限（主/夜间/自定义）、每站点连接上限（含子域）、任务/站点连接排队 | 公平性与真实网络实测 | `download/pace.go`、`download/manager.go`、`page_settings.go` |
| 完成文件发布 | 已有实现：避免覆盖、发布日志、Windows 占用重试，跨卷与无硬链接文件系统的复制发布回退 | 复制回退的原子性、跨卷与网络文件系统实测 | `download/verify.go`、`download/publish_windows.go`、`download/publish_fallback.go` |
| 磁盘与文件系统 | 已有实现：FAT32 上限、已知大小文件空间预检（Windows 与 POSIX）、并行任务空间预算（按预计剩余互相扣减）、Windows 超长路径 | 真实介质、极端并行与长路径边界待人工验证 | `download/filesize*.go`、`download/space*.go`、`download/extended_path.go` |
| FTP / MMS 等其他协议 | 已有实现：ftp:// 与 FTPS（隐式 TLS、显式 AUTH TLS/PROT P）下载、动态多连接分段（与 HTTP 范围共享分段驱动与检查点）、REST 检查点续传、SIZE/MDTM 校验、URL 与站点登录凭据、UI/API 接入 | 代理链路；FTP 分段与真实服务器行为待人工验证；MMS 等已废止协议明确不在范围 | `download/ftp.go`、`download/manager.go`、`api/server.go` |
| 命令行接口 | 已有实现：`opendl-cli` 经回环接口（[接口说明](loopback-api.md)）提交下载（名称、目录、队列、来源页、媒体选项、期望摘要、幂等编号、自定义头）、查询任务、继续/取消任务、全部开始/全部暂停、批量提交（文件或标准输入逐行地址）与连通性检查，令牌与原生主机同一来源 | 计划窗口等界面状态不随命令行变更；脚本化工作流与桌面并行的实测 | `cmd/opendl-cli/main.go`、`api/server.go` |
| 文件完整性 | 已有实现：SHA-256 发布前比对、Windows Authenticode 签名验证、Ed25519 原始格式、OpenPGP EdDSA 和 Minisign 预哈希分离签名 | 证书链、时间戳、撤销与分离签名实测；OpenPGP 支持 RSA/ECDSA/EdDSA，Minisign 旧格式限 64 MiB；不能以自行计算的摘要代替发布方期望值 | `signature/`、`download/verify.go` |

## 浏览器与媒体

对照依据：[参考下载器 功能介绍](https://www.internetdownloadmanager.com/features2.html)、[浏览器及接管选项](https://www.internetdownloadmanager.com/support/using_参考下载器/options.html)。

| 功能 | OpenDL 源码现状 | 仍需补齐或人工确认 | 源码入口 |
| --- | --- | --- | --- |
| 页面资源列表与多选提交 | 已有实现：DOM、性能资源表、动态节点、多框架扫描，以及页面注入的 fetch/XHR/媒体元素/blob 转发捕获 | 浏览器内部页面、注入受限的框架和复杂播放器仍可能遗漏；识别深度待人工验证 | `extension/page.js`、`extension/content.js`、`extension/popup.js` |
| 网络 MIME 识别 | 已有实现：被动读取响应类型并缓存地址；页面注入侧补充响应类型与文本清单嗅探 | 不能据此宣称所有站点或所有媒体请求都可捕获 | `extension/background.js`、`extension/resources.js` |
| 原生主机与重复提交处理 | 已有实现：能力声明、会话尝试记录和持久请求编号 | 浏览器与主机退出、响应丢失、旧版本组合 | `extension/background.js`、`nativehost/host.go`、`download/submission.go` |
| 站点规则表 | 已有实现：规则文件（host/路径正则/扩展名）编辑即热更新并经原生主机下发到扩展；远程规则源（HTTPS 定时同步缓存、本地规则优先合并、上限不变）；同一规则服务提取与抓取 | 规则规模与真实站点覆盖待人工验证 | `pathrec/site_rules.go`、`site_rules_remote.go`、`nativehost/host.go` |
| 浏览器点击自动接管 | 已有实现：downloads 创建事件接管白名单类型、Alt+点击强制接管、Shift+点击放行本次、弹窗整体开关、接管失败回退浏览器下载（不丢任务） | 浏览器 API 无法区分用户“另存为”（Shift+点击为补偿放行方式）；真实站点行为待人工验证 | `extension/background.js`、`extension/content.js`、`extension/popup.js` |
| 浏览器覆盖 | 已有实现：Chrome/Edge 的 MV3 扩展与原生主机注册；Firefox 提供 manifest 变体（gecko 标识、事件页后台、页面世界注入需 Firefox 128+）与注册脚本 | Firefox 需自行签名保活附加组件并实机验证；Safari 需独立扩展与 macOS 签名，明确不在范围 | `extension/manifest-firefox.json`、`scripts/install-host-firefox.ps1`、`scripts/install-host.ps1` |
| HLS 合并与录制 | 已有实现：TS 与 fragmented MP4 点播合并、直播录制、选轨、AES-128 解密；点播兼容初始化段切换与 fMP4 不连续边界，单轨及符合周期对齐条件的独立双轨结构不兼容时尝试按周期转码 | 周期转码的音画同步和真实播放器兼容待实测；直播切换及不兼容的内嵌多轨切换尚不支持，SAMPLE-AES/DRM 不支持 | `download/hls*.go`、`download/hls_dual_live.go`、`download/dash_period_mux.go` |
| DASH 与其他媒体流 | 已有实现：静态 MPD 单周期与多周期、动态 MPD 直播录制、选轨及 FFmpeg 混流；静态多周期支持逐周期展开 SegmentBase 索引，初始化段不兼容时尝试转码 | 索引、续传、转码的时间轴和真实播放器兼容待实测；直播多周期不支持 | `download/dash_manifest.go`、`download/dash_period_mux.go`、`download/dash_live.go` |

MP4 依据 [RFC 8216](https://www.rfc-editor.org/rfc/rfc8216.html) 与 [W3C ISO BMFF 字节流格式](https://www.w3.org/TR/mse-byte-stream-format-isobmff/) 实现容器检查。此部分并非 参考下载器 内部实现的推断；详细边界见 [下载功能差距](download-gap-analysis.md)。

## 队列、抓取与设置

对照依据：[参考下载器 调度器](https://www.internetdownloadmanager.com/support/参考下载器-scheduler/参考下载器_scheduler.html)、[站点抓取器](https://www.internetdownloadmanager.com/support/参考下载器-grabber/参考下载器_grabber.html)、[选项说明](https://www.internetdownloadmanager.com/support/using_参考下载器/options.html)。

| 功能 | OpenDL 源码现状 | 仍需补齐或人工确认 | 源码入口 |
| --- | --- | --- | --- |
| 队列和启动计划 | 已有实现：主/夜间/自定义队列各自的并发上限与速度额度（满载队列不阻塞其他队列）、跨队列迁移（含夜间任务直接迁移）、队列排序（侧栏与选择器按序展示）、下载窗口（启动/停止时间、重复星期）及记录持久化 | 队列排序只影响展示顺序，调度保持公平不设抢占 | `download/queue.go`、`download/manager.go`、`page_settings.go` |
| 周期同步与完成动作 | 已有实现：所有队列清空后的关闭计算机/睡眠/运行程序（手动停止不触发、支持重复星期），以及资源变化同步（5 分钟至 7 天周期、条件请求探测、校验通过后原子替换、保留旧文件；对话框可立即同步一次） | 失败恢复与重启实测 | `finish_action.go`、`download/resource_sync.go`、`page_sync.go` |
| 流量配额 | 已有实现：日/周/月正文流量配额，用完自动暂停读取、到期恢复，用量持久化到任务目录 | 统计以启用期间经本应用的流量为准，崩溃与时区边界场景待人工验证 | `download/quota.go`、`page_settings.go` |
| 站点抓取 | 已有实现：单页静态链接提取、按深度（0–3 层）同主机递归跟进与文件类型过滤、登录抓取、按来源页保留目录结构、抓取项目保存/载入/删除与按小时至每天的周期执行（同地址任务去重） | 抓取上限和真实站点覆盖待人工验证 | `grab.go`、`page_controls.go` 的 `grabLinks` / `grabWatch` |
| 分类管理 | 已有实现：文件类型分类与筛选、可编辑的分类扩展名规则、自定义分类（名称/扩展名/归档子目录，侧栏与类型筛选联动）与按分类归档 | 自定义分类上限、真实使用场景与长期兼容待人工验证 | `main.go` 的 `catExt`、`page_settings.go`、`page_main.go` |
| 登录与认证 | 已有实现：Basic/Digest（含响应认证校验）、自定义头、站点登录表、可选浏览器 Cookie、Windows 集成认证（SSPI/Negotiate，Kerberos 优先、NTLMv2 回退、TLS 通道绑定），代理侧同套机制见“代理”行 | 域环境实测；跨平台 GSSAPI 为超出 参考下载器 的可选增强 | `download/digest.go`、`download/ntlm.go`、`winauth/`、`site_logins.go` |
| 代理 | 已有实现：代理模式设置（环境/系统/直连/手动）、系统代理读取（Windows 注册表、macOS scutil、Linux GNOME gsettings 与 KDE kioslaverc，按协议形式与例外列表）、PAC 自动配置求值（Windows WinHTTP；macOS 系统 JavaScript；Linux 可用运行时 node/gjs/qjs，缺失时明确不支持；脚本辅助函数带诚实存根）、按协议分别代理、代理认证及密码保护、Windows 集成认证（HTTP 请求层与 HTTPS 隧道 CONNECT 层挑战应答） | 真实代理环境实测 | `download/proxy.go`、`download/proxy_pac_common.go`、`download/proxy_pac_linux.go` |
| 下载后扫描 | 已有实现：完成后调用配置的扫描程序（参数模板 {file}，并发 2，10 分钟超时，非零退出码报未通过） | 扫描器差异、超时与大文件场景待人工验证 | `post_scan.go`、`page_settings.go`、`page_preferences.go` |

## 桌面体验与维护

对照依据：[参考下载器 界面、拖放、更新与多语言功能](https://www.internetdownloadmanager.com/features2.html)。OpenDL 的视觉实现继续以 `docs/design.html` 及 `AGENTS.md` 的最新约定为准，不改成 参考下载器 旧界面。

| 功能 | OpenDL 源码现状 | 仍需补齐或人工确认 | 源码入口 |
| --- | --- | --- | --- |
| 任务管理与详情 | 已有实现：选择、批量操作、筛选、详情与列偏好、任务列表导出/导入 CSV（导出含地址、来源页、状态、进度、队列、时间与媒体模式/画质/音轨语言/同步周期；导入按地址重建任务、按队列显示名映射本机队列、跳过重复与非法行） | 全流程键盘操作、读屏、窄窗口、亮暗主题实测；参考下载器 的下载中压缩包预览不在范围（下载后由系统工具查看） | `page_main.go`、`page_export.go`、`page_details.go` |
| 设置和面板布局 | 已有实现：独立设置窗口、主题、可调侧栏和详情面板、设置导出/导入（JSON，不含密文凭据，导入保留本机密文并即时应用） | 多窗口同步、焦点、减少动态效果及布局持久化实测；参考下载器 的工具栏皮肤按界面设计约定不做（统一 shadcn/Radix 视觉） | `page_settings.go`、`page_settings_io.go`、`page_layout.go` |
| 剪贴板、托盘与登录启动 | 已有实现 | 系统行为与错误恢复仍需人工确认；后台运行依赖托盘入口 | `page_main.go`、`tray.go`、`page_preferences.go` |
| 外部拖入链接/拖出文件 | 已有实现：拖入链接、选中文本或链接列表文件进入添加下载；已完成任务的文件可拖出到其他应用（复制，多选可整组拖出） | Move 语义与各系统文件管理器行为待人工验证 | `drag_drop.go`、`page_table.go`、`page_main.go` |
| 完成通知与声音 | 已有实现：完成事件合并为一条系统通知（提示音跟随系统，点击回到主窗口），可在设置中关闭 | 通知权限、多平台行为和长时间运行表现待人工验证 | `notify.go`、`page_main.go`、`page_settings.go` |
| 国际化与自动更新 | 已有实现：中英文词条与设置切换（含扩展语言包）、Ed25519 签名更新清单的检查/下载/暂存/安装回退 | 更新发布源与公钥待配置，安装器升级与多平台实测 | `i18n/`、`locale.go`、`page_update.go`、`docs/updates.md` |

## 后续顺序与完成条件

原定的四个开发阶段（媒体基础兼容、核心日常功能、分类与批量管理、凭据与维护链路）已在源码侧完成。当前剩余工作按性质分三类：

1. **超出 参考下载器 范围的可选增强**（参考下载器 为 Windows 闭源软件，以下均为本项目主动超出的部分）：非 Windows 平台的集成认证（GSSAPI，需 cgo）、非 Windows 的 PAC 求值依赖平台脚本运行时。这些保持明确报错或如实降级，属于增强 backlog 而非缺口；对齐 参考下载器 口径下不构成未齐全。
2. **用户侧准备**：更新发布源与 Ed25519 公钥、发布链路（见 [签名发布与应用更新](updates.md)）。
3. **实机确认**：按 [手动验证清单](manual-verification.md) 逐项执行并回填。

完成条件不变：每项都要有真实行为闭环、失败/取消/恢复处理、持久化兼容，以及由用户手动完成的运行确认。用户尚未运行的验证始终保留为“待确认”，不提前宣称“全部复刻”。

人工确认按 [手动验证清单](manual-verification.md) 执行，覆盖下载、媒体、浏览器、队列计划、代理、完整性与更新、界面系统和维护的全部实机项目。
