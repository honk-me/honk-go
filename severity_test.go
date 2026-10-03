package honk

import (
	"context"
	"strings"
	"testing"
)

func TestHornConstantsAreAliases(t *testing.T) {
	pairs := map[Severity]Severity{Light: SeverityInfo, Beep: SeveritySuccess, Loud: SeverityWarning, Long: SeverityError, Blast: SeverityCritical}
	for alias, canonical := range pairs {
		if alias != canonical {
			t.Errorf("%s != %s", alias, canonical)
		}
	}
}

func TestParseSeverity(t *testing.T) {
	for in, want := range map[string]Severity{
		"loud": SeverityWarning, "LOUD": SeverityWarning, " Blast ": SeverityCritical, "Beep": SeveritySuccess,
		"light": SeverityInfo, "long": SeverityError, "warning": SeverityWarning, "ERROR": SeverityError,
	} {
		if got, ok := ParseSeverity(in); !ok || got != want {
			t.Errorf("ParseSeverity(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	if _, ok := ParseSeverity("fatal"); ok {
		t.Error("fatal accepted")
	}
}

func TestAliasesAreSentCanonical(t *testing.T) {
	m := newMock(t, okStep(false))
	c := client(t, m.URL)
	ctx := context.Background()
	for _, s := range []Severity{"loud", "Warning", Loud, "LOUD"} {
		if _, err := c.Send(ctx, Message{Message: "x", Severity: s}, WithIdempotencyKey("k")); err != nil {
			t.Fatal(err)
		}
	}
	for i, r := range m.requests() {
		if string(r.body) != `{"message":"x","severity":"warning"}` {
			t.Errorf("request %d: %s", i, r.body)
		}
	}
}

func TestAliasesNormalizedWithSkipValidation(t *testing.T) {
	body, err := encodeBody(Message{Message: "x", Severity: "BLAST"}, Defaults{}, true)
	if err != nil || string(body) != `{"message":"x","severity":"critical"}` {
		t.Fatalf("%s %v", body, err)
	}
	body, _ = encodeBody(Message{Message: "x", Severity: "fatal"}, Defaults{}, true)
	if string(body) != `{"message":"x","severity":"fatal"}` {
		t.Fatalf("%s", body)
	}
}

func TestUnknownSeverityNamesTheHonkScale(t *testing.T) {
	_, err := EncodeMessage(Message{Message: "x", Severity: "fatal"}, Defaults{})
	if err == nil || !strings.Contains(err.Error(), "severity must be one of light (info), beep (success), loud (warning), long (error), blast (critical)") {
		t.Fatalf("err = %v", err)
	}
}

func TestHornHelpers(t *testing.T) {
	m := newMock(t, okStep(false))
	c := client(t, m.URL)
	ctx := context.Background()
	calls := []func() (*Accepted, error){
		func() (*Accepted, error) { return c.Light(ctx, "a", "m") },
		func() (*Accepted, error) { return c.Beep(ctx, "b", "m") },
		func() (*Accepted, error) { return c.Loud(ctx, "c", "m", WithGroupKey("disk/var")) },
		func() (*Accepted, error) { return c.Long(ctx, "d", "m") },
		func() (*Accepted, error) { return c.Blast(ctx, "e", "m", WithSeverity(Light)) },
		func() (*Accepted, error) { return c.Problem(ctx, "g", "p", "m", WithSeverity(Blast)) },
	}
	for _, call := range calls {
		if _, err := call(); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"info", "success", "warning", "error", "critical", "critical"}
	for i, r := range m.requests() {
		if !strings.Contains(string(r.body), `"severity":"`+want[i]+`"`) {
			t.Errorf("request %d: %s (want %s)", i, r.body, want[i])
		}
	}
}
