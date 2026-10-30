# Lucide 图标来源

- 上游：https://github.com/lucide-icons/lucide
- 固定提交：`03f11e2d6adc319ea21e9a1d5820a99cd3767257`
- `icons/*.svg` 为从该提交下载的官方原始 SVG；授权见同目录 `LICENSE`。
- 更新来源：手动运行 `python scripts/vendor-lucide.py`，脚本只下载资源，不构建、不测试、不生成预览。
- 应用通过 `style.Icon` 统一使用这些资源，以 `style.StrokeIcon` 设置描边，通过语义色设置亮暗主题颜色；旧名称集中映射到对应 Lucide 图标，不维护自绘路径。
- 未知名称统一显示 Lucide `file`，不回退到系统图标或其他图标库。音频使用 `headphones`，删除使用 `trash`。
- 表格文件图标按最新要求优先使用系统图标，提取失败时才使用这里的 Lucide 文件类型图标；其他界面图标仍统一 Lucide。应用运行时不访问网络下载图标。
