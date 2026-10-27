package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder is a fake channel that keeps what it was asked to send.
type recorder struct {
	mu    sync.Mutex
	texts []string
	fail  bool
}

func (r *recorder) Name() string   { return "recorder" }
func (r *recorder) Target() string { return "memory" }
func (r *recorder) Send(_ context.Context, _ *http.Client, _ Alert, text string) error {
	if r.fail {
		return errors.New("refused")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.texts = append(r.texts, text)
	return nil
}

func testNotifier(chs ...channel) *Notifier {
	return &Notifier{
		channels: chs, minSeverity: "critical", resolved: true, cooldown: 10 * time.Minute,
		client: http.DefaultClient, queue: make(chan Alert, 100),
		lastSent: map[string]time.Time{}, down: map[string]bool{},
	}
}

func TestNotifierFiltersAndCoolsDown(t *testing.T) {
	n := testNotifier(&recorder{})
	crash := Problem{Cluster: "dev", Source: "pod", Severity: "critical", Namespace: "wso2", Object: "pod/gw", Reason: "CrashLoopBackOff"}

	n.Problem(Problem{Cluster: "dev", Source: "event", Severity: "warning", Object: "pod/x", Reason: "BackOff"})
	if len(n.queue) != 0 {
		t.Fatal("a warning must be filtered out with minSeverity=critical")
	}
	n.Problem(crash)
	n.Problem(crash) // the same problem again, within the cooldown
	if len(n.queue) != 1 {
		t.Fatalf("queued %d, want 1 (cooldown)", len(n.queue))
	}
	crash.Resolved = true
	n.Problem(crash)
	if len(n.queue) != 2 {
		t.Fatalf("queued %d, want 2 (resolved is a new state)", len(n.queue))
	}

	quiet := testNotifier(&recorder{})
	quiet.resolved = false
	quiet.Problem(crash)
	if len(quiet.queue) != 0 {
		t.Fatal("resolved=false must not send recoveries")
	}

	var off *Notifier // no notifier at all: must be safe to call
	off.Problem(crash)
}

func TestClusterDownAndBackUp(t *testing.T) {
	n := testNotifier(&recorder{})
	healthy := []ClusterView{{ID: "dev", Health: "healthy"}}
	down := []ClusterView{{ID: "dev", Health: "down", LastSeen: time.Now().Add(-3 * time.Minute)}}

	n.checkClusters(down) // first sight: only remembered, so a relay restart sends nothing
	if len(n.queue) != 0 {
		t.Fatal("the first check must not alert")
	}
	n.checkClusters(healthy)
	n.checkClusters(healthy)
	n.checkClusters(down)
	n.checkClusters(down)
	if len(n.queue) != 2 {
		t.Fatalf("queued %d, want 2 (back up, then down)", len(n.queue))
	}
	if a := <-n.queue; a.State != "up" {
		t.Fatalf("first alert %s, want up", a.State)
	}
	if a := <-n.queue; a.State != "down" || !strings.Contains(n.text(a), "is down") {
		t.Fatalf("second alert %+v", a)
	}
}

func TestTelegramAndWebhookDelivery(t *testing.T) {
	type call struct{ path, body string }
	got := make(chan call, 4)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- call{r.URL.Path, string(b)}
	}))
	defer fake.Close()
	old := telegramAPI
	telegramAPI = fake.URL
	defer func() { telegramAPI = old }()

	n := testNotifier(telegram{token: "123:SECRET", chatID: "42"}, webhook{url: fake.URL + "/hook"})
	n.publicURL = "https://owl.example.com"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go n.run(ctx)

	n.Problem(Problem{Cluster: "dev", Severity: "critical", Namespace: "wso2", Object: "pod/gw",
		Reason: "CrashLoopBackOff", Message: "back-off 5m0s restarting failed container"})

	for i := 0; i < 2; i++ {
		select {
		case c := <-got:
			switch c.path {
			case "/bot123:SECRET/sendMessage":
				var m struct {
					ChatID string `json:"chat_id"`
					Text   string `json:"text"`
				}
				json.Unmarshal([]byte(c.body), &m)
				want := "🔴 CrashLoopBackOff\ndev: wso2/pod/gw\nback-off 5m0s restarting failed container\nhttps://owl.example.com/c/dev/problems"
				if m.ChatID != "42" || m.Text != want {
					t.Fatalf("telegram got %+v", m)
				}
			case "/hook":
				var m struct{ Alert Alert }
				json.Unmarshal([]byte(c.body), &m)
				if m.Alert.Reason != "CrashLoopBackOff" || m.Alert.State != "open" || time.Since(m.Alert.Time) > time.Minute {
					t.Fatalf("webhook got %s", c.body)
				}
			default:
				t.Fatalf("unexpected call %s", c.path)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("no delivery")
		}
	}
}

func TestErrorsNeverShowTheToken(t *testing.T) {
	old := telegramAPI
	telegramAPI = "http://127.0.0.1:1" // nothing listens there
	defer func() { telegramAPI = old }()
	err := telegram{token: "123:SECRET", chatID: "42"}.Send(context.Background(), http.DefaultClient, Alert{}, "x")
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("err = %v", err)
	}
}

func TestNotificationEndpoints(t *testing.T) {
	gone := httptest.NewServer(http.NotFoundHandler()) // a Slack webhook that was deleted
	defer gone.Close()
	s := &server{store: NewStore(), broker: NewBroker(),
		notify: testNotifier(&recorder{}, &recorder{fail: true}, slack{url: gone.URL + "/services/T0/B0/XYZ"})}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/notifications", requireToken(appTok, s.getNotifications))
	mux.HandleFunc("POST /v1/notifications/test", requireToken(appTok, s.testNotifications))
	ts := httptest.NewServer(mux)
	defer ts.Close()

	body, _ := io.ReadAll(do(t, "GET", ts.URL+"/v1/notifications", appTok, "").Body)
	if strings.Contains(string(body), "XYZ") || !strings.Contains(string(body), strings.TrimPrefix(gone.URL, "http://")+"/…") {
		t.Fatalf("status leaks or misses the target: %s", body)
	}

	// Only the first recorder succeeds: the second refuses, the Slack webhook answers 404.
	var res []channelInfo
	json.NewDecoder(do(t, "POST", ts.URL+"/v1/notifications/test", appTok, "").Body).Decode(&res)
	if len(res) != 3 || !*res[0].OK || *res[1].OK || res[1].Error != "refused" || *res[2].OK || !strings.HasPrefix(res[2].Error, "HTTP 404") {
		t.Fatalf("test results = %+v", res)
	}

	off := &server{store: NewStore(), broker: NewBroker()}
	mux2 := http.NewServeMux()
	mux2.HandleFunc("POST /v1/notifications/test", requireToken(appTok, off.testNotifications))
	ts2 := httptest.NewServer(mux2)
	defer ts2.Close()
	if r := do(t, "POST", ts2.URL+"/v1/notifications/test", appTok, ""); r.StatusCode != http.StatusConflict {
		t.Fatalf("no channels: %d, want 409", r.StatusCode)
	}
}
