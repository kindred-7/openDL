# 文档索引

- [功能对照](feature-overview.md)：与 参考下载器 的逐项对照表（现状、剩余、源码入口），结论以该表为准。
- [下载功能差距](download-gap-analysis.md)：下载引擎的实现边界与语义细节。
- [剩余功能推进记录](remaining-gaps-progress.md)：各能力的实现边界、格式/协议细节与格式化口径。
- [手动验证清单](manual-verification.md)：全部实机验证项与预期，结果回填对照表。
- [签名发布与应用更新](updates.md)：发布协议、发布工具（含 -verify 演练）与安装恢复流程。
- [回环接口](loopback-api.md)：本机控制接口的端点、错误语义与安全边界。
- [Windows 集成认证](kerberos-feasibility.md)：SSPI/Negotiate 的配置、匹配规则与连接边界。
- [浏览器扩展发布](extension-publishing.md)：商店发布准备、身份与上架后接入。
- `design.html`：界面参考模板（只读），样式实现以其为准。

## 设计与历史记录

- [架构](architecture.md)：浏览器嗅探到桌面下载的整体结构。
- [国际化](i18n.md)：词条组织与切换机制。
- [多线程下载解构](multithread-download.md)、[路径识别分析](path-recognition-analysis.md)：参考下载器 对应机制的分析，是引擎与 pathrec 的设计依据。
- [复刻规划](development-roadmap.md)：早期规划，**已由功能对照表取代**，仅作决策留档。
- [设计稿还原](design-implementation.md)、[设计规则](design-rules.md)、[浅色方案](design-3-notes.md)、[界面评估](ui-review.md)、[界面优化](ui-optimization.md)：界面过程记录。

所有文档描述源码现状，不含构建、测试或运行结论；实机确认一律按清单执行；结论冲突时以功能对照表为准。
