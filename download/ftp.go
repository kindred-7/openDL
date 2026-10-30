package download

// FTP 支持：单连接流式下载，REST 从检查点续传，SIZE/MDTM 作为弱版本标识。
// FTPS、代理链路与多连接分段不在本实现范围；任务记录不保存登录口令。

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"openDL/pathrec"

	"openDL/i18n"
)

const ftpCommandTimeout = 30 * time.Second

// ftpReply 是一次命令的最终应答（含多行应答的末行）。
type ftpReply struct {
	code int
	text string
}

type ftpConn struct {
	control net.Conn
	reader  *bufio.Reader
	secure  bool // PROT P：数据通道同样使用 TLS
}

// wrapFTPTLS 用 TLS 包装一条 FTP 连接并完成握手；证书校验失败明确报错。
func wrapFTPTLS(ctx context.Context, conn net.Conn, serverName string) (net.Conn, error) {
	client := tls.Client(conn, &tls.Config{ServerName: serverName, MinVersion: tls.VersionTLS12})
	if err := client.HandshakeContext(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf(i18n.T("FTPS 证书或 TLS 握手失败：%w"), err)
	}
	return client, nil
}

func (c *ftpConn) read() (ftpReply, error) {
	line, err := c.reader.ReadString('\n')
	if err != nil {
		return ftpReply{}, fmt.Errorf(i18n.T("读取 FTP 应答失败：%w"), err)
	}
	line = strings.TrimRight(line, "\r\n")
	if len(line) < 4 || (line[3] != ' ' && line[3] != '-') {
		return ftpReply{}, fmt.Errorf(i18n.T("FTP 应答格式无效"))
	}
	code, err := strconv.Atoi(line[:3])
	if err != nil {
		return ftpReply{}, fmt.Errorf(i18n.T("FTP 应答格式无效"))
	}
	text := line[4:]
	for line[3] == '-' {
		line, err = c.reader.ReadString('\n')
		if err != nil {
			return ftpReply{}, fmt.Errorf(i18n.T("读取 FTP 应答失败：%w"), err)
		}
		line = strings.TrimRight(line, "\r\n")
		if len(line) >= 4 && line[3] == ' ' {
			if same, convErr := strconv.Atoi(line[:3]); convErr == nil && same == code {
				text = line[4:]
				break
			}
		}
	}
	return ftpReply{code: code, text: text}, nil
}

func (c *ftpConn) command(format string, args ...any) (ftpReply, error) {
	line := fmt.Sprintf(format, args...)
	if strings.ContainsAny(line, "\r\n") {
		return ftpReply{}, fmt.Errorf(i18n.T("FTP 命令参数无效"))
	}
	if err := c.control.SetDeadline(time.Now().Add(ftpCommandTimeout)); err != nil {
		return ftpReply{}, fmt.Errorf(i18n.T("设置 FTP 超时失败：%w"), err)
	}
	if _, err := io.WriteString(c.control, line+"\r\n"); err != nil {
		return ftpReply{}, fmt.Errorf(i18n.T("发送 FTP 命令失败：%w"), err)
	}
	return c.read()
}

func (c *ftpConn) login(user, password string) error {
	reply, err := c.command("USER %s", user)
	if err != nil {
		return err
	}
	switch reply.code {
	case 230:
		return nil
	case 331, 332:
	default:
		return fmt.Errorf(i18n.T("FTP 登录被拒绝：%d %s"), reply.code, reply.text)
	}
	reply, err = c.command("PASS %s", password)
	if err != nil {
		return err
	}
	if reply.code != 230 {
		return fmt.Errorf(i18n.T("FTP 登录被拒绝：%d %s"), reply.code, reply.text)
	}
	return nil
}

