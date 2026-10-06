package honk

import (
	"context"
	"encoding/json"
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
	call := Action{Title: "Call", URL: "tel:+15550134"}
	act := func(title, url string) []Action { return []Action{{Title: title, URL: url}} }
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
		{Message{Message: "x", Actions: []Action{call, call, call, call}}, "actions", "too_long"},
		{Message{Message: "x", Actions: act("", "tel:+15550134")}, "actions[0].title", "required"},
		{Message{Message: "x", Actions: act("  ", "tel:+15550134")}, "actions[0].title", "required"},
		{Message{Message: "x", Actions: act(strings.Repeat("t", 41), "tel:+15550134")}, "actions[0].title", "too_long"},
		{Message{Message: "x", Actions: act("Call\nEmily", "tel:+15550134")}, "actions[0].title", "invalid_format"},
		{Message{Message: "x", Actions: act("bad \xff", "tel:+15550134")}, "actions[0].title", "invalid_utf8"},
		{Message{Message: "x", Actions: act("Call", "")}, "actions[0].url", "required"},
		{Message{Message: "x", Actions: act("Open", "https://example.com/"+strings.Repeat("a", 2030))}, "actions[0].url", "too_long"},
		{Message{Message: "x", Actions: []Action{call, {Title: "Open", URL: "http://example.com"}}}, "actions[1].url", "invalid_format"},
		{Message{Message: "x", Actions: act("Open", "https://user:pw@example.com")}, "actions[0].url", "invalid_format"},
		{Message{Message: "x", Actions: act("Run", "javascript:alert(1)")}, "actions[0].url", "invalid_format"},
		{Message{Message: "x", Actions: act("Open", "shop://orders/4812")}, "actions[0].url", "invalid_format"},
		{Message{Message: "x", Actions: act("Reply", "mailto:")}, "actions[0].url", "invalid_format"},
		{Message{Message: "x", Actions: act("Reply", "mailto:emily?subject=Hi")}, "actions[0].url", "invalid_format"},
		{Message{Message: "x", Actions: act("Reply", "mailto:emily@example.com?subject=Your quote")}, "actions[0].url", "invalid_format"},
		{Message{Message: "x", Actions: act("Call", "tel:call-me")}, "actions[0].url", "invalid_format"},
		{Message{Message: "x", Actions: act("Call", "tel:+1 555 0134")}, "actions[0].url", "invalid_format"},
		{Message{Message: "x", Actions: act("Text", "sms:?body=hi")}, "actions[0].url", "invalid_format"},
		{Message{Message: "x", Actions: act("Reply", "mailto:emily@localhost")}, "actions[0].url", "invalid_format"},
		{Message{Message: "x", Actions: act("Reply", "mailto:emily@example.com,ana@example.com")}, "actions[0].url", "invalid_format"},
		{Message{Message: "x", Actions: act("Reply", "mailto:emily@example.com?cc=boss@example.com")}, "actions[0].url", "invalid_format"},
		{Message{Message: "x", Actions: act("Reply", "mailto:emily@example.com?subject=%zz")}, "actions[0].url", "invalid_format"},
		{Message{Message: "x", Actions: act("Text", "sms:+15550134?subject=Hi")}, "actions[0].url", "invalid_format"},
		{Message{Message: "x", Actions: act("Open", "https://example.com/a\u00a0b")}, "actions[0].url", "invalid_format"},
		{Message{Message: "x", Actions: act("Call", "   ")}, "actions[0].url", "required"},
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

func TestValidActions(t *testing.T) {
	for _, u := range []string{
		"https://shop.example.com:8443/admin/requests/4812?tab=notes#reply",
		"HTTPS://shop.example.com",
		"mailto:emily@example.com",
		"MailTo:emily.carter+quotes@example.co.uk?subject=Your%20quote&body=Hi%20Emily%2C",
		"tel:+15550134",
		"TEL:+1-(555)-013.4",
		"tel://+40721000000",
		"sms:+15550134",
		"SMS:0721000000?body=On%20my%20way",
		"mailto:%65mily@example.com?body=a+b&subject=",
		"  tel:+15550134  ",
	} {
		body, err := encodeBody(Message{Message: "x", Actions: []Action{{Title: "Open", URL: u}}}, Defaults{}, false)
		if err != nil {
			t.Errorf("%s: %v", u, err)
			continue
		}
		var got struct{ Actions []Action }
		if err := json.Unmarshal(body, &got); err != nil || len(got.Actions) != 1 || got.Actions[0].URL != u {
			t.Errorf("%s: body %s", u, body)
		}
	}
	title := "  " + strings.Repeat("é", 39) + "🚀  " // 40 characters once trimmed
	if _, err := encodeBody(Message{Message: "x", Actions: []Action{{Title: title, URL: "tel:+15550134"}}}, Defaults{}, false); err != nil {
		t.Fatal(err)
	}
}

func TestMoreThanThreeActionsIsOneErrorLikeOnTheServer(t *testing.T) {
	_, err := encodeBody(Message{Message: "x", Actions: make([]Action, 4)}, Defaults{}, false)
	he := asError(t, err)
	if len(he.Fields) != 1 || he.Fields[0].Field != "actions" || he.Fields[0].Code != "too_long" {
		t.Fatalf("fields %+v", he.Fields)
	}
}

func TestAllActionErrorsReportedWithTheirIndex(t *testing.T) {
	_, err := encodeBody(Message{Message: "x", Actions: []Action{
		{Title: "Reply", URL: "mailto:emily@example.com"}, {URL: "ftp://files.example.com"}, {Title: "Call"},
	}}, Defaults{}, false)
	he := asError(t, err)
	var got []string
	for _, f := range he.Fields {
		got = append(got, f.Field+":"+f.Code)
	}
	if strings.Join(got, ",") != "actions[1].title:required,actions[1].url:invalid_format,actions[2].url:required" {
		t.Fatalf("fields %v", got)
	}
}

func TestSkipValidationSendsActionsAsGiven(t *testing.T) {
	body, err := encodeBody(Message{Message: "x", Actions: []Action{{Title: "Run", URL: "javascript:alert(1)"}}}, Defaults{}, true)
	if err != nil || string(body) != `{"message":"x","actions":[{"title":"Run","url":"javascript:alert(1)"}]}` {
		t.Fatalf("body %s err %v", body, err)
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
