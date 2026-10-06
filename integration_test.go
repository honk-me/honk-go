package honk_test

// Runs against a real Honk server when HONK_URL and HONK_KEY are set (see ../README.md,
// "Integration tests"); skipped otherwise.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	honk "github.com/honk-me/honk-go"
)

func integrationClient(t *testing.T, mod ...func(*honk.Options)) *honk.Client {
	t.Helper()
	if os.Getenv("HONK_URL") == "" || os.Getenv("HONK_KEY") == "" {
		t.Skip("set HONK_URL and HONK_KEY to run against a real server")
	}
	o := honk.OptionsFromEnv()
	o.Defaults = honk.Defaults{Source: "sdk-go-it", Environment: "test"}
	for _, f := range mod {
		f(&o)
	}
	c, err := honk.New(o)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

var run = fmt.Sprintf("go-%d", time.Now().UnixNano())

func TestIntegrationMinimal(t *testing.T) {
	c := integrationClient(t)
	acc, err := c.Send(context.Background(), honk.Message{Message: "minimal " + run})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(acc.ID, "msg_") || acc.Duplicate || acc.ReceivedAt.IsZero() {
		t.Fatalf("accepted %+v", acc)
	}
}

func TestIntegrationAllFieldsAndDuplicate(t *testing.T) {
	c := integrationClient(t)
	m := honk.Message{
		Title: "Customer request " + run, Message: "Ana (Acme) asked for a quote:\nonline shop, 40 products",
		Severity: honk.SeverityInfo, Priority: honk.PriorityHigh, Category: honk.CategoryCustomers,
		Source: "sdk-go-it", Environment: "test", Channel: "requests", GroupKey: "requests/" + run,
		EventType: honk.EventTypeEvent, OccurredAt: time.Now(), URL: "https://example.com/admin/requests/4812",
		ImageURL:   "https://example.com/images/quote.png",
		Metadata:   map[string]any{"request_id": "4812", "amount": 1250.5, "vip": true},
		TTLSeconds: 600, SourceSequence: honk.Ptr[int64](1),
	}
	key := "it-" + honk.NewIdempotencyKey()
	first, err := c.Send(context.Background(), m, honk.WithIdempotencyKey(key))
	if err != nil {
		t.Fatal(err)
	}
	again, err := c.Send(context.Background(), m, honk.WithIdempotencyKey(key))
	if err != nil {
		t.Fatal(err)
	}
	if first.Duplicate || !again.Duplicate || again.ID != first.ID || !again.ReceivedAt.Equal(first.ReceivedAt) {
		t.Fatalf("first %+v again %+v", first, again)
	}
}

func TestIntegrationActionsAndDuplicate(t *testing.T) {
	c := integrationClient(t)
	m := honk.Message{
		Title: "Customer request " + run, Message: "Emily Carter (Acme) asked for a quote: online shop, 40 products",
		Category: honk.CategoryCustomers, GroupKey: "requests/" + run + "/actions",
		Actions: []honk.Action{
			{Title: "Reply", URL: "mailto:emily@example.com?subject=Your%20quote"},
			{Title: "Call Emily", URL: "tel:+15550134"},
			{Title: "Open request", URL: "https://example.com/admin/requests/4812"},
		},
	}
	key := "it-" + honk.NewIdempotencyKey()
	first, err := c.Send(context.Background(), m, honk.WithIdempotencyKey(key))
	if err != nil {
		t.Fatal(err)
	}
	again, err := c.Send(context.Background(), m, honk.WithIdempotencyKey(key))
	if err != nil || !again.Duplicate || again.ID != first.ID {
		t.Fatalf("first %+v again %+v err %v", first, again, err)
	}
}

func TestIntegrationConflict(t *testing.T) {
	c := integrationClient(t)
	key := "it-" + honk.NewIdempotencyKey()
	ctx := context.Background()
	if _, err := c.Info(ctx, "first", "payload A "+run, honk.WithIdempotencyKey(key)); err != nil {
		t.Fatal(err)
	}
	_, err := c.Info(ctx, "first", "payload B "+run, honk.WithIdempotencyKey(key))
	var he *honk.Error
	if !errors.Is(err, honk.ErrConflict) || !errors.As(err, &he) || he.Code != "idempotency_conflict" || he.Status != 409 || he.IdempotencyKey != key {
		t.Fatalf("err = %v", err)
	}
}

func TestIntegrationProblemRecovery(t *testing.T) {
	c := integrationClient(t)
	group := "it/go/" + run
	ctx := context.Background()
	p, err := c.Problem(ctx, group, "Backup failed", "pg_dump exited with 1", honk.WithSourceSequence(1))
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Recovery(ctx, group, "Backup OK", "pg_dump finished", honk.WithSourceSequence(2))
	if err != nil {
		t.Fatal(err)
	}
	if p.ID == r.ID {
		t.Fatal("same id")
	}
}

func TestIntegrationHornAliasIsCanonical(t *testing.T) {
	c := integrationClient(t)
	key := "it-" + honk.NewIdempotencyKey()
	ctx := context.Background()
	first, err := c.Loud(ctx, "Disk 91%", "/var on app-01 "+run, honk.WithIdempotencyKey(key))
	if err != nil {
		t.Fatal(err)
	}
	again, err := c.Send(ctx, honk.Message{Title: "Disk 91%", Message: "/var on app-01 " + run, Severity: "WARNING"}, honk.WithIdempotencyKey(key))
	if err != nil || !again.Duplicate || again.ID != first.ID {
		t.Fatalf("first %+v again %+v err %v", first, again, err)
	}
}

func TestIntegrationWrongKey(t *testing.T) {
	c := integrationClient(t, func(o *honk.Options) { o.Key = "honk_000000000000_00000000000000000000000000000000" })
	_, err := c.Send(context.Background(), honk.Message{Message: "x"})
	var he *honk.Error
	if !errors.Is(err, honk.ErrAuth) || !errors.As(err, &he) || he.Code != "invalid_key" || he.Status != 401 {
		t.Fatalf("err = %v", err)
	}
}

func TestIntegrationUrgentNotAllowed(t *testing.T) {
	c := integrationClient(t)
	acc, err := c.Send(context.Background(), honk.Message{Message: "urgent " + run, Priority: honk.PriorityUrgent})
	if err == nil {
		t.Logf("key allows urgent: accepted %s", acc.ID)
		return
	}
	var he *honk.Error
	if !errors.Is(err, honk.ErrAuth) || !errors.As(err, &he) || he.Code != "priority_not_allowed" || he.Status != 403 {
		t.Fatalf("err = %v", err)
	}
}

func TestIntegrationServerValidation(t *testing.T) {
	c := integrationClient(t, func(o *honk.Options) { o.SkipValidation = true })
	_, err := c.Send(context.Background(), honk.Message{Message: "x", Severity: "fatal", TTLSeconds: 5})
	var he *honk.Error
	if !errors.Is(err, honk.ErrValidation) || !errors.As(err, &he) || he.Local || he.Status != 422 || he.Code != "validation_failed" {
		t.Fatalf("err = %v", err)
	}
	var fields []string
	for _, f := range he.Fields {
		fields = append(fields, f.Field+":"+f.Code)
	}
	sort.Strings(fields)
	if strings.Join(fields, ",") != "severity:invalid_enum,ttl_seconds:out_of_range" {
		t.Fatalf("fields %v", fields)
	}
}

func TestIntegrationServerActionValidation(t *testing.T) {
	c := integrationClient(t, func(o *honk.Options) { o.SkipValidation = true })
	_, err := c.Send(context.Background(), honk.Message{Message: "x", Actions: []honk.Action{
		{Title: "Call", URL: "tel:+15550134"}, {Title: "Run", URL: "javascript:alert(1)"},
	}})
	var he *honk.Error
	if !errors.Is(err, honk.ErrValidation) || !errors.As(err, &he) || he.Local || he.Status != 422 {
		t.Fatalf("err = %v", err)
	}
	if len(he.Fields) != 1 || he.Fields[0].Field != "actions[1].url" || he.Fields[0].Code != "invalid_format" {
		t.Fatalf("fields %+v", he.Fields)
	}
}
