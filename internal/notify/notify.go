// Package notify sends short messages about things a person would want
// to know about without opening the app: a recording that failed, a
// conflict that means something will be missed, a recording that is
// ready. Two transports: a webhook (POST JSON, which Discord, Slack, ntfy,
// Gotify and Home Assistant all accept in some form) and email over SMTP.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/smtp"
	"strings"
	"sync"
	"time"
)

// Config selects the transports; empty values disable them.
type Config struct {
	WebhookURL string
	SMTPHost   string
	SMTPPort   int
	SMTPUser   string
	SMTPPass   string
	From       string
	To         []string
	// OnReady also announces finished recordings, not only problems.
	OnReady bool
}

// Notifier fans a message out to every configured transport.
type Notifier struct {
	cfg  Config
	log  *slog.Logger
	http *http.Client

	mu   sync.Mutex
	last map[string]time.Time
}

// New builds a notifier; it is safe to use with nothing configured.
func New(cfg Config, log *slog.Logger) *Notifier {
	if cfg.SMTPPort == 0 {
		cfg.SMTPPort = 587
	}
	return &Notifier{cfg: cfg, log: log, http: &http.Client{Timeout: 15 * time.Second}, last: map[string]time.Time{}}
}

// Enabled reports whether any transport is configured.
func (n *Notifier) Enabled() bool {
	return n != nil && (n.cfg.WebhookURL != "" || (n.cfg.SMTPHost != "" && len(n.cfg.To) > 0))
}

// Transports names what is configured, for the Status page.
func (n *Notifier) Transports() []string {
	var out []string
	if n == nil {
		return out
	}
	if n.cfg.WebhookURL != "" {
		out = append(out, "webhook")
	}
	if n.cfg.SMTPHost != "" && len(n.cfg.To) > 0 {
		out = append(out, "email")
	}
	return out
}

// OnReady reports whether finished recordings are announced.
func (n *Notifier) OnReady() bool { return n != nil && n.cfg.OnReady }

// Send delivers a message. key suppresses repeats of the same thing
// within an hour (a conflict re-detected every minute is one message).
func (n *Notifier) Send(key, title, body string) {
	if !n.Enabled() {
		return
	}
	if key != "" {
		n.mu.Lock()
		if t, ok := n.last[key]; ok && time.Since(t) < time.Hour {
			n.mu.Unlock()
			return
		}
		n.last[key] = time.Now()
		for k, t := range n.last {
			if time.Since(t) > 2*time.Hour {
				delete(n.last, k)
			}
		}
		n.mu.Unlock()
	}
	go func() {
		if n.cfg.WebhookURL != "" {
			if err := n.webhook(title, body); err != nil {
				n.log.Warn("notification webhook failed", "err", err)
			}
		}
		if n.cfg.SMTPHost != "" && len(n.cfg.To) > 0 {
			if err := n.email(title, body); err != nil {
				n.log.Warn("notification email failed", "err", err)
			}
		}
	}()
}

// webhook posts a JSON body with the fields most receivers understand:
// "title" and "message" (ntfy, Gotify), "content" (Discord), "text" (Slack).
func (n *Notifier) webhook(title, body string) error {
	payload, _ := json.Marshal(map[string]string{
		"title": title, "message": body, "content": title + "\n" + body, "text": title + "\n" + body,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.cfg.WebhookURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Title", title) // ntfy reads the title from a header too
	res, err := n.http.Do(req)
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("webhook answered %s", res.Status)
	}
	return nil
}

func (n *Notifier) email(title, body string) error {
	from := n.cfg.From
	if from == "" {
		from = n.cfg.SMTPUser
	}
	msg := strings.Join([]string{
		"From: " + from,
		"To: " + strings.Join(n.cfg.To, ", "),
		"Subject: [OpenDVR] " + title,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"",
		body,
		"",
	}, "\r\n")
	addr := fmt.Sprintf("%s:%d", n.cfg.SMTPHost, n.cfg.SMTPPort)
	var auth smtp.Auth
	if n.cfg.SMTPUser != "" {
		auth = smtp.PlainAuth("", n.cfg.SMTPUser, n.cfg.SMTPPass, n.cfg.SMTPHost)
	}
	return smtp.SendMail(addr, auth, from, n.cfg.To, []byte(msg))
}
