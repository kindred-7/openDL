# IDM 多线程下载核心技术解构

样本: IDMan.exe (IDM 6.43.16.1, SHA-256 3b5dc9ce53eb31933effa3fd973bec068a78e0f8c1d15a919b081f0c4d9325ba,
由 idman643build16.exe 解包得到, 见 _extracted/entries/entry005_u6490992.exe)。
本文的每条机制都标注了样本中的字符串/符号证据(file offset)。

## 一、为什么多线程能比浏览器快: 理论

单条 HTTP 下载 = 一条 TCP 流。它的吞吐受 **每流(per-flow)** 限制:

    吞吐 ≤ min( 拥塞窗口 cwnd × MSS / RTT , 接收窗口 rwnd / RTT , 服务器每连接限速 )

1. **慢启动与丢包**: 一条流的 cwnd 从 10 MSS 起步, 丢一次包窗口腰斩;
   RTT 高的链路(跨国/移动)单流永远跑不满带宽。
2. **每连接限速**: 大量 CDN/网盘/下载站对**单连接**限速(如 2 MB/s),
   但不限总并发 —— 8 条连接合计可到 16 MB/s。
3. **浏览器的原生下载是单流**: "另存为"/下载管理器一个文件一条连接;
   HTTP/2 多路复用也只是一条 TCP 流内的多 stream, 丢包照样全流阻塞,
   且浏览器不使用 Range 并行。
4. **多连接的收益模型**:

       总吞吐 ≈ Σ 每连接吞吐, 上限 = 路径带宽 / 服务器总限额

   在每连接限速的网络下接近 N 倍; 千兆内网里 4-8 条即饱和, 再加只是开销。
   这也是 IDM 只默认 8 条、最高 16 的原因(证据: MaxConnectionsNumber)。

## 二、IDM 引擎解构(证据驱动)

### 1. 分块模型: ChunksList —— 引擎核心

字符串簇(0x305c5c..0x307c40)暴露了完整的分块管理类, 关键方法与职责:

| 方法/字符串 | offset | 职责 |
| --- | --- | --- |
| ChunksList::CreateNewItem() | 0x306560 | 创建新块(带文件名/内存分配) |
| ChunksList::InitFirst() | 0x30687c | 首块初始化(探测块) |
| ChunksList::FindMaxFreeChunk() | 0x305cb8 | **动态调度核心**: 找最大的未完成块 |
| ChunksList::NeedNewConnection() | 0x305ddc | **决定是否再开连接** |
| ChunksList::FindChunkWithZipCentralDir() | 0x305c5c | 按 ZIP 中央目录对齐块边界 |
| ChunksList::InsertFromReg() / EraseRegSubKey() | 0x3061e8 / 0x305d28 | **块状态持久化(注册表)→ 跨重启续传** |
| ChunksList::CheckAlternative() / ApplyAlternative() | 0x3066ec / 0x306340 | 备用地址/镜像切换 |
| ChunksList::AppendFile() | 0x3060ac | 合并阶段追加 |
| "Try rollback, set chunksize to %I64d" | 0x307174 | **块大小自适应回滚** |
| "The last chunk is full." / "This is the last chunk. End rebuilding." | 0x305f60 / 0x306f58 | 收尾/重组终止条件 |
| "Insert next chunk in list" | 0x306ab8 | 动态插入新块 |
| "Cannot rebuild downloaded file chunks." | 0x2f8040 | 重组失败路径 |
| "Strart data for this chunk is matched with start of the file!!!" | 0x307670 | 块边界数据校验(防错位合并) |

**动态分段调度**(IDM 快的关键): 不是固定等分到底 ——
空闲连接执行 FindMaxFreeChunk(), 把最大的未完成块**对半切**出一块新工作
("Insert next chunk in list"), 快连接自然多干活, 慢连接少背尾巴。
块过大则 "Try rollback, set chunksize" 自适应缩小。

### 2. HTTP Range 协议面

| 证据 | offset | 含义 |
| --- | --- | --- |
| "Range: bytes=%I64d-%I64d" | 0x30b648 | 中间块(闭区间) |
| "Range: bytes=%I64d-" | 0x30b630 | 尾块(开放结束) |
| "Range: bytes=0-%I64d" / "Range: bytes=0-" | 0x30b604/0x30b61c | 探测/首块 |
| "Content-Range:" | 0x30ac5c | 校验 206 应答 |
| "multipart/byteranges" | 0x30ad0c | 多段应答形态识别 |
| "If-Range:" + "ETag:" + "Last-Modified:" | 0x311158/0x30ac6c/0x30b740 | **续传一致性校验**(文件变了就整体重来) |
| "If-Modified-Since: %s" | 0x30b5d4 | 快速更新探测 |
| HTTPConnection::GetRetrRangeReply() | 0x30afa4 | **Range 重试** |
| "Connection: Keep-Alive" | 0x30b464 | 块内连接复用 |

### 3. 连接数策略

- MaxConnectionsNumber(0x2fb6a4) + CMaxConnNumbers/CAddMaxConnDlg(0x3c0304/0x3bc4e8):
  全局最大连接数 + **按站点例外**(Options->Connection, 对限连接的站点设 1)。
