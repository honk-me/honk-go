package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const testKey = "honk_ab12cd34ef56_0123456789abcdefghijABCDEFGHIJ0123"

type cliMock struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []map[string]any
	keys   []string
}

func newCLIMock(t *testing.T, status int, body string, headers map[string]string) *cliMock {
	m := &cliMock{}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var b map[string]any
		_ = json.Unmarshal(raw, &b)
		m.mu.Lock()
		m.bodies = append(m.bodies, b)
		m.keys = append(m.keys, r.Header.Get("Idempotency-Key"))
		m.mu.Unlock()
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(m.Close)
	return m
}

const accepted = `{"id":"msg_1","status":"accepted","duplicate":false,"received_at":"2026-10-02T21:10:00.123Z"}`

func runCLI(t *testing.T, env map[string]string, in string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	stdout, stderr, stdin = &out, &errOut, strings.NewReader(in)
	code := run(context.Background(), args, func(k string) string { return env[k] })
	return code, out.String(), errOut.String()
}

func TestSendPrintsIDAndSerialisesFlags(t *testing.T) {
	m := newCLIMock(t, 202, accepted, nil)
	env := map[string]string{"HONK_URL": m.URL, "HONK_KEY": testKey, "HONK_SOURCE": "backup-host"}
	code, out, errOut := runCLI(t, env, "", "send", "--title", "Backup failed", "--message", "pg_dump exited with 1",
		"--severity", "error", "--priority", "high", "--category", "backups", "--channel", "nightly",
		"--group-key", "db/backup", "--event-type", "problem", "--url", "https://example.com/runs/1",
		"--image-url", "https://example.com/chart.png", "--meta", "host=db-1", "--meta", "exit:=1",
		"--ttl", "600", "--source-sequence", "9", "--occurred-at", "2026-10-02T21:10:00Z", "--idempotency-key", "backup-2026-10-02")
	if code != exitOK || out != "msg_1\n" {
		t.Fatalf("code %d out %q err %q", code, out, errOut)
	}
	want := `{"category":"backups","channel":"nightly","event_type":"problem","group_key":"db/backup","image_url":"https://example.com/chart.png","message":"pg_dump exited with 1","metadata":{"exit":1,"host":"db-1"},"occurred_at":"2026-10-02T21:10:00.000Z","priority":"high","severity":"error","source":"backup-host","source_sequence":9,"title":"Backup failed","ttl_seconds":600,"url":"https://example.com/runs/1"}`
	got, _ := json.Marshal(m.bodies[0])
	if string(got) != want {
		t.Fatalf("body\n got %s\nwant %s", got, want)
	}
	if m.keys[0] != "backup-2026-10-02" {
		t.Fatalf("key %q", m.keys[0])
	}
}

func TestProblemRecoveryAndStdin(t *testing.T) {
	m := newCLIMock(t, 202, accepted, nil)
	env := map[string]string{"HONK_URL": m.URL, "HONK_KEY": testKey, "HONK_IDEMPOTENCY_KEY": "ci-run-7"}
	if code, _, e := runCLI(t, env, "line 1\nline 2\n", "problem", "--group-key", "ci/deploy", "--title", "Deploy failed", "--message", "-", "--quiet"); code != 0 {
		t.Fatalf("problem: %d %s", code, e)
	}
	if code, out, e := runCLI(t, env, "", "recovery", "--group-key", "ci/deploy", "--message", "green again", "--json"); code != 0 || !strings.Contains(out, `"id":"msg_1"`) {
		t.Fatalf("recovery: %d %q %s", code, out, e)
	}
	if m.bodies[0]["event_type"] != "problem" || m.bodies[0]["severity"] != "error" || m.bodies[0]["message"] != "line 1\nline 2" {
		t.Fatalf("problem body %v", m.bodies[0])
	}
	if m.bodies[1]["event_type"] != "recovery" || m.bodies[1]["severity"] != "success" {
		t.Fatalf("recovery body %v", m.bodies[1])
	}
	if m.keys[0] != "ci-run-7" {
		t.Fatalf("HONK_IDEMPOTENCY_KEY not used: %q", m.keys[0])
	}
}

func TestExitCodes(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		headers map[string]string
		want    int
		stderr  string
	}{
		{"validation", 422, `{"error":{"code":"validation_failed","message":"Request validation failed","fields":[{"field":"x","code":"invalid_enum"}]}}`, nil, exitInvalid, "invalid_enum"},
		{"auth", 401, `{"error":{"code":"invalid_key","message":"Invalid or revoked ingestion key"}}`, nil, exitAuth, "invalid_key"},
		{"urgent", 403, `{"error":{"code":"priority_not_allowed","message":"no urgent"}}`, nil, exitAuth, "priority_not_allowed"},
		{"quota", 429, `{"error":{"code":"quota_exceeded","message":"Daily quota"}}`, map[string]string{"Retry-After": "3600"}, exitQuota, "Retry-After 1h0m0s"},
		{"conflict", 409, `{"error":{"code":"idempotency_conflict","message":"different payload"}}`, nil, exitConflict, "idempotency_conflict"},
		{"server", 503, `{"error":{"code":"unavailable","message":"paused"}}`, map[string]string{"Retry-After": "60"}, exitTemporary, "--idempotency-key"},
		{"notfound", 404, `404 page not found`, nil, exitError, "base address"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newCLIMock(t, tc.status, tc.body, tc.headers)
			code, _, errOut := runCLI(t, map[string]string{"HONK_URL": m.URL, "HONK_KEY": testKey}, "", "send", "--message", "x", "--retries", "0")
			if code != tc.want || !strings.Contains(errOut, tc.stderr) {
				t.Fatalf("code %d (want %d), stderr %q (want %q)", code, tc.want, errOut, tc.stderr)
			}
		})
	}
}

