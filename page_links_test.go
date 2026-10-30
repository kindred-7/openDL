package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestPageLinksUsesBaseAndDownloadAttribute(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<a href="before.pdf">before</a><base href="/files/"><a download href="get?id=1">download</a><audio src="track.m4a"></audio><object data="movie"></object><a href="before.pdf#again">duplicate</a><a download href="">empty</a>`)
	}))
	defer srv.Close()
	links, err := pageLinks(srv.URL + "/page")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{srv.URL + "/files/before.pdf", srv.URL + "/files/get?id=1", srv.URL + "/files/track.m4a", srv.URL + "/files/movie"}
	if !reflect.DeepEqual(links, want) {
		t.Fatalf("links=%v, want %v", links, want)
	}
}

func TestPageLinksIncludesLazyLoadedResources(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<a download data-href="/get?id=1">download</a><video data-src="/video"></video><audio data-original="/audio.mp3"></audio><div data-href="/ignore.zip"></div>`)
	}))
	defer srv.Close()
	got, err := pageLinks(srv.URL)
	want := []string{srv.URL + "/get?id=1", srv.URL + "/video", srv.URL + "/audio.mp3"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("links=%v, want %v, err=%v", got, want, err)
	}
}

func TestPageLinksDecodesDeclaredGBK(t *testing.T) {
	body, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(`<meta charset="gbk"><a href="文件.zip">文件</a>`))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	got, err := pageLinks(srv.URL)
	want := []string{srv.URL + "/%E6%96%87%E4%BB%B6.zip"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("links=%v, want %v, err=%v", got, want, err)
	}
}

func TestPageLinksKeepsUndeclaredUTF8AfterASCIIHeader(t *testing.T) {
	body := strings.Repeat(" ", 1024) + `<a href="文件.zip">文件</a>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	got, err := pageLinks(srv.URL)
	want := []string{srv.URL + "/%E6%96%87%E4%BB%B6.zip"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("links=%v, want %v, err=%v", got, want, err)
	}
}