- 按链路类型的档位建议(0x2ec0a0..0x2ec138): 高速(以太网/4G)/中速(ADSL/3G)/
  低速(拨号/ISDN) 三档, 对应不同连接数默认值。
- 官方警告(0x2ebea8): "请谨慎使用高连接数, 某些服务器对单 IP 连接数有限制"。
- HTTPSession::TryStartNewOptionalConnections()(0x30c3d0) / FTPSession::
  CreateOptionalConnections()(0x30a328): **运行中按需追加连接**的调度入口。

### 4. 断点续传与容错

- 块状态入注册表(InsertFromReg), 崩溃/关机后从块粒度恢复, 不用重下整个文件。
- If-Range/ETag 校验保证续传的是同一个文件。
- 服务器不支持 Range 时的降级提示(0x2efa68): "服务器不支持 resume, 只能从头下载"。
- 连接断开自动重启: "Connection failed, restarting..." / "Connection timed out,
  restarting..."(0x2f8dc8/0x2f8dec)。
- 换地址续传(0x2ef1c8): 会话过期时提示用户重新点一次, 捕获新地址接着下。
- 备用镜像(CheckAlternative)。

### 5. 磁盘与重组

- 预建稀疏文件 + 按块偏移写(WriteAt), 各连接顺序写互不打架。
- 重组前检查磁盘空间("Not enough disk space to rebuild downloaded chunks." 0x305e14)。
- ZIP 特判(FindChunkWithZipCentralDir): 对 zip 按中央目录对齐块边界, 加速"边下边用"。
- 虚拟内存分配错误处理(0x2f7e58) —— 大文件用内存映射/大缓冲。

### 6. 附加能力(与速度协同)

- CSpeedLimiter + Session::CalcSpeed()(0x3c0f68/0x30ec14): 限速器与实时速率统计。
- 队列/计划(下载限时段限流量, 0x2f5900)。
- 浏览器接管保证拿到的是**最终直链**(带 Cookie/Referer/UA), 避免页面中转慢源。
- 站点登录表(免重复认证)、UA/Host 全套请求头构造(0x30b41c/0x30b410)。

## 三、与浏览器原生下载对照

| 维度 | 浏览器原生 | IDM |
| --- | --- | --- |
| 并发连接 | 1 文件 1 连接(HTTP/2 也单流) | 1 文件 8-16 连接(N 可调) |
| 分块调度 | 无 | ChunksList 动态分块 + 最大空闲块切分 |
| 每连接限速的站点 | 被限死 | 聚合 N 倍 |
| 断点续传 | 一般无(失败重来) | 块级续传 + If-Range 校验 + 自动重启连接 |
| 直链/会话 | 弱(下载器拿不到页面上下文) | 扩展捕获直链 + Cookie/Referer |
| 失败处理 | 整体重试 | 每块重试(GetRetrRangeReply) |
| 磁盘写入 | 流式追加 | 稀疏预建 + WriteAt 并行写 |
| 限速/调度 | 无 | 有 |

**注意**: 在服务器限总带宽或千兆内网, 多连接没有收益; 且部分服务器会拒绝/
限流多连接(429/403), 所以 IDM 才有按站点 1 连接的例外表。多线程是
"把每流瓶颈换成总瓶颈"的手段, 不是凭空加速。

## 四、工程配方(按此实现即可得到 IDM 级引擎)

1. **探测**: GET/HEAD 拿 Content-Length、Accept-Ranges、ETag/Last-Modified;
   不支持 Range → 单流下载。
2. **分块**: 块数 = min(连接数, size/minChunk); minChunk 建议 512KB-2MB
   (太小则请求开销占比高)。首块可小(快速出字节用于校验/预览)。
3. **动态调度**: 连接空闲 → 取最大的未完成块**对半切**(FindMaxFreeChunk);
   块小于 2×minChunk 就整块领走; 快连接自动多做。尾块用开放 Range 收口。
4. **校验**: 每块应答核对 Content-Range 与总长; 整文件用 ETag/If-Range 防换文件;
   合并前验证块连续无缝(strt 数据与起点匹配)。
5. **续传**: 块完成位图持久化(文件/注册表均可); 重启按块恢复; 用 If-Range 续传。
6. **容错**: 每块独立重试 2-3 次 + 指数退避; 连接超时重启; 服务器 429 时读
   retry-after 并全局限速; 连接数按站点可降级到 1。
7. **写盘**: 预建 .part(稀疏), WriteAt 各块区间, 完成后按序合并/改名;
   合并前检查磁盘剩余空间。
8. **请求头**: 每连接带 Cookie/Referer/User-Agent; Connection: Keep-Alive。

## 五、OpenDL 落地情况

- download/manager.go: 多连接 Range 分片(8 连接) + WriteAt 并行写 + .part 认领 + 取消
  ——已实现 1/2/7 的主体与 8 的部分。
- 本轮新增: **动态分段调度**(空闲连接对半切最大未完成块, 对应
  FindMaxFreeChunk + Insert next chunk in list)与每块一次重试。
- 待办: 块级续传持久化(5)、If-Range 校验(4 后半)、按站点连接例外(6)、
  限速器(CSpeedLimiter 等价物)、ZIP 中央目录对齐。
