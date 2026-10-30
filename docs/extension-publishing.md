# 浏览器扩展正式发布

当前扩展尚无商店链接。发布者需要自行创建开发者账号、提交审核并维护发布身份；不要在应用中预填未经确认的链接。

## 准备发布包

1. 从 `extension` 制作干净的 ZIP：文件直接放在 ZIP 根目录，不要包含外层 `extension` 文件夹、测试文件、安装说明或其他非运行文件。保留清单引用的脚本、图标和 `_locales` 目录。
2. Chrome 和 Edge 使用 `manifest.json`；Firefox 使用单独副本，将 `manifest-firefox.json` 作为副本的 `manifest.json`，不要修改原文件。检查版本号、图标、本地化、权限和商店要求。
3. 正式版使用固定身份。Chromium 清单中的公钥对应原生主机当前允许的 ID `gfimibmmfmmhhjfioegmhbkmkhoefleo`；若商店分配的 ID 不同，必须同步调整 `nativehost/register_windows.go` 的允许来源和调用识别逻辑。Edge 与 Chrome 的 ID 可能不同，不能假设一致。Firefox 的 ID 为 `opendl@opendl.local`，也必须与原生主机允许列表一致。
4. 准备扩展介绍、截图、支持联系方式及隐私政策。扩展请求 Cookie、下载和所有网站访问权限；提交时应逐项说明用途及数据处理方式，与实际行为一致。

## 提交商店

- **Chrome**：注册 [Chrome Web Store 开发者账号](https://developer.chrome.com/docs/webstore/register)，在开发者控制台创建商品并上传 Chromium ZIP，填写商店资料、隐私披露和权限用途，提交审核。保留发布账号和签名身份。
- **Edge**：注册 [Microsoft Edge 开发者账号](https://learn.microsoft.com/en-us/microsoft-edge/extensions/publish/create-dev-account)，在 Partner Center 创建扩展并上传 Chromium ZIP，填写资料和隐私信息，提交审核。确认实际扩展 ID。
- **Firefox**：在 [Firefox Add-on 开发者中心](https://addons.mozilla.org/developers/)提交 Firefox ZIP，选择公开列出并完成审核及签名。也可以选择自行分发的签名渠道；普通 Firefox 正式版仍需要 Mozilla 签名。参见 [Mozilla 签名与分发说明](https://extensionworkshop.com/documentation/publish/signing-and-distribution-overview/)。

## 上架后接入

记录三个正式商店详情页、实际扩展 ID 和支持的浏览器版本；确认桌面端允许的来源覆盖每个实际 ID，再将安装引导改为打开对应商店页面，并保留无法识别浏览器时的选择入口。不要通过企业强制安装策略替用户安装。安装后由用户在扩展弹窗检查连接状态。

发布前由发布者手动检查各商店的最新政策、安装与更新流程，并在全新用户环境验证扩展权限、原生主机通信、卸载和升级。当前安装器完成页尚未提供独立的扩展安装按钮；启动 OpenDL 后会提示打开安装说明，也可从应用菜单再次打开。
