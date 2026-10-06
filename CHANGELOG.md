# Changelog

All notable changes to the Go module and the `honk-me` CLI are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/).

## [0.2.0] - 2026-10-07

### Added
- `Message.Actions`: up to 3 buttons (`honk.Action{Title, URL}`) with an `https://`,
  `mailto:`, `tel:` or `sms:` URL, and `WithActions` for the helpers. Validated locally like
  the server does, with errors named `actions[1].url`. Empty `Actions` are omitted, so
  messages without buttons are sent exactly as before.
- CLI: `--action TITLE=URL` (repeatable), e.g. `--action "Call Emily=tel:+15550134"`.

## [0.1.0] - 2026-10-04

### Added
- Package `honk`: context-aware `Client` for `POST /v1/messages` with every field of the v1
  ingestion API (including `ImageURL`), no dependencies outside the standard library.
- Automatic UUIDv7 `Idempotency-Key` (or `WithIdempotencyKey`), reused on every retry.
- Retries for network errors, timeouts, 429 and 5xx with exponential backoff, full jitter,
  `Retry-After` and a total deadline (a shorter context deadline wins).
- `*honk.Error` with `Kind`, plus `ErrValidation`, `ErrAuth`, `ErrQuota`, `ErrConflict`,
  `ErrNetwork`, `ErrTimeout`, `ErrServer` for `errors.Is`.
- Local validation of limits, enums and https-only URLs; `EncodeMessage` for the exact body.
- Helpers `Problem`, `Recovery`, `Info`, `Success`, `Warning`, `Error`, `Critical` with
  functional options; `FromEnv` / `OptionsFromEnv`.
- `honk-me` CLI (`send`, `problem`, `recovery`) for shell scripts, cron and CI, with
  documented exit codes, `--idempotency-key`, `--meta`, stdin messages and `--dry-run`.
- The Honk scale: constants `Light`, `Beep`, `Loud`, `Long`, `Blast` (aliases of the
  canonical severities), `ParseSeverity`, case-insensitive normalization before sending, the
  `Light`…`Blast` helpers, and CLI shortcuts `honk-me light|beep|loud|long|blast [TITLE] MESSAGE`.
