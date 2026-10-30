// Command opendl-cli submits download orders to the running OpenDL desktop
// app over its loopback API (the command-line counterpart of idman.exe).
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"openDL/api"
	"openDL/download"
)

const apiBase = "http://127.0.0.1:37913"

type headerFlags []string

func (h *headerFlags) String() string { return strings.Join(*h, ", ") }
func (h *headerFlags) Set(value string) error {
	*h = append(*h, value)
	return nil
}

func main() {
	name := flag.String("name", "", "target file name")
	directory := flag.String("dir", "", "save directory")
	queue := flag.String("queue", "", "queue id, night, or empty for main")
	page := flag.String("page", "", "source page URL (sets Referer)")
	syncMinutes := new(int) // Keep request construction compatible; periodic sync is no longer offered.
	mediaMode := flag.String("media", "", "hls-ts or dash-fmp4")
	mediaQuality := flag.String("quality", "", "height cap such as 720")
	audioLanguage := flag.String("audio", "", "audio track language")
	sha256 := flag.String("sha256", "", "expected SHA-256 of the published file")
	clientID := flag.String("client-id", "", "idempotent submission id")
	batch := flag.String("batch", "", "submit every line of a file (or - for stdin) as a URL")
	var headers headerFlags
	flag.Var(&headers, "header", "extra request header as Name: value")
	flag.Parse()
	args := flag.Args()
	command := "add"
	if len(args) > 0 {
		command = args[0]
		args = args[1:]
	}
	switch command {
	case "ping":
		if err := ping(); err != nil {
			fail(err)
		}
		fmt.Println("ok")
	case "tasks":
		reply, err := callJSON(http.MethodGet, "/api/tasks", nil)
		if err != nil {
			fail(err)
		}
		encoded, _ := json.MarshalIndent(reply, "", "  ")
		fmt.Println(string(encoded))
	case "cancel":
		if len(args) != 1 {
			fail(fmt.Errorf("cancel 需要一个任务编号"))
		}
		body, _ := json.Marshal(map[string]string{"id": strings.TrimSpace(args[0])})
		if _, err := call(http.MethodPost, "/api/cancel", body); err != nil {
			fail(err)
		}
		fmt.Println("ok")
	case "resume", "pause":
		if len(args) != 1 {
			fail(fmt.Errorf("%s 需要一个任务编号", command))
		}
		body, _ := json.Marshal(map[string]string{"id": strings.TrimSpace(args[0])})
		if _, err := call(http.MethodPost, "/api/"+command, body); err != nil {
			fail(err)
		}
		fmt.Println("ok")
	case "finish-live":
		if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
			fail(fmt.Errorf("finish-live 需要一个任务编号"))
		}
		body, _ := json.Marshal(map[string]string{"id": strings.TrimSpace(args[0])})
		reply, err := call(http.MethodPost, "/api/finish-live", body)
		if err != nil {
			fail(err)
		}
		if reply["ok"] != true {
			fail(fmt.Errorf("任务未处于可结束的直播录制状态"))
		}
		fmt.Println("ok")
	case "start-all", "pause-all":
		reply, err := call(http.MethodPost, "/api/"+command, nil)
		if err != nil {
			fail(err)
		}
		encoded, _ := json.Marshal(reply)
		fmt.Println(string(encoded))
	case "add":
		if *batch != "" {
			if len(args) != 0 {
				fail(fmt.Errorf("--batch 不接受命令行地址"))
			}
			if err := submitBatch(*batch, func() (download.Request, error) {
				return buildRequest("", name, directory, queue, page, syncMinutes, mediaMode, mediaQuality, audioLanguage, sha256, clientID, headers)
			}); err != nil {
				fail(err)
			}
			return
		}
		if len(args) != 1 {
			fail(fmt.Errorf("add 需要一个下载地址"))
		}
		req, err := buildRequest(strings.TrimSpace(args[0]), name, directory, queue, page, syncMinutes, mediaMode, mediaQuality, audioLanguage, sha256, clientID, headers)
		if err != nil {
			fail(err)
		}
		if err := submit(req); err != nil {
			fail(err)
		}
	default:
		fail(fmt.Errorf("未知命令: %s", command))
	}
}

// buildRequest 组装一次提交；请求头形如 名称: 值，来源页自动补 Referer。
func buildRequest(address string, name, directory, queue, page *string, syncMinutes *int, mediaMode, mediaQuality, audioLanguage, sha256, clientID *string, headers headerFlags) (download.Request, error) {
	req := download.Request{
		URL:             strings.TrimSpace(address),
		Name:            strings.TrimSpace(*name),
		Directory:       strings.TrimSpace(*directory),
		Queue:           strings.TrimSpace(*queue),
		PageURL:         strings.TrimSpace(*page),
		SyncMinutes:     *syncMinutes,
		MediaMode:       strings.TrimSpace(*mediaMode),
		MediaQuality:    strings.TrimSpace(*mediaQuality),
		AudioLanguage:   strings.TrimSpace(*audioLanguage),
		SHA256:          strings.TrimSpace(*sha256),
		ClientRequestID: strings.TrimSpace(*clientID),
		Headers:         map[string]string{},
	}
	for _, entry := range headers {
		key, value, ok := strings.Cut(entry, ":")
		if !ok {
			return req, fmt.Errorf("请求头形如 名称: 值: %q", entry)
		}
		req.Headers[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	if req.PageURL != "" && req.Headers["Referer"] == "" {
		req.Headers["Referer"] = req.PageURL
	}
	return req, nil
}

// submitBatch 逐行提交文件（或 - 表示标准输入）中的地址；空行与井号注释跳过。
func submitBatch(path string, build func() (download.Request, error)) error {
	var input io.Reader
	if path == "-" {
		input = os.Stdin
	} else {
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		input = file
	}
	submitted, failed := 0, 0
	scanner := bufio.NewScanner(input)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		req, err := build()
		if err != nil {
			fmt.Fprintln(os.Stderr, line+":", err)
			failed++
			continue
		}
		req.URL = line
		if err := submit(req); err != nil {
			fmt.Fprintln(os.Stderr, line+":", err)
			failed++
			continue
		}
		submitted++
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	fmt.Printf("已提交 %d 个，失败 %d 个\n", submitted, failed)
	if failed > 0 {
		return fmt.Errorf("部分地址提交失败")
	}
	return nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func token() (string, error) {
	path, err := api.TokenPath()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("无法读取接口令牌（应用未运行？）: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

func call(method, path string, body []byte) (map[string]any, error) {
	value, err := callJSON(method, path, body)
	if err != nil {
		return nil, err
	}
	reply, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("接口返回的 JSON 对象无效")
	}
	return reply, nil
}

func callJSON(method, path string, body []byte) (any, error) {
	secret, err := token()
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(method, apiBase+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-OpenDL-Token", secret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("无法连接 OpenDL（应用未运行？）: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var reply any
	if resp.StatusCode != http.StatusOK {
		if json.Unmarshal(data, &reply) == nil {
			return nil, fmt.Errorf("HTTP %d: %v", resp.StatusCode, reply)
		}
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if err := json.Unmarshal(data, &reply); err != nil {
		return nil, err
	}
	return reply, nil
}

func ping() error {
	_, err := call(http.MethodGet, "/api/ping", nil)
	return err
}

func submit(req download.Request) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	reply, err := call(http.MethodPost, "/api/download", body)
	if err != nil {
		return err
	}
	encoded, _ := json.Marshal(reply)
	fmt.Println(string(encoded))
	return nil
}
