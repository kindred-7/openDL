// Command idmhost is the browser native messaging host (the counterpart of
// IDM's idmcchandler2_64.dll): it reads length-prefixed JSON from the
// extension on stdio and forwards each download order to the OpenDL desktop
// app over the loopback API.
package nativehost

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"openDL/i18n"
	"openDL/pathrec"
)

const apiBase = "http://127.0.0.1:37913"

// message is what the extension sends.
type message struct {
	RequestID       string `json:"requestId,omitempty"`
	Type            string `json:"type"`
	URL             string `json:"url,omitempty"`
	PageURL         string `json:"pageUrl,omitempty"`
	Title           string `json:"title,omitempty"`
	Filename        string `json:"filename,omitempty"`
	Cookie          string `json:"cookie,omitempty"`
	MediaMode       string `json:"mediaMode,omitempty"`
	MediaQuality    string `json:"mediaQuality,omitempty"`
	AudioLanguage   string `json:"audioLanguage,omitempty"`
	Queue           string `json:"queue,omitempty"`
	ClientRequestID string `json:"clientRequestId,omitempty"`
	ID              string `json:"id,omitempty"`
	Folder          bool   `json:"folder,omitempty"`
	Before          string `json:"before,omitempty"`
}

func Run() {
	refreshLanguage()
	client := &http.Client{Timeout: 15 * time.Second}
	_ = serveNative(os.Stdin, os.Stdout, client, readToken)
}

// Handle independent requests concurrently so a slow desktop response does
// not hold up unrelated downloads. Output frames must remain serialized.
func serveNative(input io.Reader, output io.Writer, client *http.Client, token func() string) error {
	const maxConcurrent = 16
	slots := make(chan struct{}, maxConcurrent)
	var outputMu sync.Mutex
	var workers sync.WaitGroup
	defer workers.Wait()
	writeReply := func(reply map[string]any) error {
		outputMu.Lock()
		defer outputMu.Unlock()
		return writeMessage(output, reply)
	}
	for {
		msg, err := readMessage(input)
		if err != nil {
			return err
		}
		refreshLanguage()
		if msg.Type == "ping" {
			if err := writeReply(handleMessage(client, "", msg)); err != nil {
				return err
			}
			continue
		}
		select {
		case slots <- struct{}{}:
			workers.Add(1)
			go func(msg message) {
				defer workers.Done()
				defer func() { <-slots }()
				// Read the current token for every request; the app rotates it.
				_ = writeReply(handleMessage(client, token(), msg))
			}(msg)
		default:
			reply := map[string]any{"ok": false, "reason": i18n.T("原生主机繁忙，请稍后重试")}
			if msg.RequestID != "" {
				reply["requestId"] = msg.RequestID
			}
			if err := writeReply(reply); err != nil {
				return err
			}
		}
	}
}

// loadSiteRules 读取站点规则（本地文件与远程规则源缓存合并，本地优先）；每次请求都重读，修改文件即
// 生效，无需重启浏览器或应用。
func loadSiteRules() []pathrec.SiteRule {
	path, err := pathrec.SiteRulesPath()
	if err != nil {
		return nil
	}
	return pathrec.LoadSiteRulesWithRemote(path)
}

func handleMessage(client *http.Client, token string, msg message) map[string]any {
	reply := handle(client, token, msg)
	if msg.RequestID != "" {
		reply["requestId"] = msg.RequestID
	}
	return reply
}