func TestUsageErrors(t *testing.T) {
	env := map[string]string{"HONK_URL": "https://honk.example.com", "HONK_KEY": testKey}
	for _, tc := range []struct {
		args []string
		env  map[string]string
		want int
		msg  string
	}{
		{nil, env, exitUsage, "Usage"},
		{[]string{"shout"}, env, exitUsage, "unknown command"},
		{[]string{"send"}, env, exitUsage, "--message is required"},
		{[]string{"send", "--message", "x", "extra"}, env, exitUsage, "unexpected argument"},
		{[]string{"problem", "--message", "x"}, env, exitUsage, "needs --group-key"},
		{[]string{"send", "--message", "x", "--occurred-at", "yesterday"}, env, exitUsage, "RFC 3339"},
		{[]string{"send", "--message", "x", "--meta", "novalue"}, env, exitUsage, "key=value"},
		{[]string{"send", "--message", "x"}, map[string]string{"HONK_KEY": testKey}, exitUsage, "HONK_URL"},
		{[]string{"send", "--message", "x"}, map[string]string{"HONK_URL": "https://h"}, exitUsage, "HONK_KEY"},
		{[]string{"send", "--message", "x", "--severity", "fatal"}, env, exitInvalid, "severity must be one of"},
	} {
		code, _, errOut := runCLI(t, tc.env, "", tc.args...)
		if code != tc.want || !strings.Contains(errOut, tc.msg) {
			t.Errorf("%v: code %d (want %d) stderr %q (want %q)", tc.args, code, tc.want, errOut, tc.msg)
		}
	}
}

func TestDryRunAndVersion(t *testing.T) {
	code, out, _ := runCLI(t, nil, "", "send", "--dry-run", "--message", "x", "--meta", "n:=2", "--title", "A & B")
	if code != 0 || !strings.Contains(out, `"title": "A & B"`) || !strings.Contains(out, `"n": 2`) {
		t.Fatalf("dry run %d %q", code, out)
	}
	code, out, _ = runCLI(t, nil, "", "version")
	if code != 0 || !strings.HasPrefix(out, "honk-me 0.") {
		t.Fatalf("version %d %q", code, out)
	}
}

func TestHornShortcutSubcommands(t *testing.T) {
	m := newCLIMock(t, 202, accepted, nil)
	env := map[string]string{"HONK_URL": m.URL, "HONK_KEY": testKey}
	cases := []struct {
		args []string
		want map[string]any
	}{
		{[]string{"loud", "Disk 91%"}, map[string]any{"message": "Disk 91%", "severity": "warning"}},
		{[]string{"blast", "Payments down", "Stripe answers 500", "--channel", "payments"}, map[string]any{"title": "Payments down", "message": "Stripe answers 500", "severity": "critical", "channel": "payments"}},
		{[]string{"beep", "--group-key", "deploy/web", "Deployed v4.2"}, map[string]any{"message": "Deployed v4.2", "severity": "success", "group_key": "deploy/web"}},
		{[]string{"light", "--message", "from a flag"}, map[string]any{"message": "from a flag", "severity": "info"}},
		{[]string{"long", "Backup failed", "exit 1"}, map[string]any{"title": "Backup failed", "message": "exit 1", "severity": "error"}},
		{[]string{"send", "--message", "x", "--severity", "LOUD"}, map[string]any{"message": "x", "severity": "warning"}},
		{[]string{"problem", "--group-key", "g", "--message", "x", "--severity", "blast"}, map[string]any{"message": "x", "severity": "critical", "group_key": "g", "event_type": "problem"}},
	}
	for i, tc := range cases {
		code, out, errOut := runCLI(t, env, "", tc.args...)
		if code != exitOK || out != "msg_1\n" {
			t.Fatalf("%v: code %d out %q err %q", tc.args, code, out, errOut)
		}
		got, _ := json.Marshal(m.bodies[i])
		want, _ := json.Marshal(tc.want)
		if string(got) != string(want) {
			t.Errorf("%v\n got %s\nwant %s", tc.args, got, want)
		}
	}
	if code, _, e := runCLI(t, env, "", "loud", "a", "b", "c"); code != exitUsage || !strings.Contains(e, "[TITLE] MESSAGE") {
		t.Errorf("three positionals: %d %q", code, e)
	}
	if code, _, e := runCLI(t, env, "", "loud"); code != exitUsage || !strings.Contains(e, "--message is required") {
		t.Errorf("no message: %d %q", code, e)
	}
	if code, _, e := runCLI(t, env, "", "loud", "x", "--severity", "light"); code != exitUsage {
		t.Errorf("--severity on a shortcut should be unknown: %d %q", code, e)
	}
}