// passive 返回被动模式的数据地址，优先 EPSV。
func (c *ftpConn) passive() (string, error) {
	host, _, err := net.SplitHostPort(c.control.RemoteAddr().String())
	if err != nil {
		return "", fmt.Errorf(i18n.T("无法确认 FTP 服务器地址"))
	}
	if reply, err := c.command("EPSV"); err == nil && reply.code == 229 {
		startAt, endAt := strings.IndexByte(reply.text, '('), strings.LastIndexByte(reply.text, ')')
		if startAt >= 0 && endAt > startAt+1 {
			fields := strings.Split(reply.text[startAt+1:endAt], "|")
			if len(fields) == 5 && fields[4] != "" {
				if port, err := strconv.Atoi(fields[4]); err == nil && port > 0 && port < 65536 {
					return net.JoinHostPort(host, strconv.Itoa(port)), nil
				}
			}
		}
	}
	reply, err := c.command("PASV")
	if err != nil {
		return "", err
	}
	if reply.code != 227 {
		return "", fmt.Errorf(i18n.T("FTP 服务器不支持被动模式：%d %s"), reply.code, reply.text)
	}
	startAt, endAt := strings.IndexByte(reply.text, '('), strings.LastIndexByte(reply.text, ')')
	if startAt < 0 || endAt <= startAt+1 {
		return "", fmt.Errorf(i18n.T("FTP 被动模式应答无效"))
	}
	fields := strings.Split(reply.text[startAt+1:endAt], ",")
	if len(fields) != 6 {
		return "", fmt.Errorf(i18n.T("FTP 被动模式应答无效"))
	}
	numbers := make([]int, 6)
	for i, field := range fields {
		number, err := strconv.Atoi(strings.TrimSpace(field))
		if err != nil || number < 0 || number > 255 {
			return "", fmt.Errorf(i18n.T("FTP 被动模式应答无效"))
		}
		numbers[i] = number
	}
	address := fmt.Sprintf("%d.%d.%d.%d", numbers[0], numbers[1], numbers[2], numbers[3])
	return net.JoinHostPort(address, strconv.Itoa(numbers[4]*256+numbers[5])), nil
}

// secureHandshake 在显式 FTPS 下升级控制连接并保护数据通道。
func (c *ftpConn) secureHandshake(ctx context.Context, serverName string) error {
	reply, err := c.command("AUTH TLS")
	if err != nil {
		return err
	}
	if reply.code != 234 && reply.code != 334 {
		return fmt.Errorf(i18n.T("FTP 服务器拒绝 TLS：%d %s"), reply.code, reply.text)
	}
	conn, err := wrapFTPTLS(ctx, c.control, serverName)
	if err != nil {
		return err
	}
	c.control, c.reader = conn, bufio.NewReader(conn)
	for _, step := range []string{"PBSZ 0", "PROT P"} {
		if reply, err := c.command(step); err != nil {
			return err
		} else if reply.code != 200 {
			return fmt.Errorf(i18n.T("FTP 服务器拒绝加密数据通道：%d %s"), reply.code, reply.text)
		}
	}
	c.secure = true
	return nil
}

// ftpSource 是一次 FTP 传输的连接参数，供各分段连接复用。
type ftpSource struct {
	address     *url.URL
	host        string
	path        string
	user        string
	password    string
	implicitTLS bool
	explicitTLS bool
}