func handle(client *http.Client, token string, msg message) map[string]any {
	switch msg.Type {
	case "ping":
		return map[string]any{"ok": true, "app": "opendl-host", "version": "0.1.0", "capabilities": []string{"scheduled-queue", "idempotent-submission", "site-rules", "media-hls", "media-dash", "media-quality", "media-audio-language", "download-prompt", "browser-tasks", "browser-tasks-v2"}, "siteRules": loadSiteRules()}
	case "desktopStatus":
		info, err := desktopCapabilities(client, token, nil)
		if err != nil {
			return map[string]any{"ok": false}
		}
		return map[string]any{"ok": true, "theme": info.Theme}
	case "siteRules":
		return map[string]any{"ok": true, "rules": loadSiteRules()}
	case "browserTasks", "openBrowserTask":
		method, endpoint := http.MethodGet, "/api/browser-tasks-v2"
		if msg.Type == "browserTasks" && msg.Before != "" {
			if len(msg.Before) > 128 {
				return map[string]any{"ok": false, "reason": "invalid task cursor"}
			}
			endpoint += "?before=" + url.QueryEscape(msg.Before)
		}
		var body io.Reader
		if msg.Type == "openBrowserTask" {
			if msg.ID == "" || len(msg.ID) > 128 {
				return map[string]any{"ok": false, "reason": "invalid task"}
			}
			method, endpoint = http.MethodPost, "/api/browser-task-open"
			encoded, _ := json.Marshal(map[string]any{"id": msg.ID, "folder": msg.Folder})
			body = bytes.NewReader(encoded)
		}
		req, err := http.NewRequest(method, apiBase+endpoint, body)
		if err != nil {
			return map[string]any{"ok": false, "reason": err.Error()}
		}
		req.Header.Set("X-OpenDL-Token", token)
		resp, err := client.Do(req)
		if err != nil {
			return map[string]any{"ok": false, "reason": err.Error()}
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			if msg.Type == "browserTasks" && resp.StatusCode == http.StatusNotFound {
				return map[string]any{"ok": false, "code": "desktop_unsupported", "reason": i18n.T("桌面端版本不支持该下载请求，请更新桌面应用")}
			}
			if resp.StatusCode == http.StatusUnauthorized {
				return map[string]any{"ok": false, "code": "desktop_unauthorized", "reason": i18n.T("桌面端认证失败，请重新启动扩展和桌面应用")}
			}
			if msg.Type == "browserTasks" && resp.StatusCode == http.StatusConflict {
				return map[string]any{"ok": false, "code": "task_cursor_expired"}
			}
			return map[string]any{"ok": false, "reason": fmt.Sprintf("HTTP %d", resp.StatusCode)}
		}
		if msg.Type == "openBrowserTask" {
			return map[string]any{"ok": true}
		}
		var page struct {
			Tasks json.RawMessage `json:"tasks"`
			Next  string          `json:"next"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&page); err != nil {
			return map[string]any{"ok": false, "reason": err.Error()}
		}
		var tasks []map[string]any
		if len(page.Tasks) == 0 || json.Unmarshal(page.Tasks, &tasks) != nil {
			return map[string]any{"ok": false, "reason": i18n.T("无法读取桌面任务")}
		}
		if tasks == nil {
			tasks = make([]map[string]any, 0)
		}
		return map[string]any{"ok": true, "tasks": tasks, "next": page.Next}
	case "download", "prompt":
		if msg.Queue != "" && msg.Queue != "main" && msg.Queue != "night" {
			return map[string]any{"ok": false, "reason": i18n.T("不支持的下载队列")}
		}
		var required []string
		if msg.Type == "prompt" {
			required = append(required, "download-prompt")
		}
		if msg.Queue == "night" {
			required = append(required, "scheduled-queue")
		}
		switch msg.MediaMode {
		case "hls-ts":
			required = append(required, "media-hls")
		case "dash-fmp4":
			required = append(required, "media-dash")
		case "":
		default:
			return map[string]any{"ok": false, "reason": i18n.T("不支持的媒体下载模式")}
		}
		if msg.MediaQuality != "" {
			required = append(required, "media-quality")
		}
		if msg.AudioLanguage != "" {
			required = append(required, "media-audio-language")
		}
		if msg.ClientRequestID != "" {
			required = append(required, "idempotent-submission")
		}
		if len(required) > 0 {
			if err := checkDesktopCapabilities(client, token, required); err != nil {
				var unsupported *unsupportedCapabilityError
				if errors.As(err, &unsupported) && unsupported.capability == "idempotent-submission" {
					return map[string]any{"ok": false, "unsupportedIdempotency": true, "reason": err.Error()}
				}
				if errors.As(err, &unsupported) {
					return map[string]any{"ok": false, "code": "desktop_unsupported", "reason": err.Error()}
				}
				return map[string]any{"ok": false, "reason": err.Error()}
			}
		}
		if len(msg.Cookie) > 8192 || strings.ContainsAny(msg.Cookie, "\r\n\x00") {
			return map[string]any{"ok": false, "reason": i18n.T("Cookie 格式无效")}
		}
		payload := map[string]any{
			"url":             msg.URL,
			"pageUrl":         msg.PageURL,
			"title":           msg.Title,
			"filename":        msg.Filename,
			"mediaMode":       msg.MediaMode,
			"mediaQuality":    msg.MediaQuality,
			"audioLanguage":   msg.AudioLanguage,
			"queue":           msg.Queue,
			"clientRequestId": msg.ClientRequestID,
		}
		if msg.Cookie != "" {
			payload["headers"] = map[string]string{"Cookie": msg.Cookie}
		}
		body, _ := json.Marshal(payload)
		endpoint := "/api/download"
		if msg.Type == "prompt" {
			endpoint = "/api/prompt"
		}
		req, err := http.NewRequest(http.MethodPost, apiBase+endpoint, bytes.NewReader(body))
		if err != nil {
			return map[string]any{"ok": false, "reason": err.Error()}
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-OpenDL-Source", "browser")
		if token != "" {
			req.Header.Set("X-OpenDL-Token", token)
		}
		resp, err := client.Do(req)
		if err != nil {
			unavailable := errors.Is(err, syscall.ECONNREFUSED)
			return map[string]any{"ok": false, "unavailable": unavailable, "deliveryUnknown": !unavailable, "reason": i18n.T("无法连接桌面端: ") + err.Error()}
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			reason := fmt.Sprintf(i18n.T("桌面端返回 HTTP %d"), resp.StatusCode)
			code := ""
			unknown := resp.StatusCode >= 500
			if resp.StatusCode == http.StatusUnauthorized {
				code = "desktop_unauthorized"
				reason = i18n.T("桌面端认证失败，请重新启动扩展和桌面应用")
			} else if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusConflict {
				if data, readErr := io.ReadAll(io.LimitReader(resp.Body, 512)); readErr == nil && strings.TrimSpace(string(data)) != "" {
					reason = strings.TrimSpace(string(data))
				}
			} else if resp.StatusCode == http.StatusServiceUnavailable {
				var rejection struct {
					Code     string `json:"code"`
					Accepted *bool  `json:"accepted"`
				}
				if json.NewDecoder(io.LimitReader(resp.Body, 1024)).Decode(&rejection) == nil && rejection.Accepted != nil && !*rejection.Accepted {
					switch rejection.Code {
					case "journal_write_failed":
						unknown = false
						code = rejection.Code
						reason = i18n.T("无法保存下载任务，请检查应用日志和存储空间")
					case "update_in_progress":
						unknown = false
						code = rejection.Code
						reason = i18n.T("应用正在准备更新，请稍后再操作")
					}
				}
			}
			return map[string]any{"ok": false, "deliveryUnknown": unknown, "reason": reason, "code": code}
		}
		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out == nil {
			return map[string]any{"ok": false, "deliveryUnknown": true, "reason": i18n.T("桌面端响应格式无效")}
		}
		if id, ok := out["id"].(string); !ok || id == "" {
			return map[string]any{"ok": false, "deliveryUnknown": true, "reason": i18n.T("桌面端未返回任务编号")}
		}
		out["ok"] = true
		return out
	default:
		return map[string]any{"ok": false, "reason": "unknown type " + msg.Type}
	}
}

type unsupportedCapabilityError struct{ capability string }

func (e *unsupportedCapabilityError) Error() string {
	return i18n.T("桌面端版本不支持该下载请求，请更新桌面应用")
}

// Check before posting so unsupported options cannot be silently ignored.
func checkDesktopCapabilities(client *http.Client, token string, required []string) error {
	_, err := desktopCapabilities(client, token, required)
	return err
}

type desktopInfo struct {
	Capabilities []string        `json:"capabilities"`
	Theme        json.RawMessage `json:"theme"`
}

func desktopCapabilities(client *http.Client, token string, required []string) (desktopInfo, error) {
	var info desktopInfo
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/api/ping", nil)
	if err != nil {
		return info, err
	}
	if token != "" {
		req.Header.Set("X-OpenDL-Token", token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return info, fmt.Errorf(i18n.T("无法确认桌面端下载能力：%w"), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return info, fmt.Errorf(i18n.T("无法确认桌面端下载能力（HTTP %d）"), resp.StatusCode)
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&info) != nil {
		return info, errors.New(i18n.T("无法读取桌面端下载能力"))
	}
	for _, capability := range required {
		found := false
		for _, available := range info.Capabilities {
			if available == capability {
				found = true
				break
			}
		}
		if !found {
			return info, &unsupportedCapabilityError{capability: capability}
		}
	}
	return info, nil
}

// readMessage reads one 4-byte little-endian length framed JSON message.
func readMessage(r io.Reader) (message, error) {
	var msg message
	var n uint32
	if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
		return msg, err
	}
	if n == 0 || n > 1<<20 {
		return msg, fmt.Errorf("bad message size %d", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return msg, err
	}
	return msg, json.Unmarshal(buf, &msg)
}

func writeMessage(w io.Writer, v any) error {
	buf, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, uint32(len(buf))); err != nil {
		return err
	}
	_, err = w.Write(buf)
	return err
}

// readToken loads the per-install token the desktop app wrote.
func readToken() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(dir, "opendl", "api-token"))
	if err != nil {
		return ""
	}
	return string(b)
}

// Read the desktop preference without changing the native messaging protocol.
func refreshLanguage() {
	dir, err := os.UserConfigDir()
	if err != nil {
		return
	}
	data, err := os.ReadFile(filepath.Join(dir, "opendl", "settings.json"))
	if err != nil {
		return
	}
	var preference struct {
		Language string `json:"language"`
	}
	if json.Unmarshal(data, &preference) == nil {
		i18n.Set(preference.Language)
	}
}
