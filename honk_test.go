package honk

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

const testKey = "honk_ab12cd34ef56_0123456789abcdefghijABCDEFGHIJ0123"

var uuidV7Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type step struct {
	status  int
	headers map[string]string
	body    string
	delay   time.Duration
	drop    bool // close the connection without answering
}

type recorded struct {
	header http.Header
	path   string
	body   []byte
	at     time.Time
}

type mock struct {
	*httptest.Server
	mu    sync.Mutex
	steps []step
	reqs  []recorded
}

func newMock(t *testing.T, steps ...step) *mock {
	t.Helper()
	m := &mock{steps: steps}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		n := len(m.reqs)
		m.reqs = append(m.reqs, recorded{header: r.Header.Clone(), path: r.URL.Path, body: body, at: time.Now()})
		s := m.steps[min(n, len(m.steps)-1)]
		m.mu.Unlock()
		if s.drop {
			hj, _ := w.(http.Hijacker)
			conn, _, _ := hj.Hijack()
			conn.Close()
			return
		}
		if s.delay > 0 {
			select {
			case <-time.After(s.delay):
			case <-r.Context().Done():
				return
			}
		}
		for k, v := range s.headers {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(s.status)
		io.WriteString(w, s.body)
	}))
	t.Cleanup(m.Close)
	return m
}

func (m *mock) requests() []recorded {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]recorded(nil), m.reqs...)
}

func okStep(dup bool) step {
	d := "false"
	if dup {
		d = "true"
	}
	return step{status: 202, body: `{"id":"msg_01k6h3w4z5x6y7z8a9b0c1d2e3","status":"accepted","duplicate":` + d + `,"received_at":"2026-10-02T21:10:00.123Z"}`}
}

func errStep(status int, code string, retryAfter string, extra string) step {
	h := map[string]string{}
	if retryAfter != "" {
		h["Retry-After"] = retryAfter
	}
	return step{status: status, headers: h, body: `{"error":{"code":"` + code + `","message":"` + code + ` happened","request_id":"req_test"` + extra + `}}`}
}

