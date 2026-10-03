package honk

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

func TestLocalValidation(t *testing.T) {
	c, err := New(Options{URL: "https://honk.example.com", Key: testKey})
	if err != nil {
		t.Fatal(err)
	}
	many := map[string]any{}
	for i := 0; i < 17; i++ {
		many[fmt.Sprintf("k%d", i)] = i
	}
	big := map[string]any{}
	for i := 0; i < 16; i++ {
		big[fmt.Sprintf("k%d", i)] = strings.Repeat(`"`, 500)
	}
	cases := []struct {
		m           Message
		field, code string
	}{
		{Message{}, "message", "required"},
		{Message{Message: "   "}, "message", "too_short"},
		{Message{Message: strings.Repeat("x", 8193)}, "message", "too_long"},
		{Message{Message: strings.Repeat("é", 4097)}, "message", "too_long"},
		{Message{Message: "bell\a"}, "message", "invalid_format"},
		{Message{Message: "bad \xff utf8"}, "message", "invalid_utf8"},
		{Message{Message: "x", Title: strings.Repeat("t", 161)}, "title", "too_long"},
		{Message{Message: "x", Title: "two\nlines"}, "title", "invalid_format"},
		{Message{Message: "x", Title: "  "}, "title", "too_short"},
		{Message{Message: "x", Environment: strings.Repeat("e", 33)}, "environment", "too_long"},
		{Message{Message: "x", GroupKey: strings.Repeat("g", 129)}, "group_key", "too_long"},
		{Message{Message: "x", Severity: "fatal"}, "severity", "invalid_enum"},
		{Message{Message: "x", Priority: "asap"}, "priority", "invalid_enum"},
		{Message{Message: "x", Category: "crm"}, "category", "invalid_enum"},
		{Message{Message: "x", EventType: EventTypeRecovery}, "group_key", "requires_group_key"},
		{Message{Message: "x", SourceSequence: Ptr[int64](3)}, "source_sequence", "requires_group_key"},
		{Message{Message: "x", GroupKey: "g", SourceSequence: Ptr[int64](-1)}, "source_sequence", "out_of_range"},
		{Message{Message: "x", GroupKey: "g", SourceSequence: Ptr[int64](1 << 53)}, "source_sequence", "out_of_range"},
		{Message{Message: "x", URL: "http://example.com"}, "url", "invalid_format"},
		{Message{Message: "x", URL: "https://user:pw@example.com"}, "url", "invalid_format"},
		{Message{Message: "x", URL: "https://example.com/" + strings.Repeat("a", 2048)}, "url", "invalid_format"},
		{Message{Message: "x", ImageURL: "https://cdn.example.com/a.jpg#x"}, "image_url", "invalid_format"},
		{Message{Message: "x", ImageURL: "https://cdn.example.com:99999/a.jpg"}, "image_url", "invalid_format"},
		{Message{Message: "x", Metadata: map[string]any{"nested": map[string]any{"a": 1}}}, "metadata.nested", "invalid_format"},
		{Message{Message: "x", Metadata: map[string]any{"bad key": 1}}, "metadata.bad key", "invalid_format"},
		{Message{Message: "x", Metadata: map[string]any{"v": strings.Repeat("v", 513)}}, "metadata.v", "invalid_format"},
		{Message{Message: "x", Metadata: map[string]any{"n": math.NaN()}}, "metadata.n", "invalid_format"},
		{Message{Message: "x", Metadata: map[string]any{"nil": nil}}, "metadata.nil", "invalid_format"},
		{Message{Message: "x", Metadata: many}, "metadata", "too_long"},
		{Message{Message: "x", TTLSeconds: 59}, "ttl_seconds", "out_of_range"},
		{Message{Message: "x", TTLSeconds: 86401}, "ttl_seconds", "out_of_range"},
		{Message{Message: strings.Repeat("x", 8000), Metadata: big}, "body", "too_long"},
	}
	for _, tc := range cases {
		t.Run(tc.field+"/"+tc.code, func(t *testing.T) {
			_, err := c.Send(context.Background(), tc.m)
			he := asError(t, err)
			if !he.Local || he.Kind != KindValidation || !errors.Is(err, ErrValidation) || he.Attempts != 0 {
				t.Fatalf("error = %+v", he)
			}
			for _, f := range he.Fields {
				if f.Field == tc.field && f.Code == tc.code {
					return
				}
			}
			t.Fatalf("want %s/%s, got %+v", tc.field, tc.code, he.Fields)
		})
	}
}

func TestAllErrorsReportedAtOnce(t *testing.T) {
	_, err := encodeBody(Message{Severity: "x", URL: "ftp://a"}, Defaults{}, false)
	he := asError(t, err)
	var names []string
	for _, f := range he.Fields {
		names = append(names, f.Field)
	}
	if strings.Join(names, ",") != "message,severity,url" {
		t.Fatalf("fields %v", names)
	}
	if !strings.Contains(err.Error(), "honk: invalid message (message message is required; severity must be one of") {
		t.Fatalf("message %q", err.Error())
	}
}

func TestValidEdgeCases(t *testing.T) {
	body, err := encodeBody(Message{
		Message: "line1\nline2\ttab\r\n", Title: strings.Repeat("t", 160),
		OccurredAt: time.Date(2026, 10, 2, 0, 10, 0, 123456789, time.UTC),
		URL:        "https://[::1]:8443/path?q=1#frag", ImageURL: "https://cdn.example.com/a.jpg?size=2",
		Metadata: map[string]any{"a.b-c_d": "v", "n": 1.5, "b": false, "u": uint8(3)},
		GroupKey: "g", SourceSequence: Ptr[int64](1<<53 - 1), TTLSeconds: 60,
	}, Defaults{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"occurred_at":"2026-10-02T00:10:00.123Z"`) || !strings.Contains(string(body), `"source_sequence":9007199254740991`) {
		t.Fatalf("body %s", body)
	}
}

func TestInvalidIdempotencyKeys(t *testing.T) {
	c, _ := New(Options{URL: "https://honk.example.com", Key: testKey})
	for _, k := range []string{"has space", strings.Repeat("x", 129), "ünicode"} {
		_, err := c.Send(context.Background(), Message{Message: "x"}, WithIdempotencyKey(k))
		he := asError(t, err)
		if !he.Local || he.Fields[0].Field != "Idempotency-Key" {
			t.Errorf("key %q: %+v", k, he)
		}
	}
}
