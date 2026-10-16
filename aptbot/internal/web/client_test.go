package web

import (
	"compress/flate"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBlockedHostIsLeftAlone(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	c := NewClient(time.Millisecond)
	ctx := context.Background()
	if _, err := c.Get(ctx, srv.URL+"/a", nil); err == nil {
		t.Fatal("expected 403 error")
	}
	if _, err := c.Get(ctx, srv.URL+"/b", nil); err == nil {
		t.Fatal("expected skip error")
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("server hit %d times, want 1 (no retry, no second request)", n)
	}
}

func TestRetryAfterHonoured(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	c := NewClient(time.Millisecond)
	start := time.Now()
	body, err := c.Get(context.Background(), srv.URL, nil)
	if err != nil || string(body) != "ok" {
		t.Fatalf("body=%q err=%v", body, err)
	}
	if time.Since(start) < time.Second {
		t.Fatal("did not wait for Retry-After")
	}
}

func TestRawDeflateBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "deflate")
		fw, _ := flate.NewWriter(w, flate.DefaultCompression)
		fw.Write([]byte(`{"total":102}`))
		fw.Close()
	}))
	defer srv.Close()
	body, err := NewClient(time.Millisecond).Get(context.Background(), srv.URL, nil)
	if err != nil || string(body) != `{"total":102}` {
		t.Fatalf("body=%q err=%v", body, err)
	}
}

func TestHostTimeoutAndAttempts(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()
	c := NewClient(time.Millisecond)
	host := strings.TrimPrefix(srv.URL, "http://")
	c.HostTimeout[host] = 200 * time.Millisecond
	c.HostAttempts[host] = 1
	start := time.Now()
	if _, err := c.Get(context.Background(), srv.URL, nil); err == nil {
		t.Fatal("expected a timeout")
	}
	if d := time.Since(start); d > time.Second || hits.Load() != 1 {
		t.Fatalf("took %s with %d tries; want one try cut at 200ms", d, hits.Load())
	}
}

func TestHostTimeoutCanBeLonger(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	c := NewClient(time.Millisecond)
	host := strings.TrimPrefix(srv.URL, "http://")
	c.HostTimeout[host] = 2 * time.Second
	if _, err := c.Get(context.Background(), srv.URL, nil); err != nil {
		t.Fatal(err)
	}
}