func client(t *testing.T, url string, mod ...func(*Options)) *Client {
	t.Helper()
	o := Options{URL: url, Key: testKey, Backoff: Backoff{Base: time.Millisecond, Max: 5 * time.Millisecond}}
	for _, f := range mod {
		f(&o)
	}
	c, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func asError(t *testing.T, err error) *Error {
	t.Helper()
	var he *Error
	if !errors.As(err, &he) {
		t.Fatalf("expected *honk.Error, got %T %v", err, err)
	}
	return he
}

func TestSendSerialisesEveryFieldWithOpenAPINames(t *testing.T) {
	m := newMock(t, okStep(false))
	c := client(t, m.URL)
	occurred := time.Date(2026, 10, 2, 0, 10, 0, 0, time.FixedZone("EEST", 3*3600))
	acc, err := c.Send(context.Background(), Message{
		Title: "Redis connection failed", Message: "Billing API could not connect to Redis after 3 attempts.",
		Severity: SeverityError, Priority: PriorityHigh, Category: CategoryInfrastructure,
		Source: "billing-api", Environment: "production", Channel: "infrastructure",
		GroupKey: "billing/redis/connectivity", EventType: EventTypeProblem, OccurredAt: occurred,
		URL: "https://example.com/incidents/redis", ImageURL: "https://cdn.example.com/a.jpg?w=1&h=2",
		Metadata:   map[string]any{"host": "app-01", "attempts": 3, "retried": true},
		TTLSeconds: 3600, SourceSequence: Ptr[int64](42),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 10, 2, 21, 10, 0, 123e6, time.UTC)
	if acc.ID != "msg_01k6h3w4z5x6y7z8a9b0c1d2e3" || acc.Duplicate || !acc.ReceivedAt.Equal(want) {
		t.Fatalf("accepted = %+v", acc)
	}
	r := m.requests()[0]
	if r.path != "/v1/messages" {
		t.Fatalf("path %s", r.path)
	}
	for k, v := range map[string]string{"Authorization": "Bearer " + testKey, "Content-Type": "application/json", "User-Agent": "honk-go/" + Version} {
		if got := r.header.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if !uuidV7Re.MatchString(r.header.Get("Idempotency-Key")) {
		t.Errorf("Idempotency-Key %q is not a UUIDv7", r.header.Get("Idempotency-Key"))
	}
	var got map[string]any
	if err := json.Unmarshal(r.body, &got); err != nil {
		t.Fatal(err)
	}
	wantBody := map[string]any{
		"title": "Redis connection failed", "message": "Billing API could not connect to Redis after 3 attempts.",
		"severity": "error", "priority": "high", "category": "infrastructure", "source": "billing-api",
		"environment": "production", "channel": "infrastructure", "group_key": "billing/redis/connectivity",
		"event_type": "problem", "occurred_at": "2026-10-01T21:10:00.000Z", "url": "https://example.com/incidents/redis",
		"image_url": "https://cdn.example.com/a.jpg?w=1&h=2", "metadata": map[string]any{"host": "app-01", "attempts": 3.0, "retried": true},
		"ttl_seconds": 3600.0, "source_sequence": 42.0,
	}
	gj, _ := json.Marshal(got)
	wj, _ := json.Marshal(wantBody)
	if string(gj) != string(wj) {
		t.Fatalf("body\n got %s\nwant %s", gj, wj)
	}
	if strings.Contains(string(r.body), "\\u00") {
		t.Errorf("body escapes HTML: %s", r.body)
	}
}

func TestMinimalMessageAndDefaults(t *testing.T) {
	m := newMock(t, okStep(false))
	c := client(t, m.URL+"/v1/messages/", func(o *Options) { o.Defaults = Defaults{Source: "cron", Environment: "production"} })
	ctx := context.Background()
	if _, err := c.Send(ctx, Message{Message: "Backup finished in 42s"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Send(ctx, Message{Message: "b", Source: "laravel", Channel: "requests"}); err != nil {
		t.Fatal(err)
	}
	reqs := m.requests()
	if got := string(reqs[0].body); got != `{"message":"Backup finished in 42s","source":"cron","environment":"production"}` {
		t.Errorf("body 0 = %s", got)
	}
	if got := string(reqs[1].body); got != `{"message":"b","source":"laravel","environment":"production","channel":"requests"}` {
		t.Errorf("body 1 = %s", got)
	}
	if reqs[0].path != "/v1/messages" {
		t.Errorf("path %s", reqs[0].path)
	}
}

func TestHelpers(t *testing.T) {
	m := newMock(t, okStep(false))
	c := client(t, m.URL)
	ctx := context.Background()
	mustOK := func(_ *Accepted, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	mustOK(c.Problem(ctx, "db/backup", "Backup failed", "pg_dump exited with 1", WithIdempotencyKey("p-1"), WithChannel("backups")))
	mustOK(c.Recovery(ctx, "db/backup", "Backup OK", "pg_dump finished", WithSourceSequence(7)))
	mustOK(c.Warning(ctx, "Disk 85%", "app-01 /var", WithSeverity(SeverityInfo)))
	mustOK(c.Critical(ctx, "", "Payments down"))
	mustOK(c.Problem(ctx, "q/failed", "Queue", "failing", WithSeverity(SeverityCritical), WithMetadata(map[string]any{"a": 1}), WithMetadata(map[string]any{"b": "x"})))
	r := m.requests()
	wants := []string{
		`{"title":"Backup failed","message":"pg_dump exited with 1","severity":"error","channel":"backups","group_key":"db/backup","event_type":"problem"}`,
		`{"title":"Backup OK","message":"pg_dump finished","severity":"success","group_key":"db/backup","event_type":"recovery","source_sequence":7}`,
		`{"title":"Disk 85%","message":"app-01 /var","severity":"warning"}`,
		`{"message":"Payments down","severity":"critical"}`,
		`{"title":"Queue","message":"failing","severity":"critical","group_key":"q/failed","event_type":"problem","metadata":{"a":1,"b":"x"}}`,
	}
	for i, w := range wants {
		if string(r[i].body) != w {
			t.Errorf("request %d\n got %s\nwant %s", i, r[i].body, w)
		}
	}
	if r[0].header.Get("Idempotency-Key") != "p-1" {
		t.Errorf("key %q", r[0].header.Get("Idempotency-Key"))
	}
}

func TestRetriesReuseIdempotencyKeyAndBody(t *testing.T) {
	m := newMock(t, errStep(503, "unavailable", "0", ""), errStep(500, "internal", "", ""), step{drop: true}, okStep(false))
	acc, err := client(t, m.URL).Send(context.Background(), Message{Message: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if acc.ID == "" {
		t.Fatal("no id")
	}
	r := m.requests()
	if len(r) != 4 {
		t.Fatalf("%d requests", len(r))
	}
	for _, x := range r[1:] {
		if x.header.Get("Idempotency-Key") != r[0].header.Get("Idempotency-Key") || string(x.body) != string(r[0].body) {
			t.Fatal("retry changed the key or the body")
		}
	}
}

func TestRetriesTimedOutAttempt(t *testing.T) {
	m := newMock(t, step{status: 202, delay: 500 * time.Millisecond, body: okStep(false).body}, okStep(false))
	_, err := client(t, m.URL, func(o *Options) { o.Timeout = 100 * time.Millisecond }).Send(context.Background(), Message{Message: "x"}, WithIdempotencyKey("k-1"))
	if err != nil {
		t.Fatal(err)
	}
	r := m.requests()
	if len(r) != 2 || r[1].header.Get("Idempotency-Key") != "k-1" {
		t.Fatalf("requests %d", len(r))
	}
}

func TestHonoursRetryAfter(t *testing.T) {
	m := newMock(t, errStep(429, "rate_limited", "1", ""), okStep(false))
	start := time.Now()
	if _, err := client(t, m.URL).Send(context.Background(), Message{Message: "x"}); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < time.Second {
		t.Fatalf("did not wait for Retry-After (%s)", time.Since(start))
	}
}

func TestRetryAfterBeyondDeadlineFailsFast(t *testing.T) {
	m := newMock(t, errStep(429, "quota_exceeded", "7200", `,"limit":"messages_per_day"`))
	start := time.Now()
	_, err := client(t, m.URL).Send(context.Background(), Message{Message: "x"}, WithIdempotencyKey("q-1"))
	he := asError(t, err)
	if !errors.Is(err, ErrQuota) || he.Code != "quota_exceeded" || he.Status != 429 || he.RetryAfter != 2*time.Hour ||
		he.Attempts != 1 || he.IdempotencyKey != "q-1" || !he.Retryable() {
		t.Fatalf("error = %+v", he)
	}
	if time.Since(start) > time.Second || len(m.requests()) != 1 {
		t.Fatal("should fail fast without retrying")
	}
}

func TestGivesUpAfterRetries(t *testing.T) {
	m := newMock(t, errStep(503, "unavailable", "0", ""))
	_, err := client(t, m.URL, func(o *Options) { o.Retries = 2 }).Send(context.Background(), Message{Message: "x"})
	he := asError(t, err)
	if !errors.Is(err, ErrServer) || he.Attempts != 3 || he.RequestID != "req_test" || he.Code != "unavailable" {
		t.Fatalf("error = %+v", he)
	}
	if n := len(m.requests()); n != 3 {
		t.Fatalf("%d requests", n)
	}
}

func TestNoRetries(t *testing.T) {
	m := newMock(t, errStep(503, "unavailable", "0", ""))
	_, err := client(t, m.URL, func(o *Options) { o.Retries = NoRetries }).Send(context.Background(), Message{Message: "x"})
	if !errors.Is(err, ErrServer) || len(m.requests()) != 1 {
		t.Fatalf("err %v, %d requests", err, len(m.requests()))
	}
}

func TestStopsAtDeadline(t *testing.T) {
	m := newMock(t, errStep(503, "unavailable", "1", ""))
	start := time.Now()
	_, err := client(t, m.URL, func(o *Options) { o.Retries = 10; o.Deadline = 1500 * time.Millisecond }).Send(context.Background(), Message{Message: "x"})
	if !errors.Is(err, ErrServer) {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 1600*time.Millisecond {
		t.Fatalf("took %s", d)
	}
	if n := len(m.requests()); n != 2 {
		t.Fatalf("%d requests", n)
	}
}

func TestTimeoutError(t *testing.T) {
	m := newMock(t, step{status: 202, delay: 500 * time.Millisecond, body: okStep(false).body})
	_, err := client(t, m.URL, func(o *Options) { o.Timeout = 50 * time.Millisecond; o.Retries = 1 }).Send(context.Background(), Message{Message: "x"})
	he := asError(t, err)
	if he.Kind != KindTimeout || !errors.Is(err, ErrTimeout) || !errors.Is(err, ErrNetwork) || he.Attempts != 2 {
		t.Fatalf("error = %+v", he)
	}
}

func TestConnectionRefused(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	_, err := client(t, "http://"+addr, func(o *Options) { o.Retries = 1 }).Send(context.Background(), Message{Message: "x"})
	he := asError(t, err)
	if he.Kind != KindNetwork || he.Code != "network_error" || !uuidV7Re.MatchString(he.IdempotencyKey) || he.Attempts != 2 {
		t.Fatalf("error = %+v", he)
	}
}

func TestContextCancel(t *testing.T) {
	m := newMock(t, step{status: 202, delay: time.Second, body: okStep(false).body})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := client(t, m.URL).Send(ctx, Message{Message: "x"})
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v", err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	_, err = client(t, m.URL).Send(ctx2, Message{Message: "x"})
	if !errors.Is(err, context.Canceled) || asError(t, err).Kind != KindCanceled {
		t.Fatalf("err = %v", err)
	}
}

func TestErrorMappingNeverRetried(t *testing.T) {
	cases := []struct {
		step     step
		sentinel error
		kind     Kind
		code     string
	}{
		{errStep(422, "validation_failed", "", `,"fields":[{"field":"severity","code":"invalid_enum","message":"must be one of …"}]`), ErrValidation, KindValidation, "validation_failed"},
		{errStep(422, "unknown_field", "", `,"fields":[{"field":"foo","code":"not_allowed"}]`), ErrValidation, KindValidation, "unknown_field"},
		{errStep(413, "payload_too_large", "", ""), ErrValidation, KindValidation, "payload_too_large"},
		{errStep(401, "invalid_key", "", ""), ErrAuth, KindAuth, "invalid_key"},
		{errStep(403, "priority_not_allowed", "", ""), ErrAuth, KindAuth, "priority_not_allowed"},
		{errStep(403, "workspace_suspended", "", ""), ErrAuth, KindAuth, "workspace_suspended"},
		{errStep(409, "idempotency_conflict", "", ""), ErrConflict, KindConflict, "idempotency_conflict"},
		{errStep(404, "not_found", "", ""), nil, KindHTTP, "not_found"},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			m := newMock(t, tc.step)
			_, err := client(t, m.URL).Send(context.Background(), Message{Message: "x"})
			he := asError(t, err)
			if he.Kind != tc.kind || he.Code != tc.code || he.Status != tc.step.status || he.RequestID != "req_test" || he.Attempts != 1 || he.Retryable() || he.Local {
				t.Fatalf("error = %+v", he)
			}
			if tc.sentinel != nil && !errors.Is(err, tc.sentinel) {
				t.Fatalf("errors.Is(%v) false", tc.sentinel)
			}
			if n := len(m.requests()); n != 1 {
				t.Fatalf("%d requests", n)
			}
		})
	}
}

func TestValidationErrorFields(t *testing.T) {
	m := newMock(t, errStep(422, "validation_failed", "", `,"fields":[{"field":"image_url","code":"invalid_format","message":"must be https"}]`))
	_, err := client(t, m.URL).Send(context.Background(), Message{Message: "x"})
	he := asError(t, err)
	if len(he.Fields) != 1 || he.Fields[0] != (FieldError{Field: "image_url", Code: "invalid_format", Message: "must be https"}) {
		t.Fatalf("fields %+v", he.Fields)
	}
	if !strings.Contains(err.Error(), "image_url must be https") {
		t.Fatalf("message %q", err.Error())
	}
}

func TestRedirectNotFollowed(t *testing.T) {
	m := newMock(t, step{status: 301, headers: map[string]string{"Location": "https://honk.example.com/v1/messages"}})
	_, err := client(t, m.URL).Send(context.Background(), Message{Message: "x"})
	if err == nil || !strings.Contains(err.Error(), "redirect to https://honk.example.com") || len(m.requests()) != 1 {
		t.Fatalf("err = %v", err)
	}
}

func TestProxyHTMLErrorIsRetriedThenReported(t *testing.T) {
	m := newMock(t, step{status: 502, body: "<html>Bad Gateway</html>"})
	_, err := client(t, m.URL, func(o *Options) { o.Retries = 1 }).Send(context.Background(), Message{Message: "x"})
	he := asError(t, err)
	if he.Kind != KindServer || he.Status != 502 || he.Code != "" || len(m.requests()) != 2 {
		t.Fatalf("error = %+v", he)
	}
}

func TestDuplicate(t *testing.T) {
	m := newMock(t, okStep(true))
	acc, err := client(t, m.URL).Send(context.Background(), Message{Message: "x"}, WithIdempotencyKey("deploy-4812"))
	if err != nil || !acc.Duplicate {
		t.Fatalf("acc %+v err %v", acc, err)
	}
}

func TestSkipValidation(t *testing.T) {
	m := newMock(t, errStep(422, "validation_failed", "", `,"fields":[{"field":"severity","code":"invalid_enum"}]`))
	_, err := client(t, m.URL, func(o *Options) { o.SkipValidation = true }).Send(context.Background(), Message{Message: "x", Severity: "fatal"})
	if he := asError(t, err); he.Local || he.Kind != KindValidation {
		t.Fatalf("error = %+v", he)
	}
	if !strings.Contains(string(m.requests()[0].body), `"severity":"fatal"`) {
		t.Fatal("severity not sent")
	}
}

func TestNewValidatesOptions(t *testing.T) {
	for _, tc := range []struct {
		o    Options
		want string
	}{
		{Options{Key: testKey}, "HONK_URL"},
		{Options{URL: "honk.example.com", Key: testKey}, "https://"},
		{Options{URL: "https://h"}, "HONK_KEY"},
		{Options{URL: "https://h", Key: "hka_mobile"}, "honk_"},
	} {
		if _, err := New(tc.o); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("New(%+v) = %v, want error mentioning %q", tc.o, err, tc.want)
		}
	}
}

func TestFromEnv(t *testing.T) {
	m := newMock(t, okStep(false))
	t.Setenv("HONK_URL", m.URL)
	t.Setenv("HONK_KEY", testKey)
	t.Setenv("HONK_SOURCE", "cron")
	t.Setenv("HONK_ENVIRONMENT", "")
	t.Setenv("HONK_CHANNEL", "ops")
	c, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Send(context.Background(), Message{Message: "x"}); err != nil {
		t.Fatal(err)
	}
	if got := string(m.requests()[0].body); got != `{"message":"x","source":"cron","channel":"ops"}` {
		t.Fatalf("body %s", got)
	}
}

func TestUUIDv7(t *testing.T) {
	a := uuidv7(time.UnixMilli(1_700_000_000_000))
	b := uuidv7(time.UnixMilli(1_700_000_000_001))
	if !uuidV7Re.MatchString(a) || a >= b || strings.ReplaceAll(a[:13], "-", "") != "018bcfe56800" {
		t.Fatalf("a=%s b=%s", a, b)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Duration{
		"3": 3 * time.Second, " 120 ": 2 * time.Minute, "": 0, "soon": 0,
		"Fri, 02 Oct 2026 10:00:30 GMT": 30 * time.Second, "Fri, 02 Oct 2026 09:00:00 GMT": 0,
	} {
		if got := parseRetryAfter(in, now); got != want {
			t.Errorf("parseRetryAfter(%q) = %s, want %s", in, got, want)
		}
	}
}
