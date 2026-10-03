// Command honk-me sends one event to Honk from a shell script, cron job or CI step.
//
//	honk-me loud "Disk 91%"                                   # shortcut per horn: light beep loud long blast
//	honk-me send --title "Backup finished" --message "42 s" --severity beep
//	honk-me problem --group-key db/backup --title "Backup failed" --message "exit 1"
//	honk-me recovery --group-key db/backup --title "Backup OK" --message "fixed"
//
// HONK_URL and HONK_KEY come from the environment. Run `honk-me help` for flags and exit codes.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	honk "github.com/honk-me/honk-go"
)

// Exit codes (documented in README.md and `honk-me help`).
const (
	exitOK         = 0 // accepted (also a duplicate replay)
	exitError      = 1 // unexpected answer (404 wrong URL, redirect, malformed reply)
	exitUsage      = 2 // bad flags, or HONK_URL / HONK_KEY missing or malformed
	exitInvalid    = 3 // the message is invalid (local check, or 400/413/415/422)
	exitAuth       = 4 // 401/403: bad or revoked key, urgent not allowed, suspended
	exitQuota      = 5 // 429 after retries: daily quota or rate limit
	exitConflict   = 6 // 409: idempotency key reused with a different payload
	exitTemporary  = 7 // network error, timeout or 5xx after retries: safe to rerun with the same key
	usageExitCodes = `Exit codes:
  0  accepted (or a duplicate of an accepted event)
  1  unexpected answer (wrong HONK_URL, redirect, malformed reply)
  2  usage error, or HONK_URL / HONK_KEY missing
  3  invalid message (fix the flags)
  4  authentication: invalid/revoked key, urgent not allowed, suspended project
  5  quota or rate limit (429); stderr shows Retry-After
  6  idempotency conflict (409): same key, different payload
  7  temporary failure (network, timeout, 5xx) after retries; rerun with the same --idempotency-key`
)

var stdout io.Writer = os.Stdout
var stderr io.Writer = os.Stderr
var stdin io.Reader = os.Stdin

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Getenv)
	stop()
	os.Exit(code)
}

func usage() string {
	return `honk-me ` + honk.Version + ` — send an event to Honk

Usage:
  honk-me light|beep|loud|long|blast [TITLE] MESSAGE [flags]   shortcut with that severity
  honk-me send     --message TEXT [flags]
  honk-me problem  --group-key KEY --message TEXT [flags]   (event_type problem, severity long)
  honk-me recovery --group-key KEY --message TEXT [flags]   (event_type recovery, severity beep)
  honk-me version

The Honk scale (--severity): light (info), beep (success), loud (warning), long (error),
blast (critical). Canonical names work too.

Environment:
  HONK_URL               base URL of your Honk server (required)
  HONK_KEY               project ingestion key honk_… (required; never pass it as a flag)
  HONK_SOURCE, HONK_ENVIRONMENT, HONK_CHANNEL   defaults for --source, --environment, --channel
  HONK_IDEMPOTENCY_KEY   default for --idempotency-key

Run "honk-me send -h" for the message flags.

` + usageExitCodes + "\n"
}

