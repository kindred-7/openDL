package nativehost

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"syscall"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRepliesKeepRequestIdentity(t *testing.T) {
	for _, id := range []string{"ping-1", "download-2"} {
		reply := handleMessage(http.DefaultClient, "", message{Type: "ping", RequestID: id})
		if reply["requestId"] != id || reply["ok"] != true {
			t.Fatalf("unexpected reply for %s: %v", id, reply)
		}
	}
}

func TestDownloadForwardsCookieAsRequestHeader(t *testing.T) {
	var received string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var payload struct {
			Headers map[string]string `json:"headers"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			return nil, err
		}
		received = payload.Headers["Cookie"]
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"id":"task"}`)), Header: make(http.Header)}, nil
	})}
	reply := handle(client, "token", message{Type: "download", URL: "https://example.test/file", Cookie: "session=value"})
	if reply["ok"] != true || received != "session=value" {
		t.Fatalf("cookie forwarding failed: reply=%v", reply)
	}
	if reply := handle(client, "token", message{Type: "download", Cookie: "bad\r\nheader"}); reply["ok"] != false {
		t.Fatal("invalid cookie was accepted")
	}
}

func TestScheduledQueueRequiresDesktopCapability(t *testing.T) {
	for _, supported := range []bool{false, true} {
		t.Run(map[bool]string{false: "old-desktop", true: "supported"}[supported], func(t *testing.T) {
			var posts int
			var queue string
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/api/ping" {
					body := `{"ok":true}`
					if supported {
						body = `{"ok":true,"capabilities":["scheduled-queue"]}`
					}
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
				}
				posts++
				var payload struct {
					Queue string `json:"queue"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					return nil, err
				}
				queue = payload.Queue
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"id":"task"}`)), Header: make(http.Header)}, nil
			})}
			reply := handle(client, "token", message{Type: "download", URL: "https://example.test/file", Queue: "night"})
			if supported {
				if reply["ok"] != true || posts != 1 || queue != "night" {
					t.Fatalf("queue not forwarded: %v posts=%d queue=%q", reply, posts, queue)
				}
			} else if reply["ok"] != false || posts != 0 {
				t.Fatalf("old desktop received scheduled task: %v posts=%d", reply, posts)
			}
		})
	}
}

func TestLogicalSubmissionIDRequiresCapabilityAndIsForwarded(t *testing.T) {
	for _, supported := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "supported"}[supported], func(t *testing.T) {
			var postedID string
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				body := `{"ok":true}`
				if r.URL.Path == "/api/ping" {
					if supported {
						body = `{"ok":true,"capabilities":["idempotent-submission"]}`
					}
				} else {
					var payload struct {
						ID string `json:"clientRequestId"`
					}
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						return nil, err
					}
					postedID = payload.ID
					body = `{"id":"task"}`
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})}
			reply := handle(client, "token", message{Type: "download", URL: "https://example.test/file", ClientRequestID: "logical-1"})
			if supported {
				if reply["ok"] != true || postedID != "logical-1" {
					t.Fatalf("missing request identity: %v id=%q", reply, postedID)
				}
			} else if reply["unsupportedIdempotency"] != true || postedID != "" {
				t.Fatalf("legacy desktop received the request: %v id=%q", reply, postedID)
			}
		})
	}
}

func TestDownloadDeliveryClassification(t *testing.T) {
	cases := []struct {
		name            string
		status          int
		body            string
		err             error
		unavailable     bool
		deliveryUnknown bool
	}{
		{name: "connection refused", err: syscall.ECONNREFUSED, unavailable: true},
		{name: "connection lost", err: errors.New("connection lost"), deliveryUnknown: true},
		{name: "unauthorized", status: http.StatusUnauthorized, body: "bad token"},
		{name: "server error", status: http.StatusInternalServerError, body: "error", deliveryUnknown: true},
		{name: "recording rejected", status: http.StatusServiceUnavailable, body: `{"code":"journal_write_failed","accepted":false}`},
		{name: "ambiguous recording error", status: http.StatusServiceUnavailable, body: `{"code":"journal_write_failed"}`, deliveryUnknown: true},
		{name: "invalid success response", status: http.StatusOK, body: "invalid", deliveryUnknown: true},
		{name: "missing task id", status: http.StatusOK, body: `{}`, deliveryUnknown: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				if tc.err != nil {
					return nil, tc.err
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
			})}
			reply := handle(client, "token", message{Type: "download", URL: "https://example.com/file"})
			unavailable, _ := reply["unavailable"].(bool)
			unknown, _ := reply["deliveryUnknown"].(bool)
			if reply["ok"] != false || unavailable != tc.unavailable || unknown != tc.deliveryUnknown {
				t.Fatalf("unexpected delivery classification: %v", reply)
			}
		})
	}
}

func TestSlowDownloadDoesNotBlockHandshake(t *testing.T) {
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	defer inputReader.Close()
	defer inputWriter.Close()
	defer outputReader.Close()
	defer outputWriter.Close()
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		close(started)
		<-release
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"id":"task"}`)), Header: make(http.Header)}, nil
	})}
	go func() { _ = serveNative(inputReader, outputWriter, client, func() string { return "token" }) }()
	if err := writeMessage(inputWriter, message{Type: "download", RequestID: "slow", URL: "https://example.com/file"}); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := writeMessage(inputWriter, message{Type: "ping", RequestID: "ping"}); err != nil {
		t.Fatal(err)
	}
	result := make(chan message, 1)
	go func() { msg, _ := readMessage(outputReader); result <- msg }()
	select {
	case msg := <-result:
		if msg.RequestID != "ping" {
			t.Fatalf("unexpected reply: %+v", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handshake blocked by slow request")
	}
}
