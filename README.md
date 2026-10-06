# honk-go

[![CI](https://github.com/honk-me/honk-go/actions/workflows/ci.yml/badge.svg)](https://github.com/honk-me/honk-go/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/honk-me/honk-go.svg)](https://pkg.go.dev/github.com/honk-me/honk-go)

Official Go client and CLI for [Honk](https://honk-me.app), the inbox that turns events from
your apps, scripts, cron jobs and CI into calm, grouped push notifications on your phone.

- Package `honk`: context-aware, standard library only, safe for concurrent use.
- CLI `honk-me` for shell scripts, cron and CI (replaces the cURL snippet).
- Retries with backoff, `Retry-After`, a total deadline and an idempotency key on every send,
  so a retry never creates a duplicate.

The ingestion key (`honk_…`) is a secret: keep it in the environment or a secret store, never
in source code or client apps.

## Install

```sh
go get github.com/honk-me/honk-go                          # library (Go 1.22+)
go install github.com/honk-me/honk-go/cmd/honk-me@latest   # CLI
```

The CLI is also attached as prebuilt binaries (Linux, macOS, Windows; amd64 and arm64) to
every [GitHub release](https://github.com/honk-me/honk-go/releases).

Create a project and an ingestion key at [honk-me.app](https://honk-me.app). Its
*Integrations* page generates ready-to-paste code for the library and the CLI.

## Quick start

```go
import honk "github.com/honk-me/honk-go" // package honk

c, err := honk.FromEnv() // HONK_URL, HONK_KEY (+ optional HONK_SOURCE, HONK_ENVIRONMENT, HONK_CHANNEL)
if err != nil {
	log.Fatal(err)
}
_, err = c.Beep(ctx, "Backup finished", "nightly pg_dump took 42 s")
```

Or explicitly: `honk.New(honk.Options{URL: "https://honk.example.com", Key: key})`.

## The Honk scale

Every severity has a horn name. Use either; the SDK always sends the canonical value.

| Horn | Severity | Method | Constant | CLI |
|---|---|---|---|---|
| light honk | `light` (info) | `c.Light(ctx, title, message, opts...)` | `honk.Light` | `honk-me light …` |
| beep-beep | `beep` (success) | `c.Beep(…)` | `honk.Beep` | `honk-me beep …` |
| loud honk | `loud` (warning) | `c.Loud(…)` | `honk.Loud` | `honk-me loud …` |
| long honk | `long` (error) | `c.Long(…)` | `honk.Long` | `honk-me long …` |
| blast | `blast` (critical) | `c.Blast(…)` | `honk.Blast` | `honk-me blast …` |

The horn constants are aliases (`honk.Loud == honk.SeverityWarning`), and `Severity: "LOUD"`
is normalized too, so alias and canonical spellings are the same event, also for idempotency.
`Long` and `Blast` push at least as high priority. `Info`, `Success`, `Warning`, `Error` and
`Critical` remain as synonyms; `honk.ParseSeverity("Blast")` returns `SeverityCritical`.

## Recipe: notify me when a customer asks for something

One group per request (`requests/<id>`) and a stable idempotency key: two different customers
never fold into one notification, and a retried handler or job never buzzes twice.

```go
var notifier, _ = honk.FromEnv() // create once, reuse (keep-alive)

func onCustomerRequest(r CustomerRequest) {
	// ...save the request first, then notify off the request path:
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := notifier.Send(ctx, honk.Message{
			Title:    truncate("New request: "+r.Subject, 150),
			Message:  truncate(fmt.Sprintf("%s (%s) asked: %s", r.Name, r.Company, r.Body), 2000),
			Priority: honk.PriorityHigh, // push right away
			Category: honk.CategoryCustomers,
			Channel:  "requests",
			GroupKey: fmt.Sprintf("requests/%d", r.ID), // one group per request
			URL:      fmt.Sprintf("https://shop.example.com/admin/requests/%d", r.ID),
			Metadata: map[string]any{"request_id": strconv.Itoa(r.ID)},
		}, honk.WithIdempotencyKey(fmt.Sprintf("request-%d", r.ID)))
		if err != nil {
			log.Printf("honk: %v", err) // never fail the customer's request
		}
	}()
}

func truncate(s string, n int) string { // by runes, never splits UTF-8
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
```

From a queue worker, return the error when `he.Retryable()` and let the queue retry with the
same key.

## Grouping in three lines

Messages with the same `GroupKey` (per project, environment, source and channel) form one
group: the first one pushes, repeats update it calmly instead of buzzing again. Use one key
per customer request (`requests/<id>`), and a shared key only for repeats of the same problem
(`queue/failed-jobs`). `Problem`/`Recovery` pairs need a `GroupKey`.

## Sending

```go
func (c *Client) Send(ctx context.Context, m honk.Message, opts ...honk.Option) (*honk.Accepted, error)
// Accepted{ID, Duplicate, ReceivedAt}
```

| Field | Notes |
|---|---|
| `Message` | **required**, 1–8192 bytes UTF-8, line breaks allowed |
| `Title` | ≤ 160 chars, one line; default: first line of `Message` |
| `Severity` | `honk.Light` `Beep` `Loud` `Long` `Blast` (or `SeverityInfo` … `SeverityCritical`, or any-case strings); `Long`/`Blast` push at least as high |
| `Priority` | `PriorityLow` `PriorityNormal` `PriorityHigh` `PriorityUrgent` (urgent needs a key with *allow urgent*) |
| `Category` | `CategoryInfrastructure`, `CategorySecurity`, `CategoryBackups`, `CategoryDeployments`, `CategoryPayments`, `CategoryCustomers`, `CategorySales`, `CategoryAutomation`, `CategoryPersonal`, `CategoryOther` |
| `Source` / `Environment` / `Channel` | ≤ 64 / 32 / 64 chars; default `api` / `default` / `general` or `Options.Defaults` |
| `GroupKey` | ≤ 128 chars |
| `EventType` | `EventTypeEvent` `EventTypeProblem` `EventTypeRecovery` (recovery needs `GroupKey`) |
| `OccurredAt` | `time.Time`, sent as UTC RFC 3339 with milliseconds |
| `URL` / `ImageURL` | `https://` only, no credentials (`ImageURL`: no `#fragment`; fetched by the server afterwards) |
| `Actions` | `[]honk.Action{{Title, URL}}`, up to 3 buttons, the first is the primary; see below |
| `Metadata` | `map[string]any`, ≤ 16 keys `[A-Za-z0-9_.-]{1,64}`; string (≤ 512 chars), number or bool values |
| `TTLSeconds` | push lifetime 60–86400 (0 = default 3600) |
| `SourceSequence` | `*int64` (`honk.Ptr[int64](n)`), 0 … 2^53-1, needs `GroupKey` |

Zero values are omitted. A nil error means Honk **durably stored** the message (`202`), not
that a push was delivered or read. `honk.EncodeMessage(m, defaults)` returns the exact JSON.

Helpers take the core fields plus options (`WithIdempotencyKey`, `WithSeverity`,
`WithPriority`, `WithCategory`, `WithSource`, `WithEnvironment`, `WithChannel`,
`WithGroupKey`, `WithOccurredAt`, `WithURL`, `WithImageURL`, `WithActions`, `WithMetadata`,
`WithTTLSeconds`, `WithSourceSequence`):

```go
c.Loud(ctx, "Disk 91%", "/var on app-01", honk.WithGroupKey("disk/app-01/var"))
c.Light(ctx, "Deploy started", "v4.2.0", honk.WithChannel("deploys"))
c.Beep(...); c.Long(...); c.Blast(...)
c.Problem(ctx, "db/backup", "Backup failed", "pg_dump exited with 1")      // a long honk by default
c.Recovery(ctx, "db/backup", "Backup OK", "pg_dump finished in 41 s")      // a beep by default
```

### Buttons (actions)

Up to three buttons on the message, in display order: reply to the customer, call them, open
the order.

```go
_, err := c.Send(ctx, honk.Message{
	Title:    "New request: online shop quote",
	Message:  "Emily Carter (Acme) asked for a quote: online shop, 40 products",
	Category: honk.CategoryCustomers,
	GroupKey: "requests/4812",
	Actions: []honk.Action{
		{Title: "Reply", URL: "mailto:emily@example.com?subject=" + url.PathEscape("Your quote")},
		{Title: "Call Emily", URL: "tel:+15550134"},
	},
})
```

- `Title`: 1–40 characters, one line, shown as sent.
- `URL`, at most 2048 bytes without spaces: `https://` (no credentials); `mailto:` with one
  address and optionally `?subject=…&body=…` (percent-encoded with `url.PathEscape`, no other
  keys); `tel:` with a number (digits, `-` `.` `(` `)`, `+` only first); `sms:` with a number
  and optionally `?body=…`. Other schemes are refused.
- Honk never opens or fetches them; the app does when you tap one. They appear on the
  message in the app and the web inbox, and on iPhone notifications that show the message
  text. Errors name the button: `actions[1].url`.
- Helpers take `honk.WithActions(honk.Action{…}, …)`; the CLI takes
  `--action "Call Emily=tel:+15550134"`.

### Options

```go
honk.New(honk.Options{
	URL:      "https://honk.example.com",
	Key:      key,
	Timeout:  5 * time.Second,  // per attempt
	Retries:  4,                // after the first attempt; honk.NoRetries disables
	Deadline: 30 * time.Second, // total, waits included; a shorter ctx deadline wins
	Defaults: honk.Defaults{Source: "billing", Environment: "production"},
	SkipValidation: false,      // local checks (the server always validates)
	HTTPClient: myClient,       // proxies, tracing; redirects are never followed
})
```

## Retries and idempotency, guaranteed

- Every send carries an `Idempotency-Key`: yours (`WithIdempotencyKey`), or a fresh UUIDv7
  (`honk.NewIdempotencyKey()`). **The same key is reused on every retry.** Within 24 h Honk
  answers a replay with the original ID and `Duplicate: true`, so a lost response never creates
  a second message.
- Only network errors, timeouts, `429` and `5xx` are retried, with exponential backoff and
  full jitter (`rand(0, min(8 s, 0.5 s·2ⁿ))`), never sooner than the server's `Retry-After`.
- Everything stops at the deadline (or the context's): if the next wait would cross it (for
  example a daily quota that resets at midnight), the error is returned at once with
  `RetryAfter`.
- `4xx` other than `429` are never retried: fix the request instead.
- Short per-attempt timeouts and a keep-alive transport: create one `Client` and share it.

## Errors

Errors are `*honk.Error` (`Kind`, `Status`, `Code`, `Fields`, `Local`, `RequestID`,
`IdempotencyKey`, `Attempts`, `RetryAfter`, `Retryable()`), and match sentinels with
`errors.Is`:

| Sentinel / Kind | When | What to do |
|---|---|---|
| `ErrValidation` / `KindValidation` | rejected locally (`Local`) or `400`/`413`/`415`/`422`; `Fields` lists every problem | fix the message |
| `ErrAuth` / `KindAuth` | `401 invalid_key`, `403 priority_not_allowed`, `project_suspended`, `workspace_suspended` | fix the key or the priority |
| `ErrQuota` / `KindQuota` | `429 quota_exceeded` (daily, resets at UTC midnight) or `rate_limited`, after retries | retry after `RetryAfter` |
| `ErrConflict` / `KindConflict` | `409 idempotency_conflict`: same key, different payload | new key or original payload |
| `ErrNetwork` / `KindNetwork` | unreachable on every attempt | retry later, same key |
| `ErrTimeout` / `KindTimeout` | attempts (or the context) timed out; maybe stored | retry later, same key |
| `ErrServer` / `KindServer` | `5xx` on every attempt | retry later, same key |

`KindCanceled` wraps `context.Canceled`; timeouts also match `context.DeadlineExceeded` when
the context expired.

```go
_, err := c.Long(ctx, "Payment failed", "Stripe declined order 1042", honk.WithGroupKey("payments/stripe"))
var he *honk.Error
switch {
case err == nil:
case errors.Is(err, honk.ErrValidation):
	log.Printf("bug: %v", err) // he.Fields says what to fix
case errors.As(err, &he) && he.Retryable():
	requeue(he.IdempotencyKey, he.RetryAfter)
default:
	log.Print(err)
}
```

## CLI: `honk-me`

For shell scripts, cron and CI. Reads `HONK_URL` and `HONK_KEY` (never pass the key as a
flag), plus optional `HONK_SOURCE`, `HONK_ENVIRONMENT`, `HONK_CHANNEL` and
`HONK_IDEMPOTENCY_KEY`.

```sh
honk-me loud "Disk 91%"                                   # shortcut: light, beep, loud, long, blast
honk-me beep "Backup finished" "nightly pg_dump took 42 s" # [TITLE] MESSAGE, flags anywhere
honk-me send --title "Disk almost full" --message "/var at 91%" --severity loud \
  --group-key "disk/$(hostname)/var" --source "$(hostname)" --meta host="$(hostname)" --meta used:=91
honk-me problem  --group-key db/backup --title "Backup failed" --message "pg_dump exited with 1"
honk-me recovery --group-key db/backup --title "Backup OK"     --message "pg_dump finished"
tail -c 8000 /var/log/backup.log | honk-me send --title "Backup log" --message -   # message from stdin
honk-me send --message "Front door" --image-url https://cam.example.com/snap.jpg --priority high
honk-me loud "Disk 91%" "/var on app-01" --action "Open Grafana=https://grafana.example.com/d/disk"
```

Cron, alerting only when the job fails:

```cron
0 3 * * * pg_dump app > /backup/app.sql || honk-me problem --group-key db/backup --source "$(hostname)" --title "Backup failed" --message "pg_dump exited with $?"
```

Send `recovery` only when something was actually broken (for example from a check that
remembers its last state): a recovery with no open problem still opens a "recovered" episode
and notifies, so a recovery after every successful run would buzz every night.

CI (GitHub Actions), with one key per run attempt so the CLI's own retries never duplicate:

```yaml
- name: Notify
  if: failure()
  env:
    HONK_URL: ${{ secrets.HONK_URL }}
    HONK_KEY: ${{ secrets.HONK_KEY }}
  run: |
    honk-me problem --group-key "ci/${{ github.repository }}/${{ github.ref_name }}" \
      --title "CI failed: ${{ github.workflow }}" --message "${{ github.server_url }}/${{ github.repository }}/actions/runs/${{ github.run_id }}" \
      --source github-actions --category deployments \
      --idempotency-key "gh-${{ github.run_id }}-${{ github.run_attempt }}" || true
```

Shortcuts `honk-me light|beep|loud|long|blast [TITLE] MESSAGE [flags]` fix the severity and
take the text as arguments. `send`, `problem` and `recovery` take flags only:
`--title`, `--message` (`-` = stdin), `--severity` (horn or canonical name), `--priority`, `--category`,
`--source`, `--environment`, `--channel`, `--group-key`, `--event-type` (send only),
`--occurred-at` (RFC 3339 or `now`), `--url`, `--image-url`, `--action TITLE=URL` (repeatable,
up to 3; split at the first `=`), `--meta k=v` / `--meta k:=3` (repeatable), `--ttl`,
`--source-sequence`, `--idempotency-key`, `--timeout`, `--deadline`, `--retries`, `--json`,
`--quiet`, `--dry-run`. `honk-me send -h` lists them all.

On success it prints the message ID (or JSON with `--json`). Exit codes:

| Code | Meaning |
|---|---|
| 0 | accepted (or a duplicate of an accepted event) |
| 1 | unexpected answer (wrong `HONK_URL`, redirect, malformed reply) |
| 2 | usage error, or `HONK_URL` / `HONK_KEY` missing |
| 3 | invalid message: fix the flags |
| 4 | authentication: invalid/revoked key, urgent not allowed, suspended project |
| 5 | quota or rate limit (`429`); stderr shows `Retry-After` |
| 6 | idempotency conflict (`409`): same key, different payload |
| 7 | temporary failure (network, timeout, `5xx`) after retries: rerun with the same `--idempotency-key` |

Add `|| true` where a notification failure must not fail the script.

## Development

```sh
go test -race ./...                                   # unit tests (httptest) + CLI tests
HONK_URL=… HONK_KEY=… go test -run Integration ./...  # against a real server (use a test project's key)
```

The version lives in `honk.Version` (also the User-Agent and `honk-me version`). Releases: push
a tag `vX.Y.Z` matching it; the release workflow attaches the CLI binaries and the Go proxy
serves the module (see `CHANGELOG.md`).

## Links

- [honk-me.app](https://honk-me.app): the Honk inbox (web, iPhone).
- Other SDKs: [Node.js](https://github.com/honk-me/honk-node),
  [PHP / Laravel](https://github.com/honk-me/honk-php), [Swift](https://github.com/honk-me/honk-swift),
  [Kotlin / Java](https://github.com/honk-me/honk-kotlin).

MIT License.