func run(ctx context.Context, args []string, getenv func(string) string) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage())
		return exitUsage
	}
	cmd, args := args[0], args[1:]
	var eventType honk.EventType
	var severity honk.Severity
	shortcut := false
	switch cmd {
	case "send":
	case "problem":
		eventType, severity = honk.EventTypeProblem, honk.Long
	case "recovery":
		eventType, severity = honk.EventTypeRecovery, honk.Beep
	case "light", "beep", "loud", "long", "blast":
		severity, shortcut = honk.SeverityAliases[cmd], true
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, "honk-me", honk.Version)
		return exitOK
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage())
		return exitOK
	default:
		fmt.Fprintf(stderr, "honk-me: unknown command %q\n\n%s", cmd, usage())
		return exitUsage
	}

	fs := flag.NewFlagSet("honk-me "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		m          honk.Message
		meta       = metaFlag{}
		occurredAt string
		seq        int64 = -1
		key        string
		timeout    time.Duration
		dead       time.Duration
		retries    int
		asJSON     bool
		quiet      bool
		dryRun     bool
	)
	fs.StringVar(&m.Title, "title", "", "one-line title, ≤ 160 characters (default: first line of the message)")
	fs.StringVar(&m.Message, "message", "", `message text, ≤ 8192 bytes; "-" reads stdin (e.g. "tail -c 8000 log | honk-me send --message -")`)
	if shortcut {
		m.Severity = severity
	} else {
		fs.StringVar((*string)(&m.Severity), "severity", string(severity), "light (info), beep (success), loud (warning), long (error), blast (critical)")
	}
	fs.StringVar((*string)(&m.Priority), "priority", "", "low, normal, high, urgent (urgent needs a key with allow_urgent)")
	fs.StringVar((*string)(&m.Category), "category", "", "infrastructure, security, backups, deployments, payments, customers, sales, automation, personal, other")
	fs.StringVar(&m.Source, "source", getenv("HONK_SOURCE"), "source, ≤ 64 characters (e.g. $(hostname) for cron); default $HONK_SOURCE, else the server's \"api\"")
	fs.StringVar(&m.Environment, "environment", getenv("HONK_ENVIRONMENT"), "environment, ≤ 32 characters; default $HONK_ENVIRONMENT")
	fs.StringVar(&m.Channel, "channel", getenv("HONK_CHANNEL"), "channel, ≤ 64 characters; default $HONK_CHANNEL")
	fs.StringVar(&m.GroupKey, "group-key", "", "group key, ≤ 128 characters (required for problem/recovery)")
	if cmd == "send" {
		fs.StringVar((*string)(&m.EventType), "event-type", "", "event, problem, recovery")
	}
	fs.StringVar(&occurredAt, "occurred-at", "", `when it happened: RFC 3339 timestamp or "now"`)
	fs.StringVar(&m.URL, "url", "", "https link shown as \"Open link\"")
	fs.StringVar(&m.ImageURL, "image-url", "", "https image the server fetches and attaches")
	fs.Var(meta, "meta", "metadata key=value (string) or key:=value (JSON number/boolean); repeatable")
	fs.IntVar(&m.TTLSeconds, "ttl", 0, "push lifetime in seconds, 60–86400 (default 3600)")
	fs.Int64Var(&seq, "source-sequence", -1, "monotonic counter for problem/recovery ordering (needs --group-key)")
	fs.StringVar(&key, "idempotency-key", getenv("HONK_IDEMPOTENCY_KEY"), "stable key for this event, reused on retries; default a new UUIDv7")
	fs.DurationVar(&timeout, "timeout", 5*time.Second, "timeout of one attempt")
	fs.DurationVar(&dead, "deadline", 30*time.Second, "total time budget, retries included")
	fs.IntVar(&retries, "retries", 4, "retries after the first attempt (network errors, 429, 5xx)")
	fs.BoolVar(&asJSON, "json", false, `print {"id","duplicate","received_at"} instead of the id`)
	fs.BoolVar(&quiet, "quiet", false, "print nothing on success")
	fs.BoolVar(&dryRun, "dry-run", false, "validate and print the JSON body without sending")
	fs.Usage = func() {
		if shortcut {
			fmt.Fprintf(stderr, "Usage: honk-me %s [TITLE] MESSAGE [flags]\n\nFlags:\n", cmd)
		} else {
			fmt.Fprintf(stderr, "Usage: honk-me %s [flags]\n\nFlags:\n", cmd)
		}
		fs.PrintDefaults()
		fmt.Fprintf(stderr, "\n%s\n", usageExitCodes)
	}
	// Flags may come before or after the positional arguments.
	var positional []string
	for rest := args; ; {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return exitOK
			}
			return exitUsage
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	switch {
	case len(positional) > 0 && !shortcut:
		fmt.Fprintf(stderr, "honk-me: unexpected argument %q (use --message)\n", positional[0])
		return exitUsage
	case len(positional) > 2:
		fmt.Fprintf(stderr, "honk-me: %s takes [TITLE] MESSAGE (quote them), got %d arguments\n", cmd, len(positional))
		return exitUsage
	case len(positional) == 2:
		m.Title, m.Message = positional[0], positional[1]
	case len(positional) == 1:
		m.Message = positional[0]
	}
	if eventType != "" {
		m.EventType = eventType
		if m.GroupKey == "" {
			fmt.Fprintf(stderr, "honk-me: %s needs --group-key\n", cmd)
			return exitUsage
		}
	}
	if m.Message == "-" {
		b, err := io.ReadAll(bufio.NewReader(io.LimitReader(stdin, 1<<20)))
		if err != nil {
			fmt.Fprintln(stderr, "honk-me: reading stdin:", err)
			return exitUsage
		}
		m.Message = strings.TrimRight(string(b), "\n")
	}
	if m.Message == "" {
		fmt.Fprintln(stderr, "honk-me: --message is required")
		return exitUsage
	}
	switch occurredAt {
	case "":
	case "now":
		m.OccurredAt = time.Now()
	default:
		t, err := time.Parse(time.RFC3339Nano, occurredAt)
		if err != nil {
			fmt.Fprintf(stderr, "honk-me: --occurred-at must be RFC 3339 (e.g. 2026-10-02T21:10:00Z) or \"now\"\n")
			return exitUsage
		}
		m.OccurredAt = t
	}
	if seq >= 0 {
		m.SourceSequence = &seq
	}
	if len(meta) > 0 {
		m.Metadata = meta
	}
	if retries == 0 {
		retries = honk.NoRetries
	}

	if dryRun {
		return printDryRun(m)
	}
	c, err := honk.New(honk.Options{
		URL: getenv("HONK_URL"), Key: getenv("HONK_KEY"), Timeout: timeout, Deadline: dead, Retries: retries,
		UserAgent: "honk-me-cli",
	})
	if err != nil {
		fmt.Fprintln(stderr, "honk-me:", strings.TrimPrefix(err.Error(), "honk: "))
		return exitUsage
	}
	var opts []honk.Option
	if key != "" {
		opts = append(opts, honk.WithIdempotencyKey(key))
	}
	acc, err := c.Send(ctx, m, opts...)
	if err != nil {
		return report(err)
	}
	switch {
	case quiet:
	case asJSON:
		_ = json.NewEncoder(stdout).Encode(map[string]any{"id": acc.ID, "duplicate": acc.Duplicate, "received_at": acc.ReceivedAt.UTC().Format(time.RFC3339Nano)})
	default:
		fmt.Fprintln(stdout, acc.ID)
	}
	return exitOK
}

