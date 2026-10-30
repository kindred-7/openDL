# 参考下载器 6.43.16 路径识别机制分析与 MyGo 重构

样本: 原始安装样本 (Tonec, 参考下载器 6.43.16.1 安装器, SHA-256
276aa7e4ff7682445b011ebe6ac3920e7eca6ece7d3f6723b1f1cb515164461b)。
主程序 原始程序 (SHA-256 3b5dc9ce53eb31933effa3fd973bec068a78e0f8c1d15a919b081f0c4d9325ba)
由安装器 payload 解出: payload 为"32 字节头 + zlib 流"的顺序条目, 共 192 个条目
(解包脚本见 _extracted/, 条目在 _extracted/entries/)。

## 为什么 参考下载器 能识别 fluxdown 认不到的"特殊路径"

参考下载器 的识别不是一层, 而是三层叠加。fluxdown 这类常规下载器只有"标准 URL 解析 +
path.Base 提名"一层, 所以以下路径它直接丢弃或截断:

| 路径样例 | fluxdown (严格 RFC 3986) | 参考下载器 |
| --- | --- | --- |
| https://mirror.example.cn/下载/年度报告.zip | 非 ASCII host/path, 匹配失败或截断 | 正常识别, 文件名"年度报告.zip" |
| https://files.example.com/Q3 Report;v=2.pdf | 在空格处截断 | 整段保留, ; 参数保留 |
| https://x.org/dl/My%20Report%20(2024).tar.gz | 括号/编码处理不一致 | UrlUnescape 还原 "My Report (2024).tar.gz" |
| https://example.net/~user/soft;v=1/setup_x64.exe | ;matrix 参数被当查询 | 完整路径 + 文件名正确 |
| https://shop.example.com/order/download.php?id=88 | 文件名 download.php | 优先 Content-Disposition / 页面标题 |
| https://web.whatsapp.com/res/a.mp3 | 当普通下载弹窗 | 命中特殊路径掩码, 不进下载流 |

### L1 浏览器侧 URL 提取器(内容脚本)

扩展 content.js 的 URL 提取正则(字符类 %H/%T/%Q 展开后):

    \b\w+://(?:[T]*(?::[T]*)?@)?[H.]+\.[H]+(?::\d+)?(?:/(?:(?: +(?!\w+:))?[T/~;])*)?(?:\?[Q]*)?(?:#[T]*)?

- %H = [\w\- -\uFFFF]   (host 允许空格、全部 BMP 字符)
- %T = [\w\-.+*()$!,% -\uFFFF]  (path 允许空格、+*()$!逗号、unicode)
- %Q = [^\s\[\]{}()]    (query 只排除空白和括号)

关键点: path 段每轮允许"一串空格 + 一个字符", 空格后只要不是"单词+冒号"
(lookahead +(?!\w+:))就继续。因此空格、分号、波浪号、中文、() 等
"非法 URL 字符"都在 URL 里。普通工具用 net/url 或 [A-Za-z0-9-_.~/?=&%]
字符集, 这些路径要么整个丢弃, 要么在第一个空格/分号处截断。
重构: pathrec/extract.go(lookahead 以事后修剪 + 散文尾修剪重现)。

### L2 特殊路径掩码表 + 站点规则

- 掩码表: 安装器 entry044, 89 条 glob(URL 掩码), 如
  https://web.whatsapp.com/res/*.mp3、http://ad.*.com/st?ad_type=*、
  http://*.download.windowsupdate.com/*。
  命中者是"特殊路径": 广告、游戏音效、更新包等后台资源, 参考下载器 特殊处理
  (不弹下载面板/不接管)。这就是"识别到一些特殊的路径"的字面来源。
  重构: pathrec/mask.go + masks.txt(原表逐条提取)。
- 站点规则: content.js 的规则装载(Fc, case 1..5)由原生侧下发
  host 正则 + path 正则 + 扩展名表; 规则里的 "^/" 会被展开成
  "^(?:https?://<host>)?/", 同时匹配裸路径和完整 URL——
  这是 参考下载器 识别"无扩展名下载路径"(如 /watch?v=、/file/d/)的机制。
  重构: pathrec/rule.go(ExpandPathPattern 逐行对应 content.js 实现)。

