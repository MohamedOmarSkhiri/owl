package main

// Notifications: the relay sends a message when a problem opens, resolves or comes back,
// and when a cluster goes down or comes back. Channels: Telegram, Slack, Discord and a
// generic webhook, all plain HTTPS calls. Everything is configured with RELAY_NOTIFY_*
// environment variables (the Helm chart sets them from notifications.* values).
//
// Sending never blocks the agent: alerts go into a queue and one goroutine delivers them,
// with a few retries. A cooldown per problem keeps a flapping pod from flooding a phone.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Alert is one thing worth telling someone about.
type Alert struct {
	Kind      string    `json:"kind"`  // problem | cluster | test
	State     string    `json:"state"` // open | resolved | down | up | test
	Cluster   string    `json:"cluster"`
	Severity  string    `json:"severity,omitempty"` // critical | warning (problems)
	Namespace string    `json:"namespace,omitempty"`
	Object    string    `json:"object,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	Message   string    `json:"message,omitempty"`
	Time      time.Time `json:"time"`
	URL       string    `json:"url,omitempty"` // link into Owl, when RELAY_PUBLIC_URL is set
}

// channel is one place messages go to.
type channel interface {
	Name() string   // telegram, slack, discord, webhook
	Target() string // where, with secrets hidden, for the app's Notifications panel
	Send(ctx context.Context, c *http.Client, a Alert, text string) error
}

// telegramAPI is a variable so tests can point it at a fake server.
var telegramAPI = "https://api.telegram.org"

type telegram struct{ token, chatID string }

func (t telegram) Name() string   { return "telegram" }
func (t telegram) Target() string { return "chat " + t.chatID }
func (t telegram) Send(ctx context.Context, c *http.Client, _ Alert, text string) error {
	return postJSON(ctx, c, telegramAPI+"/bot"+t.token+"/sendMessage",
		map[string]any{"chat_id": t.chatID, "text": text, "disable_web_page_preview": true})
}

type slack struct{ url string }

func (s slack) Name() string   { return "slack" }
func (s slack) Target() string { return hostOnly(s.url) }
func (s slack) Send(ctx context.Context, c *http.Client, _ Alert, text string) error {
	return postJSON(ctx, c, s.url, map[string]any{"text": text})
}

type discord struct{ url string }

func (d discord) Name() string   { return "discord" }
func (d discord) Target() string { return hostOnly(d.url) }
func (d discord) Send(ctx context.Context, c *http.Client, _ Alert, text string) error {
	return postJSON(ctx, c, d.url, map[string]any{"content": text})
}

// webhook posts the whole alert as JSON, plus the ready-made text, for your own tools.
type webhook struct{ url string }

func (w webhook) Name() string   { return "webhook" }
func (w webhook) Target() string { return hostOnly(w.url) }
func (w webhook) Send(ctx context.Context, c *http.Client, a Alert, text string) error {
	return postJSON(ctx, c, w.url, map[string]any{"alert": a, "text": text})
}

func postJSON(ctx context.Context, c *http.Client, endpoint string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.Do(req)
	if err != nil {
		return redact(err) // the URL can contain a token: never log it
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 200))
		return fmt.Errorf("HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

// redact keeps the error but drops the URL, which holds the Telegram token or webhook secret.
func redact(err error) error {
	if ue, ok := err.(*url.Error); ok {
		return fmt.Errorf("%s request failed: %v", ue.Op, ue.Err)
	}
	return err
}

// hostOnly shows "hooks.slack.com/…": enough to recognise the target, not enough to use it.
func hostOnly(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(invalid URL)"
	}
	return u.Host + "/…"
}

// Notifier filters alerts and delivers them in the background.
type Notifier struct {
	channels    []channel
	publicURL   string
	minSeverity string // critical | warning
	resolved    bool   // also send "resolved" and "back up"
	cooldown    time.Duration
	client      *http.Client
	queue       chan Alert

	mu       sync.Mutex
	lastSent map[string]time.Time // problem key + state -> last message
	down     map[string]bool      // cluster -> was down at the last check
}

// notifierFromEnv reads RELAY_PUBLIC_URL and RELAY_NOTIFY_*. No channel set = notifications off.
func notifierFromEnv() *Notifier {
	n := &Notifier{
		publicURL:   strings.TrimRight(os.Getenv("RELAY_PUBLIC_URL"), "/"),
		minSeverity: env("RELAY_NOTIFY_MIN_SEVERITY", "critical"),
		resolved:    env("RELAY_NOTIFY_RESOLVED", "true") != "false",
		cooldown:    10 * time.Minute,
		client:      &http.Client{Timeout: 10 * time.Second},
		queue:       make(chan Alert, 100),
		lastSent:    map[string]time.Time{},
		down:        map[string]bool{},
	}
	if n.minSeverity != "critical" && n.minSeverity != "warning" {
		log.Printf("notifications: RELAY_NOTIFY_MIN_SEVERITY=%q is not critical or warning, using critical", n.minSeverity)
		n.minSeverity = "critical"
	}
	if v := os.Getenv("RELAY_NOTIFY_COOLDOWN"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			n.cooldown = d
		} else {
			log.Printf("notifications: RELAY_NOTIFY_COOLDOWN=%q is not a duration like 10m, using 10m", v)
		}
	}
	token, chat := os.Getenv("RELAY_NOTIFY_TELEGRAM_TOKEN"), os.Getenv("RELAY_NOTIFY_TELEGRAM_CHAT_ID")
	switch {
	case token != "" && chat != "":
		n.channels = append(n.channels, telegram{token, chat})
	case token != "" || chat != "":
		log.Print("notifications: Telegram needs both the bot token and the chat id, skipping it")
	}
	if v := os.Getenv("RELAY_NOTIFY_SLACK_WEBHOOK"); v != "" {
		n.channels = append(n.channels, slack{v})
	}
	if v := os.Getenv("RELAY_NOTIFY_DISCORD_WEBHOOK"); v != "" {
		n.channels = append(n.channels, discord{v})
	}
	if v := os.Getenv("RELAY_NOTIFY_WEBHOOK_URL"); v != "" {
		n.channels = append(n.channels, webhook{v})
	}
	return n
}

func (n *Notifier) enabled() bool { return n != nil && len(n.channels) > 0 }

// Problem is called when a problem opens, resolves or comes back (Store.UpsertProblem).
func (n *Notifier) Problem(p Problem) {
	if !n.enabled() || (n.minSeverity == "critical" && p.Severity != "critical") {
		return
	}
	state := "open"
	if p.Resolved {
		if !n.resolved {
			return
		}
		state = "resolved"
	}
	// One message per problem and state per cooldown: a pod crashing every minute sends one.
	key := p.Cluster + "|" + problemKey(p) + "|" + state
	n.mu.Lock()
	if last, ok := n.lastSent[key]; ok && time.Since(last) < n.cooldown {
		n.mu.Unlock()
		return
	}
	n.lastSent[key] = time.Now()
	n.mu.Unlock()
	when := p.LastSeen
	if when.IsZero() { // agents leave it to the relay, like Store.UpsertProblem does
		when = time.Now().UTC()
	}
	n.enqueue(Alert{
		Kind: "problem", State: state, Cluster: p.Cluster, Severity: p.Severity,
		Namespace: p.Namespace, Object: p.Object, Reason: p.Reason, Message: p.Message,
		Time: when, URL: n.link(p.Cluster, "problems"),
	})
}

// checkClusters compares each cluster's health with the last check: down and back up.
// The first time a cluster is seen only records its state, so a relay restart sends nothing.
func (n *Notifier) checkClusters(clusters []ClusterView) {
	if !n.enabled() {
		return
	}
	for _, c := range clusters {
		isDown := c.Health == "down"
		n.mu.Lock()
		was, seen := n.down[c.ID]
		n.down[c.ID] = isDown
		n.mu.Unlock()
		if !seen || was == isDown || (!isDown && !n.resolved) {
			continue
		}
		a := Alert{Kind: "cluster", State: "up", Cluster: c.ID, Time: time.Now().UTC(), URL: n.link(c.ID, "overview")}
		if isDown {
			a.State, a.Severity = "down", "critical"
			a.Message = fmt.Sprintf("No report from its agent since %s.", c.LastSeen.Format(time.RFC1123))
		}
		n.enqueue(a)
	}
}

// watchClusters runs checkClusters every 30 s, as long as the relay runs.
func (n *Notifier) watchClusters(ctx context.Context, st *Store) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n.checkClusters(st.Clusters())
		}
	}
}

func (n *Notifier) link(cluster, section string) string {
	if n.publicURL == "" {
		return ""
	}
	return n.publicURL + "/c/" + url.PathEscape(cluster) + "/" + section
}

func (n *Notifier) enqueue(a Alert) {
	select {
	case n.queue <- a:
	default: // a channel is down and 100 alerts are waiting: drop rather than block the agents
		log.Printf("notifications: queue full, dropped %s %s %s", a.State, a.Cluster, a.Reason)
	}
}

// run delivers queued alerts to every channel, retrying each one twice.
func (n *Notifier) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case a := <-n.queue:
			text := n.text(a)
			for _, ch := range n.channels {
				var err error
				for try, wait := 1, time.Second; ; try, wait = try+1, wait*4 {
					if err = ch.Send(ctx, n.client, a, text); err == nil || try == 3 {
						break
					}
					time.Sleep(wait) // 1 s, then 4 s
				}
				if err != nil {
					log.Printf("notifications: %s failed: %v", ch.Name(), err)
				}
			}
		}
	}
}

// text is the human message, the same on every channel.
func (n *Notifier) text(a Alert) string {
	var b strings.Builder
	switch {
	case a.Kind == "test":
		b.WriteString("🦉 Owl test message: notifications work.")
	case a.Kind == "cluster" && a.State == "down":
		fmt.Fprintf(&b, "🔴 Cluster %s is down", a.Cluster)
	case a.Kind == "cluster":
		fmt.Fprintf(&b, "✅ Cluster %s is back", a.Cluster)
	case a.State == "resolved":
		fmt.Fprintf(&b, "✅ Resolved: %s", a.Reason)
	case a.Severity == "critical":
		fmt.Fprintf(&b, "🔴 %s", a.Reason)
	default:
		fmt.Fprintf(&b, "🟠 %s", a.Reason)
	}
	if a.Kind == "problem" {
		where := a.Object
		if a.Namespace != "" {
			where = a.Namespace + "/" + a.Object
		}
		fmt.Fprintf(&b, "\n%s: %s", a.Cluster, where)
	}
	if a.Message != "" && a.State != "resolved" {
		msg := a.Message
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		b.WriteString("\n" + msg)
	}
	if a.URL != "" {
		b.WriteString("\n" + a.URL)
	}
	return b.String()
}

type channelInfo struct {
	Name   string `json:"name"`
	Target string `json:"target"`
	OK     *bool  `json:"ok,omitempty"`    // test results only
	Error  string `json:"error,omitempty"` // test results only
}

// GET /v1/notifications: what is configured. Tokens and webhook paths are never sent.
func (s *server) getNotifications(w http.ResponseWriter, _ *http.Request) {
	n := s.notify
	out := map[string]any{"enabled": n.enabled(), "channels": []channelInfo{}}
	if n != nil {
		chans := []channelInfo{}
		for _, ch := range n.channels {
			chans = append(chans, channelInfo{Name: ch.Name(), Target: ch.Target()})
		}
		out["channels"] = chans
		out["minSeverity"] = n.minSeverity
		out["resolved"] = n.resolved
		out["cooldown"] = n.cooldown.String()
		out["publicUrl"] = n.publicURL
	}
	writeJSON(w, http.StatusOK, out)
}

// POST /v1/notifications/test: sends a test message to every channel now and reports each result.
func (s *server) testNotifications(w http.ResponseWriter, r *http.Request) {
	n := s.notify
	if !n.enabled() {
		writeErr(w, http.StatusConflict, "no notification channel is configured")
		return
	}
	a := Alert{Kind: "test", State: "test", Time: time.Now().UTC(), URL: n.publicURL}
	text := n.text(a)
	results := []channelInfo{}
	for _, ch := range n.channels {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		err := ch.Send(ctx, n.client, a, text)
		cancel()
		ok := err == nil
		info := channelInfo{Name: ch.Name(), Target: ch.Target(), OK: &ok}
		if err != nil {
			info.Error = err.Error()
		}
		results = append(results, info)
	}
	writeJSON(w, http.StatusOK, results)
}
