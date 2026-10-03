package honk

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Kind classifies an *Error.
type Kind string

const (
	// KindValidation: the message is invalid (rejected locally, or 400/413/415/422). Fix it.
	KindValidation Kind = "validation"
	// KindAuth: 401/403 — invalid or revoked key, priority_not_allowed (urgent without
	// allow_urgent), project_suspended, workspace_suspended.
	KindAuth Kind = "auth"
	// KindQuota: 429 quota_exceeded (daily messages_per_day, until UTC midnight) or
	// rate_limited, after retries. See RetryAfter.
	KindQuota Kind = "quota"
	// KindConflict: 409 idempotency_conflict — the key was already used with a different
	// payload in the last 24 hours.
	KindConflict Kind = "conflict"
	// KindNetwork: Honk could not be reached on any attempt before the deadline.
	KindNetwork Kind = "network"
	// KindTimeout: attempts timed out (or the context deadline passed). The event may or may
	// not have been stored; retrying with the same idempotency key is safe.
	KindTimeout Kind = "timeout"
	// KindCanceled: the context was canceled.
	KindCanceled Kind = "canceled"
	// KindServer: 5xx on every attempt before the deadline.
	KindServer Kind = "server"
	// KindHTTP: any other unexpected answer (404 wrong URL, a redirect, a malformed 202).
	KindHTTP Kind = "http"
)

// Sentinels for errors.Is: errors.Is(err, honk.ErrQuota).
var (
	ErrValidation = errors.New("honk: invalid message")
	ErrAuth       = errors.New("honk: authentication failed")
	ErrQuota      = errors.New("honk: quota or rate limit exceeded")
	ErrConflict   = errors.New("honk: idempotency conflict")
	ErrNetwork    = errors.New("honk: network error")
	ErrTimeout    = errors.New("honk: timeout")
	ErrServer     = errors.New("honk: server error")
)

// FieldError is one invalid field, from the server (error.fields[]) or local validation.
type FieldError struct {
	// Field is the wire name: group_key, metadata.region, Idempotency-Key, body, …
	Field string `json:"field"`
	// Code: required, too_long, too_short, invalid_enum, invalid_format, out_of_range,
	// not_allowed, invalid_utf8, requires_group_key.
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// Error is returned by Send and the helpers. Use errors.As to read the details, or
// errors.Is with the Err* sentinels.
type Error struct {
	Kind Kind
	// Status is the HTTP status (0 when no answer was received).
	Status int
	// Code is the API error code (invalid_key, quota_exceeded, …), or network_error/timeout.
	Code    string
	Message string
	// Fields lists invalid fields for KindValidation.
	Fields []FieldError
	// Local is true when the SDK rejected the message before sending anything.
	Local     bool
	RequestID string
	// IdempotencyKey that was used. Retry later with the same key to stay duplicate-free.
	IdempotencyKey string
	// Attempts is the number of HTTP attempts made.
	Attempts int
	// RetryAfter is the server's Retry-After, when present.
	RetryAfter time.Duration
	// Err is the underlying transport or context error, if any.
	Err error
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("honk: ")
	if e.Status != 0 {
		fmt.Fprintf(&b, "%d ", e.Status)
	}
	if e.Code != "" && e.Status != 0 {
		b.WriteString(e.Code + ": ")
	}
	b.WriteString(e.Message)
	if len(e.Fields) > 0 {
		parts := make([]string, len(e.Fields))
		for i, f := range e.Fields {
			msg := f.Message
			if msg == "" {
				msg = f.Code
			}
			parts[i] = f.Field + " " + msg
		}
		b.WriteString(" (" + strings.Join(parts, "; ") + ")")
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Err }

// Is matches the Err* sentinels by Kind (ErrNetwork also matches timeouts).
func (e *Error) Is(target error) bool {
	switch target {
	case ErrValidation:
		return e.Kind == KindValidation
	case ErrAuth:
		return e.Kind == KindAuth
	case ErrQuota:
		return e.Kind == KindQuota
	case ErrConflict:
		return e.Kind == KindConflict
	case ErrNetwork:
		return e.Kind == KindNetwork || e.Kind == KindTimeout
	case ErrTimeout:
		return e.Kind == KindTimeout
	case ErrServer:
		return e.Kind == KindServer
	}
	return false
}

// Retryable reports whether sending the same event again later (with the same idempotency
// key) may succeed.
func (e *Error) Retryable() bool {
	switch e.Kind {
	case KindNetwork, KindTimeout, KindServer, KindQuota:
		return true
	}
	return false
}

func localValidation(fields []FieldError) *Error {
	return &Error{Kind: KindValidation, Code: "validation_failed", Local: true, Fields: fields, Message: "invalid message"}
}