### L3 文件名推导(原始程序 头部解析)

原始程序 头部解析字符串簇(file 0x30b740..0x30b880, VA 0x70cb40 附近):

    utf-8' ' | utf-8'' | windows-125'' | ISO-8859- | ISO-8859''
    =?UTF8?B? | =?UTF-8? | filename | inline; | attachment | Content-Disposition:

- "Content-Disposition:"(VA 0x70cc44)在 .text 的引用:
  file 0x18cb6e 处 6a 14 68 44 cc 70 00 = push 20; push 0x70cc44 —
  以长度 20 做头部名比较, 位于 ProcessHeader 一带。
- "filename"(VA 0x70cc24)有 5 处引用: 0x18cc30 / 0x18cc67 / 0x18cdb8 /
  0x18d0fa / 0x18d12e, 与上面同一函数区间。
- RFC 5987: filename*=charset'lang'pct-encoded(utf-8''/windows-1252''/
  ISO-8859-1'' 前缀即解析器的字符集白名单);
- RFC 2047: =?UTF-8?B?..?=/=?UTF-8?Q?..?= 编码词;
- 字符集回退: UTF-8 非法时按 windows-1252 / ISO-8859-1 解字节;
- 其他相关导入: UrlUnescapeA/W(百分号解码)、PathFindFileNameA(取末段)、
  InternetCrackUrlA(拆 URL)。
  重构: pathrec/filename.go(FromContentDisposition / FromURLPath /
  CleanTitle / sanitizeWindows)。

## 与 fluxdown 的差异汇总

1. 词法层: fluxdown 用标准 URL 语法, 参考下载器 用 %H/%T/%Q 宽字符类 + 空格容忍
   + lookahead 截断 —— 后者认得前者丢弃的路径。
2. 规则层: fluxdown 无掩码表、无站点规则, 只能靠扩展名猜下载;
   参考下载器 有 89 条特殊路径掩码 + 可下发的站点 path 规则(无扩展名也能识别)。
3. 命名层: fluxdown 多为 path.Base(u.Path) 直接提名;
   参考下载器 走 Content-Disposition(RFC 5987/2047)→ URL 末段(百分号+字符集
   解码)→ 页面标题(清洗)→ MIME 兜底, 并做 Windows 保留名/非法字符处理。
4. 标题清洗: content.js 的标题正则 ^(?:\(\d+\)\s+)?(.+?)\s+[-|@]+\s*<站点>$
   去掉浏览器角标和站名尾巴 —— fluxdown 无此步骤。

## MyGo 重构映射

- pathrec/extract.go   ← content.js URL 提取正则(含散文尾修剪)
- pathrec/mask.go      ← 安装器 entry044 掩码表
- pathrec/masks.txt    ← 89 条掩码原样提取
- pathrec/rule.go      ← content.js 站点规则与 ^/ 展开
- pathrec/filename.go  ← 原始程序 头部解析簇(RFC 5987/2047/CP1252/消毒)
- pathrec/recognize.go ← 三层管线汇总 Recognize()
- main.go / main_test.go ← MyGo UI: 粘贴文本 → 识别 → 候选列表
- pathrec/pathrec_test.go ← 特殊路径回归样例

验证: go tool mygo vet . 通过; CGO_ENABLED=0 go test ./... 通过。

## 证据与未决项

- 条目提取: _extracted/entries/(192 项), 原始程序 = entry005_u6490992.exe。
- 字符串与引用证据: 见上文 file offset / VA / 机器码字节。
- REA Ghidra 反编译 ProcessHeader 函数尚未取得: 6.5MB x86 自动分析超过
  MCP 客户端单次调用时限(mcp_servers.<server>.tool_timeout_sec), 静态字符串/
  引用证据已足够支撑上述结论; 需要伪代码级证据时提高该时限后重跑
  analyze_function(入口区间 0x18cb56..0x18d200 一带)。
- fluxdown 源码不在本工作区; 差异按行为类别(严格 RFC 解析型下载器)对比。
  把 fluxdown 源码放进工作区后, 可用同一套 pathrec 测试逐条对拍。
