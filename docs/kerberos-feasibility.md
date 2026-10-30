# Windows SSPI / Negotiate 接入

源码已接入 Windows 当前用户的 SSPI/Negotiate。运行结果仍需用户在 Windows 域环境手动确认；本次未构建、测试或启动应用。

## 配置

打开「设置 → 网络 → 系统集成认证」，填写完整站点地址，例如 `https://intranet.example.com`，选择策略后添加。相同地址再次添加会替换原策略。配置键仍使用 `windows_auth` 以保持兼容。

- **仅 Kerberos**：默认选项。在获取 Negotiate 凭据前使用 `SspiExcludePackage` 排除 NTLM；无可用 Kerberos 时失败，不自动使用账号密码或 NTLM。完成后仍检查实际认证包及 Kerberos 双向认证标志。
- **允许 NTLM 回退**：使用 Negotiate 由 Windows 优先协商 Kerberos，必要时允许 NTLM；服务器只有 NTLM 挑战时使用系统 NTLM SSP。
- **服务 SPN**：默认 `HTTP/请求主机`；可指定 `HTTP/服务名` 以适配已有域配置。客户端不会注册 SPN、修改域或系统策略。

按协议、主机和有效端口精确匹配：默认端口等价，HTTP/HTTPS 不等价，不包含子域名或其他端口。每个重定向目标独立匹配，集成认证令牌不会复制给重定向请求。未配置站点沿用现有 Cookie/Bearer/Basic/Digest/显式 NTLM 路径，不自动使用系统身份。

认证中的重定向响应必须先完成服务端令牌处理和协商协议检查，才能交给客户端继续跳转；仅 Kerberos 策略仍要求系统确认 Kerberos 及服务端身份验证。

配置站点优先使用系统身份，请求中的 Authorization 与 URL 用户信息不参与认证，保存的站点账号密码也不会注入。站点策略保存在偏好 `windows_auth` 中；不保存系统密码、票据或 SSPI 令牌。损坏的持久化策略会阻止网络请求并提示修正；保存有效设置后解除阻止，避免配置错误导致静默降级。配置修改作用于后续请求，正在进行的协商按已取得的策略结束。

## 连接与平台边界

每个请求使用独立 HTTP/1.1 连接与 SSPI 上下文，多轮协商共用现有连接限额；最终正文读完或关闭后释放连接。30 秒认证期限不限制后续文件正文传输。代价是并行分段需分别认证，暂未实现认证连接池。

HTTPS 使用经过验证的服务端证书生成 `tls-server-end-point` 通道绑定；不支持的签名算法报告错误，同一次认证中证书变化会中止。HTTP 仅在用户明确配置对应 HTTP 站点时使用，无 TLS 通道绑定。目标站点的 401 认证与代理 407/CONNECT 的集成认证已接入。Linux/macOS 可通过 cgo 和 `gssapi` 构建标签启用 GSSAPI 后端；独立 NTLM 要求系统机制产生原始 NTLMSSP 令牌，仍需用户手动构建及实机验证。

任务详情中的「最近认证」展示本次进程内最近一次成功系统认证得到的 Kerberos/NTLM；不是所有分段的协议汇总，也不代表全部内容来源均使用了同一身份。重启后不保留该状态。

Kerberos 依赖域/Realm、KDC、DNS、时钟和有效 HTTP 服务 SPN。Windows 默认通常不对 IP 地址使用 Kerberos，需要管理员配置的例外场景不由客户端自动处理。仅本地账号或只支持 NTLM 的服务器不能由客户端单方面改成 Kerberos。

## 留给用户的手动确认

1. 有效域票据与 HTTP SPN：两个策略均应显示 Kerberos，普通下载和多段下载正常。
2. SPN 不存在/KDC 不可用：仅 Kerberos 必须失败，不发送 NTLM；允许回退应遵循系统及服务器策略。
3. 仅 NTLM 服务器：仅 Kerberos 拒绝；允许回退显示 NTLM。
4. HTTPS 扩展保护：可信证书及通道绑定正常，证书更换/无效证书应失败。
5. 跨域、子域、端口与 HTTP/HTTPS 重定向：只对单独配置的目标使用系统身份，无令牌转发。
6. 暂停/继续、重启恢复、省略历史凭据的任务、资源同步与普通显式 NTLM：行为符合各自站点策略。
7. 非法持久化策略：应报告并阻止请求；在设置删除或修正后恢复。

## 依据

- [Microsoft Negotiate](https://learn.microsoft.com/en-us/windows/win32/secauthn/microsoft-negotiate)
- [Kerberos 概述](https://learn.microsoft.com/en-us/windows-server/security/kerberos/kerberos-authentication-overview)
- [SspiExcludePackage](https://learn.microsoft.com/en-us/windows/win32/api/sspi/nf-sspi-sspiexcludepackage) 与 [SDK 中可选输入身份声明](https://github.com/microsoft/win32metadata/blob/main/generation/WinSDK/RecompiledIdlHeaders/shared/sspi.h)
- [IP 地址与 Kerberos SPN](https://learn.microsoft.com/en-us/windows-server/security/kerberos/configuring-kerberos-over-ip)
- [RFC 5929：TLS 通道绑定](https://www.rfc-editor.org/rfc/rfc5929)
