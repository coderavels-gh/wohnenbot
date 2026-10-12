// Package notify sends alerts: WhatsApp via CallMeBot (preferred), push via
// ntfy (iPhone + Mac apps), and email as the last fallback.
package notify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/smtp"
	"net/url"
	"strings"
	"time"

	"aptbot/internal/config"
)

type Message struct {
	Title string // short headline
	Body  string // multi-line plain text
	URL   string
}

type Dispatcher struct {
	Cfg  config.NotifyConfig
	HTTP *http.Client
}

func NewDispatcher(cfg config.NotifyConfig) *Dispatcher {
	return &Dispatcher{Cfg: cfg, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// Send delivers messages with the fallback chain WhatsApp → ntfy → email.
// Channels marked "always" receive messages regardless.
func (d *Dispatcher) Send(ctx context.Context, msgs []Message) error {
	if len(msgs) == 0 {
		return nil
	}
	delivered := false
	var errs []error
	if len(d.Cfg.WhatsApp) > 0 {
		if err := d.whatsapp(ctx, msgs); err != nil {
			errs = append(errs, err)
		} else {
			delivered = true
		}
	}
	if n := d.Cfg.Ntfy; n != nil && n.Topic != "" && (!delivered || n.Always) {
		if err := d.ntfy(ctx, msgs); err != nil {
			errs = append(errs, err)
		} else {
			delivered = true
		}
	}
	if e := d.Cfg.Email; e != nil && e.SMTPHost != "" && (!delivered || e.Always) {
		if err := d.email(msgs); err != nil {
			errs = append(errs, err)
		} else {
			delivered = true
		}
	}
	if !delivered {
		if len(errs) == 0 {
			return errors.New("no notification channel configured")
		}
		return errors.Join(errs...)
	}
	for _, e := range errs {
		log.Printf("notify: (fallback used) %v", e)
	}
	return nil
}

func (d *Dispatcher) get(ctx context.Context, u string) (string, int, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	resp, err := d.HTTP.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return string(b), resp.StatusCode, nil
}

// whatsapp: one CallMeBot request per message per recipient. Succeeds if at
// least one recipient received everything.
func (d *Dispatcher) whatsapp(ctx context.Context, msgs []Message) error {
	okRecipients := 0
	var errs []error
	for _, r := range d.Cfg.WhatsApp {
		failed := false
		for i, m := range msgs {
			if i > 0 {
				time.Sleep(4 * time.Second) // CallMeBot rate limit
			}
			text := m.Body
			if m.URL != "" {
				text += "\n" + m.URL
			}
			u := "https://api.callmebot.com/whatsapp.php?" + url.Values{
				"phone": {r.Phone}, "text": {text}, "apikey": {r.APIKey},
			}.Encode()
			body, status, err := d.get(ctx, u)
			lb := strings.ToLower(body)
			if err == nil && (status != 200 || strings.Contains(lb, "apikey is invalid") || strings.Contains(lb, "error")) {
				err = fmt.Errorf("HTTP %d: %.150s", status, strings.TrimSpace(stripTags(body)))
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("whatsapp %s: %w", r.Name, err))
				failed = true
				break
			}
		}
		if !failed {
			okRecipients++
		}
	}
	if okRecipients == 0 {
		return errors.Join(errs...)
	}
	for _, e := range errs {
		log.Printf("notify: %v", e)
	}
	return nil
}

func stripTags(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func (d *Dispatcher) ntfy(ctx context.Context, msgs []Message) error {
	n := d.Cfg.Ntfy
	for _, m := range msgs {
		req, _ := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(n.Server, "/")+"/"+n.Topic, strings.NewReader(m.Body))
		req.Header.Set("Title", asciiHeader(m.Title))
		req.Header.Set("Tags", "house")
		req.Header.Set("Priority", "high")
		if m.URL != "" {
			req.Header.Set("Click", m.URL)
		}
		resp, err := d.HTTP.Do(req)
		if err != nil {
			return fmt.Errorf("ntfy: %w", err)
		}
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("ntfy: HTTP %d", resp.StatusCode)
		}
	}
	return nil
}

// HTTP header values must be ASCII; ntfy also accepts RFC 2047 but
// transliterating German umlauts is simpler and readable.
func asciiHeader(s string) string {
	r := strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "Ä", "Ae", "Ö", "Oe", "Ü", "Ue", "ß", "ss", "€", "EUR", "²", "2", "·", "-")
	s = r.Replace(s)
	var b strings.Builder
	for _, c := range s {
		if c >= 32 && c < 127 {
			b.WriteRune(c)
		}
	}
	return b.String()
}

func (d *Dispatcher) email(msgs []Message) error {
	e := d.Cfg.Email
	var body strings.Builder
	for _, m := range msgs {
		body.WriteString(m.Body + "\n" + m.URL + "\n\n")
	}
	subject := msgs[0].Title
	if len(msgs) > 1 {
		subject = fmt.Sprintf("%d new flats", len(msgs))
	}
	msg := "From: " + e.From + "\r\nTo: " + strings.Join(e.To, ", ") + "\r\nSubject: " + asciiHeader(subject) +
		"\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" + body.String()
	port := e.SMTPPort
	if port == 0 {
		port = 587
	}
	auth := smtp.PlainAuth("", e.Username, e.Password, e.SMTPHost)
	if err := smtp.SendMail(fmt.Sprintf("%s:%d", e.SMTPHost, port), auth, e.From, e.To, []byte(msg)); err != nil {
		return fmt.Errorf("email: %w", err)
	}
	return nil
}