// downloadFTPRange 用独立 FTP 控制连接取回一个区间：REST 从区间起点读到本
// 连接结束，只收前 size 字节；MDTM 与校验不符时报资源变化。
func (m *Manager) downloadFTPRange(t *Task, source ftpSource, out *os.File, start, end int64, validator string, segment *segmentProgress, splitCtx context.Context) (int64, error) {
	if splitCtx == nil {
		splitCtx = context.Background()
	}
	release, err := m.limiter.acquire(splitCtx)
	if err != nil {
		return 0, err
	}
	defer release()
	ctx, cancel := context.WithCancel(splitCtx)
	defer cancel()
	var dialer net.Dialer
	control, err := dialer.DialContext(ctx, "tcp", source.host)
	if err != nil {
		return 0, &responseReadError{err: err}
	}
	defer func() { _ = control.Close() }()
	if source.implicitTLS {
		if control, err = wrapFTPTLS(ctx, control, source.address.Hostname()); err != nil {
			return 0, err
		}
	}
	ftp := &ftpConn{control: control, reader: bufio.NewReader(control), secure: source.implicitTLS}
	welcome, err := ftp.read()
	if err != nil {
		return 0, &responseReadError{err: err}
	}
	if welcome.code != 220 {
		return 0, fmt.Errorf(i18n.T("FTP 服务器拒绝连接：%d %s"), welcome.code, welcome.text)
	}
	if source.explicitTLS {
		if err := ftp.secureHandshake(ctx, source.address.Hostname()); err != nil {
			return 0, err
		}
	}
	if err := ftp.login(source.user, source.password); err != nil {
		return 0, err
	}
	if reply, err := ftp.command("TYPE I"); err != nil {
		return 0, err
	} else if reply.code != 200 {
		return 0, fmt.Errorf(i18n.T("FTP 服务器拒绝二进制传输：%d %s"), reply.code, reply.text)
	}
	if stamp, ok := ftp.modified(source.path); ok && validator != "" && stamp != validator {
		return 0, fmt.Errorf(i18n.T("下载期间服务器文件已改变"))
	}
	size := end - start + 1
	if reply, err := ftp.command("REST %d", start); err != nil {
		return 0, err
	} else if reply.code != 350 {
		return 0, fmt.Errorf(i18n.T("FTP 服务器不支持从检查点续传"))
	}
	dataAddress, err := ftp.passive()
	if err != nil {
		return 0, err
	}
	data, err := dialer.DialContext(ctx, "tcp", dataAddress)
	if err != nil {
		return 0, &responseReadError{err: err}
	}
	defer data.Close()
	if ftp.secure {
		if data, err = wrapFTPTLS(ctx, data, source.address.Hostname()); err != nil {
			return 0, err
		}
	}
	reply, err := ftp.command("RETR %s", source.path)
	if err != nil {
		return 0, err
	}
	if reply.code != 150 && reply.code != 125 {
		return 0, fmt.Errorf(i18n.T("FTP 取回文件失败：%d %s"), reply.code, reply.text)
	}
	reader := idleReader{body: data, timeout: m.bodyIdleTimeout, cancel: cancel}
	// FTP 的 REST 读到文件末尾；只收本区间长度后关闭数据连接。
	written, err := m.copyWithProgressCancel(t, &offsetWriter{f: out, off: start, segment: segment}, io.LimitReader(reader, size), splitCtx.Done())
	if err == nil && written != size {
		err = &responseReadError{err: fmt.Errorf(i18n.T("分段数据不完整：%d，预期 %d: %w"), written, size, io.ErrUnexpectedEOF)}
	}
	_, _ = ftp.command("QUIT")
	return written, err
}

// size 返回远端文件大小；不可用时报告 false。
func (c *ftpConn) size(path string) (int64, bool) {
	reply, err := c.command("SIZE %s", path)
	if err != nil || reply.code != 213 {
		return 0, false
	}
	total, err := strconv.ParseInt(strings.TrimSpace(reply.text), 10, 64)
	if err != nil || total < 0 {
		return 0, false
	}
	return total, true
}

// modified 返回远端修改时间，作为弱版本标识；不可用时报告 false。
func (c *ftpConn) modified(path string) (string, bool) {
	reply, err := c.command("MDTM %s", path)
	if err != nil || reply.code != 213 {
		return "", false
	}
	stamp := strings.TrimSpace(reply.text)
	if len(stamp) != 14 {
		return "", false
	}
	return stamp, true
}

