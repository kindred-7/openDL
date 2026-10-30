# 参考下载器 浏览器请求捕获规则解构（文件预览请求到下载链接）

样本：参考下载器 6.43.16.1 扩展包（安装器 entry117，已按 zip 完整解出：
_extracted/ext2/entry117_u110094.bin__background.js 等）与 原始程序 字符串。
本文回答：参考下载器 用什么规则精确捕获浏览器发向服务端的文件预览请求并取出下载链接。

## 0. 结论

参考下载器 不依赖 URL 有没有扩展名。它是三层判定：
全量请求观察 - 请求头改写/标记 - 响应头判定 + 规则命中 + XHR 复测，
最后把 URL + 完整请求头 + Cookie 整体交给原生下载。因此
/preview?docId=123 这类无扩展名的预览请求也能精确命中，且下载时
会话（Cookie/签名/Referer）与浏览器预览请求完全一致。

## 1. 观察面：监听哪些请求

manifest（entry117）：webRequest + webRequestBlocking + cookies + nativeMessaging。

注册（background.js 函数 zb）：
- onBeforeRequest x2：{urls: Ha, types: Ea}（ftp 等）与 {urls: L, types: K} + requestBody
- onBeforeSendHeaders：requestHeaders + blocking
- onHeadersReceived：responseHeaders + blocking
- onResponseStarted / onErrorOccurred / onBeforeRedirect

类型集 K = main_frame, sub_frame, object, image, xmlhttprequest, other。
预览请求正是 sub_frame（iframe 预览）、object（插件文档）、
xmlhttprequest（fetch/XHR 拉文档字节）三类；URL 面为 *://*/* 与 ftp://*/*。
content.js 另行覆盖 media 元素与页面脚本。

## 2. 请求侧规则（onBeforeRequest，函数 Cc/Ka）

原生下发的主规则是一个大正则 d[18]，每个捕获组是一条站点路径规则，
组索引映射四张平行表：指令串、URL 正则、默认名、捕获组配置（F=文件名组，T=取标题）。
指令串旗标字符：

| 旗标 | 含义 |
| --- | --- |
| G / P | 只匹配 GET / POST |
| V / A / C | 视频 / 音频 / 内容类，命中转媒体流程 |
| H | 强制标记 |
| X / S / D | 排除 / 静默 / 禁止等行为开关 |
| T | 文件名取页面标题 |

命中后文件名取值顺序：URL 正则捕获组（F 组）- 标题（T）- 默认名；
再用扩展正则 f 校验，通过则标记下载（返回码 4）。
忽略表（d[2]/d[3]/d[4]/d[-10]，即安装包 entry044 的 URL 掩码与原生下发的
排除正则）先行排除广告与后台资源。内部协议（chrome://、about:、扩展页）直接跳过。

## 3. 请求头改写：预览请求的精确标记（onBeforeSendHeaders，Cb）

两处关键改造：

1. Accept 清洗：当 XHR 的 Accept 带 参考下载器 私有标记
   application/vnd.t1c.<kind>-<id>（Ua 正则解析出 kind/id，如 pxr/pfm），
   发往服务器前把该段从 Accept 剥离（保留前后缀），并把请求登记到规则 id
   名下（b.I=id）。预览请求因此与站点规则精确关联，服务器只看到正常 Accept。
2. 页面归属：用 Referer 把请求关联到发起页面（port.G），无 port 的 XHR 自动
   回填；同时记录完整 requestHeaders、时间戳、proxyInfo 与 Proxy-Authorization。

## 4. 响应侧判定链（onHeadersReceived - Fb/nc）

按序短路：

1. 状态码必须是 200 / 206 / 304；204 且状态行含 参考下载器 是原生信令特判。
2. 读 Content-Type（去参数）与 Content-Disposition。
3. 顶层导航且 text/html 且非 attachment - 记为页面，放行。
4. 忽略掩码命中 - 放行。
5. Content-Disposition: attachment 且顶层 - 立即接管（最强信号）。
6. filename 参数（disposition 的 filename / content-type 的 name）解码后是
   强信号；无扩展名但有 filename - 接管。