func printDryRun(m honk.Message) int {
	body, err := honk.EncodeMessage(m, honk.Defaults{})
	if err != nil {
		return report(err)
	}
	var out bytes.Buffer
	_ = json.Indent(&out, body, "", "  ")
	fmt.Fprintln(stdout, out.String())
	return exitOK
}

func report(err error) int {
	fmt.Fprintln(stderr, "honk-me:", strings.TrimPrefix(err.Error(), "honk: "))
	var he *honk.Error
	if !errors.As(err, &he) {
		return exitError
	}
	if he.RetryAfter > 0 {
		fmt.Fprintf(stderr, "honk-me: Retry-After %s\n", he.RetryAfter)
	}
	if he.Retryable() && he.IdempotencyKey != "" {
		fmt.Fprintf(stderr, "honk-me: retry later with --idempotency-key %s (no duplicate will be created)\n", he.IdempotencyKey)
	}
	switch he.Kind {
	case honk.KindValidation:
		return exitInvalid
	case honk.KindAuth:
		return exitAuth
	case honk.KindQuota:
		return exitQuota
	case honk.KindConflict:
		return exitConflict
	case honk.KindNetwork, honk.KindTimeout, honk.KindServer, honk.KindCanceled:
		return exitTemporary
	}
	return exitError
}

// metaFlag collects --meta key=value (string) and key:=value (JSON scalar).
type metaFlag map[string]any

func (m metaFlag) String() string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

func (m metaFlag) Set(s string) error {
	if k, raw, ok := strings.Cut(s, ":="); ok && !strings.Contains(k, "=") {
		switch raw {
		case "true", "false":
			m[k] = raw == "true"
			return nil
		}
		var f float64
		if json.Unmarshal([]byte(raw), &f) != nil {
			return fmt.Errorf("%q: after := use a JSON number or true/false", s)
		}
		m[k] = json.Number(raw)
		return nil
	}
	k, v, ok := strings.Cut(s, "=")
	if !ok || k == "" {
		return fmt.Errorf("%q: use key=value or key:=number", s)
	}
	m[k] = v
	return nil
}
