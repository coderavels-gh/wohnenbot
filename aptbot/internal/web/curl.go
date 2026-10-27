package web

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/textproto"
	"os/exec"
	"strconv"
	"strings"
)

// curlTransport sends requests through the system curl. Kleinanzeigen refuses
// Go's own HTTP client by its connection fingerprint, whatever the headers,
// but answers curl. Redirects are left to http.Client, as usual.
type curlTransport struct{}

func (curlTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	args := []string{"-sS", "--compressed", "--dump-header", "-", "-X", req.Method}
	for k, vs := range req.Header {
		for _, v := range vs {
			args = append(args, "-H", k+": "+v)
		}
	}
	var stdin io.Reader
	if req.Body != nil {
		args = append(args, "--data-binary", "@-")
		stdin = req.Body
	}
	args = append(args, "--", req.URL.String())
	cmd := exec.CommandContext(req.Context(), "curl", args...)
	cmd.Stdin = stdin
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		if req.Context().Err() != nil {
			return nil, req.Context().Err()
		}
		return nil, fmt.Errorf("curl: %v: %s", err, strings.TrimSpace(errb.String()))
	}
	return parseCurlOutput(out.Bytes(), req)
}

// parseCurlOutput reads "HTTP/2 200\r\nheaders…\r\n\r\nbody". The body is
// already decompressed (--compressed), so Content-Encoding is dropped.
func parseCurlOutput(out []byte, req *http.Request) (*http.Response, error) {
	head, body, ok := bytes.Cut(out, []byte("\r\n\r\n"))
	if !ok {
		return nil, fmt.Errorf("curl: no response headers")
	}
	tp := textproto.NewReader(bufio.NewReader(bytes.NewReader(append(head, "\r\n\r\n"...))))
	status, err := tp.ReadLine()
	if err != nil {
		return nil, fmt.Errorf("curl: %w", err)
	}
	proto, rest, _ := strings.Cut(status, " ")
	code, _, _ := strings.Cut(rest, " ")
	n, err := strconv.Atoi(code)
	if !strings.HasPrefix(proto, "HTTP/") || err != nil {
		return nil, fmt.Errorf("curl: bad status line %q", status)
	}
	hdr, err := tp.ReadMIMEHeader()
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("curl: headers: %w", err)
	}
	h := http.Header(hdr)
	h.Del("Content-Encoding")
	h.Del("Content-Length")
	return &http.Response{
		Status: status, StatusCode: n, Proto: proto, Header: h,
		Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)), Request: req,
	}, nil
}

// hostTransport sends requests for the hosts in curl through curlTransport.
type hostTransport struct {
	curl map[string]bool
	base http.RoundTripper
}

func (t hostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.curl[req.URL.Host] {
		return curlTransport{}.RoundTrip(req)
	}
	return t.base.RoundTrip(req)
}