// downloadFTP 下载 ftp:// 任务。REST 从检查点续传；远端修改时间或大小变
// 化时从头下载，避免把不一致的内容拼接在一起。
func (m *Manager) downloadFTP(t *Task) error {
	address, err := url.Parse(t.sourceURL())
	if err != nil || address.Hostname() == "" {
		return fmt.Errorf(i18n.T("FTP 地址无效"))
	}
	scheme := strings.ToLower(address.Scheme)
	if scheme != "ftp" && scheme != "ftps" {
		return fmt.Errorf(i18n.T("FTP 地址无效"))
	}
	path := strings.TrimPrefix(address.Path, "/")
	if path == "" || strings.ContainsAny(path, "\r\n") {
		return fmt.Errorf(i18n.T("FTP 路径无效"))
	}
	user, password := "anonymous", "opendl@"
	if address.User != nil {
		user = address.User.Username()
		password, _ = address.User.Password()
	}
	if auth := t.headers["Authorization"]; strings.HasPrefix(auth, "Basic ") {
		if raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(auth, "Basic ")); err == nil {
			if name, pass, ok := strings.Cut(string(raw), ":"); ok {
				user, password = name, pass
			}
		}
	}
	// ftps:// 默认按隐式 TLS（端口 990，与 curl 一致）；显式指定端口时
	// 用 AUTH TLS 升级同一条控制连接。
	implicitTLS := scheme == "ftps" && (address.Port() == "" || address.Port() == "990")
	defaultPort := "21"
	if implicitTLS {
		defaultPort = "990"
	}
	host := net.JoinHostPort(address.Hostname(), address.Port())
	if address.Port() == "" {
		host = net.JoinHostPort(address.Hostname(), defaultPort)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-t.cancel:
			cancel()
		case <-ctx.Done():
		}
	}()
	var dialer net.Dialer
	control, err := dialer.DialContext(ctx, "tcp", host)
	if err != nil {
		return fmt.Errorf(i18n.T("连接 FTP 服务器失败：%w"), err)
	}
	defer func() { _ = control.Close() }()
	if implicitTLS {
		if control, err = wrapFTPTLS(ctx, control, address.Hostname()); err != nil {
			return err
		}
	}
	ftp := &ftpConn{control: control, reader: bufio.NewReader(control), secure: implicitTLS}
	welcome, err := ftp.read()
	if err != nil {
		return err
	}
	if welcome.code != 220 {
		return fmt.Errorf(i18n.T("FTP 服务器拒绝连接：%d %s"), welcome.code, welcome.text)
	}
	if scheme == "ftps" && !implicitTLS {
		if err := ftp.secureHandshake(ctx, address.Hostname()); err != nil {
			return err
		}
	}
	if err := ftp.login(user, password); err != nil {
		return err
	}
	if reply, err := ftp.command("TYPE I"); err != nil {
		return err
	} else if reply.code != 200 {
		return fmt.Errorf(i18n.T("FTP 服务器拒绝二进制传输：%d %s"), reply.code, reply.text)
	}
	total := int64(-1)
	if size, ok := ftp.size(path); ok {
		total = size
	}
	validator := ""
	if stamp, ok := ftp.modified(path); ok {
		validator = stamp
	}
	originalName := pathrec.DeriveFilename(pathrec.Source{URL: t.sourceURL(), Title: t.title})
	name := t.nameHint
	if name == "" {
		name = originalName
	} else if t.keepExtension {
		name = preserveExtension(name, originalName)
	}
	name = pathrec.SanitizeFilename(name)
	if err := os.MkdirAll(t.directory, 0o755); err != nil {
		return err
	}
	t.metaMu.Lock()
	filePath := t.filePath
	t.metaMu.Unlock()
	part := filePath + ".part"
	if filePath == "" {
		filePath, part, err = claimPath(t.directory, name)
		if err != nil {
			return err
		}
		t.metaMu.Lock()
		t.filePath = filePath
		t.metaMu.Unlock()
	}
	// 有可靠版本标识的已知大小文件走动态分段，与 HTTP 范围共享同一驱动；
	// 其余场景单连接流式下载，REST 从检查点续传。
	if total > 0 && validator != "" && (t.conns > 1 || t.conns == -1) {
		source := ftpSource{
			address:     address,
			host:        host,
			path:        path,
			user:        user,
			password:    password,
			implicitTLS: implicitTLS,
			explicitTLS: scheme == "ftps" && !implicitTLS,
		}
		return m.downloadSplit(t, part, total, validator, func(out *os.File, start, end int64, validator string, segment *segmentProgress, splitCtx context.Context) (int64, error) {
			return m.downloadFTPRange(t, source, out, start, end, validator, segment, splitCtx)
		})
	}
	out, err := os.OpenFile(extendedPath(part), os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	if err := m.prepareOutputLimit(t, out, total); err != nil {
		return err
	}
	t.checkpointMu.Lock()
	savedValidator := t.resumeValidator
	t.checkpointMu.Unlock()
	offset := int64(0)
	if info, err := out.Stat(); err == nil && info.Size() > 0 {
		if validator != "" && validator == savedValidator && (total < 0 || info.Size() <= total) {
			offset = info.Size()
		} else if err := out.Truncate(0); err != nil {
			return err
		}
	}
	t.total.Store(total)
	t.done.Store(offset)
	t.rangeSupport.Store(0)
	t.resumeSafe.Store(false)
	t.resetSegments(nil)
	progress := t.startSegment(chunkSpan{start: 0, end: -1})
	progress.done.Store(offset)
	finished := false
	defer func() {
		if !finished {
			t.removeSegment(progress)
		}
	}()
	if offset > 0 {
		if reply, err := ftp.command("REST %d", offset); err != nil {
			return err
		} else if reply.code != 350 {
			return fmt.Errorf(i18n.T("FTP 服务器不支持从检查点续传"))
		}
	}
	dataAddress, err := ftp.passive()
	if err != nil {
		return err
	}
	data, err := dialer.DialContext(ctx, "tcp", dataAddress)
	if err != nil {
		return fmt.Errorf(i18n.T("连接 FTP 数据通道失败：%w"), err)
	}
	if ftp.secure {
		if data, err = wrapFTPTLS(ctx, data, address.Hostname()); err != nil {
			return err
		}
	}
	defer data.Close()
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		select {
		case <-t.cancel:
			data.Close()
			control.Close()
		case <-stopped:
		}
	}()
	reply, err := ftp.command("RETR %s", path)
	if err != nil {
		return err
	}
	if reply.code != 150 && reply.code != 125 {
		return fmt.Errorf(i18n.T("FTP 取回文件失败：%d %s"), reply.code, reply.text)
	}
	if err := out.Truncate(offset); err != nil {
		return err
	}
	if _, err := out.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	reader := idleReader{body: data, timeout: m.bodyIdleTimeout, cancel: cancel}
	if _, err := m.copyWithProgress(t, out, reader); err != nil {
		select {
		case <-t.cancel:
			return contextCanceled
		default:
		}
		return describeStorageError(err)
	}
	if err := out.Sync(); err != nil {
		return fmt.Errorf(i18n.T("无法同步下载文件：%w"), err)
	}
	_, _ = ftp.command("QUIT")
	if err := out.Close(); err != nil {
		return err
	}
	t.checkpointMu.Lock()
	t.resumeValidator = validator
	t.resumeTotal, t.resumeResource = total, resourceFingerprint(address)
	saveErr := m.saveTaskLocked(t)
	t.checkpointMu.Unlock()
	if saveErr != nil {
		return fmt.Errorf(i18n.T("无法保存下载记录：%w"), saveErr)
	}
	written := t.done.Load()
	t.finishStream(progress, written)
	finished = true
	t.total.Store(written)
	return m.finishFile(t, part)
}