7. URL 末段扩展名与 MIME 子类型一致 - 命中（处理 .tar.gz 与版本号后缀）。
8. MIME 白名单表 hb（100+ 条，application/pdf、application/msword、
   application/zip、video/*、audio/*、text/plain 等）：扩展在该 MIME 的扩展集
   内 - 命中；无扩展名时用表内首项反推扩展名。
9. 反例黑名单：HTM|HTML|CGI|PHP|ASP|ASPX|JSP|JSPX 不当下载；
   application/json|x-javascript、text/javascript|plain 视为非文档。
10. 站点规则槽位（host 到规则集，Qb 用槽 101 匹配主机名）：槽 111/112 匹配
    URL 与路径查询串，case 1..6 分别定义下载/媒体/HTML 判定（含 M4S 分片、
    M3U8/F4M/MPD 流、F4F Seg1-Frag 分片、ASF 广告过滤 Content-Length<=100KB
    或 Server: DCLK-AdSvr）。
11. XHR 复测：可疑命中用 XMLHttpRequest 以 blob/text responseType 重新拉一遍
    验证；本地代理链路回执响应头 x-参考下载器-status（值为 3 表示确认）做终审。

返回码：4=下载接管，2=媒体接管（视频面板），5=待定/等待 HTML 页，1=放行，6=仅观察。

## 5. 下载链接的交付（MSG#13 到原生）

判定为接管后，Y() 构造 MSG#13 经 connectNative(com.tonec.参考下载器) -
原始程序 - 原始程序，载荷字段表（编号槽位）：

- [6] URL（含查询串，即签名下载链接）、[12] 状态行、[4] 推断扩展、
  [100] 文件名、[2] 服务器 IP/主机、[11] 全部请求头、[13] 响应头、
  [18] 补充头、[14] POST body、[55] Proxy-Authorization、[7] 标签页状态
- 头部元组 [id, portId, 时间戳, 状态码, 请求类别, 判定码, tabId, frameId]
- 标志位 q：IPv4/IPv6、页面来源、POST 等位标志
- Cookie 补全：请求头缺 Cookie 时用 cookies API 按 URL 取（permissions 含
  cookies），保证带登录态下载
- 原生终审：Options->File types 白名单（原始程序 的 COptFileTypes 类），可按类型拒绝

## 6. 会话重放：绕过浏览器禁止头

重放原请求（原 method/url/body）时，XHR 不允许设置 Host/Origin/Referer/
Cookie/User-Agent/Connection/DNT/Accept-Encoding/Sec-*（ma 正则）。
参考下载器 把这些头改名为 x-参考下载器-<name> 发出，再在 onBeforeSendHeaders（blocking）
里剥掉前缀还原为真实头并去重（含扩展自身 Origin 的还原）：
服务器看到的请求与浏览器原始预览请求逐头一致。这也是短时效签名 URL
能直接下载的原因。

## 7. 与 OpenDL 的对照（落地要点）

1. 监听面：types 必须含 sub_frame/object/xmlhttprequest；MV3 无 blocking 改写
   时用 declarativeNetRequest 的 modifyHeaders 等价实现。
2. 判定链照搬顺序：状态码 - MIME 白名单（表可直接从 hb 提取）-
   disposition/filename - URL 扩展比对 - 黑名单 - 站点规则 - 复测。
3. Accept 清洗与规则 id 关联可用普通扩展头实现（需 host_permissions）。
4. Cookie 补全用 chrome.cookies.get({url})（需 cookies 权限）。
5. x-参考下载器- 头还原技巧依赖 webRequestBlocking（MV2/Firefox 可用）。
6. 复测 XHR 与状态头回执的模式，可在桌面端代发请求的架构里用 loopback API
   返回判定代替（api.Server 已具备）。

## 证据索引

- entry117 manifest（权限/内容脚本/原生主机）
- background.js：zb（注册）、Cc/Ka（请求侧规则）、Cb（头改写+Accept 清洗+x-参考下载器- 还原）、
  Fb/nc（响应判定链）、Y（MSG#13 载荷）、sc（x-参考下载器-status 复测）、Ua（vnd.t1c 解析）、
  hb（MIME 表）、Qa/Pa（黑名单）、ma（禁止头正则）
- 原始程序：COptFileTypes（文件类型白名单）、notaskfn2、
  CCouldNotTakeOverOnHtml4Dlg（无法接管提示）
