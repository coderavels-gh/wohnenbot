// Package web contains a polite HTTP client and small HTML helpers.
package web

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const BrowserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36"

// Client throttles requests per host and retries transient failures.
type Client struct {
	http  *http.Client
	delay time.Duration

	mu   sync.Mutex
	last map[string]time.Time
}

func NewClient(delay time.Duration) *Client {
	return &Client{
		http:  &http.Client{Timeout: 40 * time.Second},
		delay: delay,
		last:  map[string]time.Time{},
	}
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
	next := c.last[host].Add(c.delay)
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

// Do sends a request (body may be nil) and returns the response body.
// Defaults to a browser User-Agent and German Accept-Language.
func (c *Client) Do(ctx context.Context, method, rawURL string, body []byte, headers map[string]string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(time.Duration(attempt*attempt) * 5 * time.Second):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if err := c.wait(ctx, u.Host); err != nil {
			return nil, err
		}
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, rawURL, rd)
		if err != nil {
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
			lastErr = err
			continue
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			lastErr = &HTTPError{resp.StatusCode, rawURL, string(data)}
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
