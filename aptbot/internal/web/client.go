// Package web contains a polite HTTP client and small HTML helpers.
package web

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const BrowserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36"

// Client is deliberately polite: one request at a time per host with a
// minimum gap, retries only for transient errors, Retry-After honoured, and a
// host that answers 403/429 is left alone for the rest of the run.
type Client struct {
	http  *http.Client
	delay time.Duration
	// HostDelay overrides the gap for specific hosts.
	HostDelay map[string]time.Duration
	// HostTimeout and HostAttempts override the per-request timeout (40 s)
	// and the number of tries (3) for specific hosts.
	HostTimeout  map[string]time.Duration
	HostAttempts map[string]int
	// HostCurl: hosts fetched through the system curl (see curlTransport).
	HostCurl map[string]bool

	mu      sync.Mutex
	last    map[string]time.Time
	blocked map[string]error
	paused  map[string]time.Time // host → until (refused us in an earlier run)
}

func NewClient(delay time.Duration) *Client {
	curl := map[string]bool{}
	return &Client{
		// Timeouts are per request (defaultTimeout, HostTimeout).
		http:         &http.Client{Transport: hostTransport{curl: curl, base: http.DefaultTransport}},
		HostCurl:     curl,
		delay:        delay,
		HostDelay:    map[string]time.Duration{},
		HostTimeout:  map[string]time.Duration{},
		HostAttempts: map[string]int{},
		last:         map[string]time.Time{},
		blocked:      map[string]error{},
		paused:       map[string]time.Time{},
	}
}

// Pause keeps the client from contacting host until the given time (a site
// that refused us in an earlier run). Unlike a block in this run, it isn't
// reported by BlockedHosts, so the pause isn't extended.
func (c *Client) Pause(host string, until time.Time) {
	c.mu.Lock()
	c.paused[host] = until
	c.mu.Unlock()
}

// inflate decodes a "deflate" body, which servers send either zlib-wrapped or
// raw (City-Wohnen sends raw deflate whatever the request asks for). Go only
// decodes gzip by itself.
func inflate(data []byte) ([]byte, error) {
	var r io.ReadCloser
	if len(data) > 1 && data[0] == 0x78 {
		zr, err := zlib.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		r = zr
	} else {
		r = flate.NewReader(bytes.NewReader(data))
	}
	defer r.Close()
	return io.ReadAll(io.LimitReader(r, 50<<20))
}

// defaultTimeout bounds one request unless HostTimeout says otherwise.
const defaultTimeout = 40 * time.Second

// maxRetryAfter caps how long one request waits on a Retry-After header.
const maxRetryAfter = 2 * time.Minute

func retryAfter(h http.Header) time.Duration {
	v := h.Get("Retry-After")
	if v == "" {
		return 0
	}
	if s, err := strconv.Atoi(v); err == nil {
		return time.Duration(s) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return time.Until(t)
	}
	return 0
}

type HTTPError struct {
	Status int
	URL    string
	Body   string
}

func (e *HTTPError) Error() string {
	b := e.Body
	if len(b) > 200 {
		b = b[:200]
	}
	return fmt.Sprintf("GET/POST %s: HTTP %d: %s", e.URL, e.Status, b)
}

func (c *Client) wait(ctx context.Context, host string) error {
	c.mu.Lock()
	gap := c.delay
	if d, ok := c.HostDelay[host]; ok {
		gap = d
	}
	next := c.last[host].Add(gap)
	now := time.Now()
	if next.Before(now) {
		next = now
	}
	c.last[host] = next
	c.mu.Unlock()
	select {
	case <-time.After(time.Until(next)):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// BlockedHosts lists the hosts that refused requests (403/429) this run.
func (c *Client) BlockedHosts() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for h := range c.blocked {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// Do sends a request (body may be nil) and returns the response body.
// Defaults to a browser User-Agent and German Accept-Language.
func (c *Client) Do(ctx context.Context, method, rawURL string, body []byte, headers map[string]string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	berr := c.blocked[u.Host]
	until := c.paused[u.Host]
	c.mu.Unlock()
	if time.Now().Before(until) {
		return nil, fmt.Errorf("skipped, %s refused an earlier run: paused until %s", u.Host, until.Local().Format("15:04"))
	}
	if berr != nil {
		return nil, fmt.Errorf("skipped, %s refused an earlier request this run: %w", u.Host, berr)
	}
	attempts := 3
	if n, ok := c.HostAttempts[u.Host]; ok && n > 0 {
		attempts = n
	}
	timeout := defaultTimeout
	if d, ok := c.HostTimeout[u.Host]; ok && d > 0 {
		timeout = d
	}
	var lastErr error
	var wait time.Duration
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			if wait == 0 {
				wait = time.Duration(attempt*attempt) * 5 * time.Second
			}
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			wait = 0
		}
		if err := c.wait(ctx, u.Host); err != nil {
			return nil, err
		}
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		rctx, cancel := context.WithTimeout(ctx, timeout)
		req, err := http.NewRequestWithContext(rctx, method, rawURL, rd)
		if err != nil {
			cancel()
			return nil, err
		}
		req.Header.Set("User-Agent", BrowserUA)
		req.Header.Set("Accept-Language", "de-DE,de;q=0.9,en;q=0.8")
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/json;q=0.9,*/*;q=0.8")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
		resp.Body.Close()
		cancel()
		if err == nil && strings.EqualFold(resp.Header.Get("Content-Encoding"), "deflate") {
			data, err = inflate(data)
		}
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == 403 || resp.StatusCode == 429 {
			// Rate-limited or blocked: wait as told once, then leave the host alone.
			herr := &HTTPError{resp.StatusCode, rawURL, string(data)}
			if ra := retryAfter(resp.Header); resp.StatusCode == 429 && ra > 0 && ra <= maxRetryAfter && attempt == 0 {
				wait, lastErr = ra, herr
				continue
			}
			c.mu.Lock()
			c.blocked[u.Host] = herr
			c.mu.Unlock()
			return data, herr
		}
		if resp.StatusCode >= 500 {
			lastErr = &HTTPError{resp.StatusCode, rawURL, string(data)}
			if ra := retryAfter(resp.Header); ra > 0 && ra <= maxRetryAfter {
				wait = ra
			}
			continue
		}
		if resp.StatusCode >= 300 {
			return data, &HTTPError{resp.StatusCode, rawURL, string(data)}
		}
		return data, nil
	}
	return nil, lastErr
}

func (c *Client) Get(ctx context.Context, rawURL string, headers map[string]string) ([]byte, error) {
	return c.Do(ctx, http.MethodGet, rawURL, nil, headers)
}
