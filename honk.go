package honk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Version of this SDK, sent in the User-Agent header.
const Version = "0.1.0"

const (
	defaultTimeout     = 5 * time.Second
	defaultRetries     = 4
	defaultDeadline    = 30 * time.Second
	defaultBackoffBase = 500 * time.Millisecond
	defaultBackoffMax  = 8 * time.Second
)

// NoRetries disables retries when used as Options.Retries.
const NoRetries = -1

// Options configure a Client. URL and Key are required.
type Options struct {
	// URL is the base address of your Honk server, e.g. https://honk.example.com.
	URL string
	// Key is a project ingestion key (honk_…). Keep it server-side.
	Key string
	// Timeout of one HTTP attempt. Default 5s.
	Timeout time.Duration
	// Retries after the first attempt (network errors, 429 and 5xx only). 0 means the
	// default (4); use NoRetries to disable.
	Retries int
	// Deadline is the total time budget of one Send, waits included. Default 30s. A shorter
	// context deadline wins.
	Deadline time.Duration
	// Defaults fill Source/Environment/Channel when a message leaves them empty.
	Defaults Defaults
	// SkipValidation sends messages without local checks (the server always validates).
	SkipValidation bool
	// Backoff between retries: attempt n waits rand(0, min(Max, Base·2ⁿ)) (full jitter), or
	// the server's Retry-After when longer. Default 500ms / 8s.
	Backoff Backoff
	// HTTPClient to use (proxies, instrumentation). Its CheckRedirect is overridden so
	// redirects are reported instead of followed. Default: a keep-alive client.
	HTTPClient *http.Client
	// UserAgent is appended to the User-Agent header.
	UserAgent string
}

// Backoff configures the delay between retries.
type Backoff struct {
	Base time.Duration
	Max  time.Duration
}

// Client sends messages to one Honk project. It is safe for concurrent use; create one and
// reuse it so connections are kept alive.
type Client struct {
	url       string
	key       string
	timeout   time.Duration
	retries   int
	deadline  time.Duration
	defaults  Defaults
	skipCheck bool
	backoff   Backoff
	http      *http.Client
	userAgent string
}

// New validates the options and returns a Client.
func New(o Options) (*Client, error) {
	u := strings.TrimRight(strings.TrimSpace(o.URL), "/")
	u = strings.TrimSuffix(u, "/v1/messages")
	if u == "" {
		return nil, errors.New("honk: URL is required (the base address of your Honk server, e.g. https://honk.example.com; is HONK_URL set?)")
	}
	if !strings.HasPrefix(strings.ToLower(u), "https://") && !strings.HasPrefix(strings.ToLower(u), "http://") {
		return nil, fmt.Errorf("honk: URL must start with https:// (got %q)", o.URL)
	}
	key := strings.TrimSpace(o.Key)
	if key == "" {
		return nil, errors.New("honk: Key is required (a project ingestion key honk_…; is HONK_KEY set?)")
	}
	if !strings.HasPrefix(key, "honk_") || strings.ContainsFunc(key, func(r rune) bool { return r < 0x21 || r > 0x7e }) {
		return nil, errors.New("honk: Key must be a project ingestion key starting with honk_ (create one under Project → Keys)")
	}
	c := &Client{
		url: u, key: key, timeout: o.Timeout, retries: o.Retries, deadline: o.Deadline,
		defaults: o.Defaults, skipCheck: o.SkipValidation, backoff: o.Backoff,
		userAgent: "honk-go/" + Version,
	}
	if o.UserAgent != "" {
		c.userAgent += " " + o.UserAgent
	}
	if c.timeout <= 0 {
		c.timeout = defaultTimeout
	}
	if c.deadline <= 0 {
		c.deadline = defaultDeadline
	}
	switch {
	case c.retries == 0:
		c.retries = defaultRetries
	case c.retries < 0:
		c.retries = 0
	}
	if c.backoff.Base <= 0 {
		c.backoff.Base = defaultBackoffBase
	}
	if c.backoff.Max <= 0 {
		c.backoff.Max = defaultBackoffMax
	}
	if o.HTTPClient != nil {
		hc := *o.HTTPClient
		c.http = &hc
	} else {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.MaxIdleConnsPerHost = 8
		c.http = &http.Client{Transport: t}
	}
	c.http.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c, nil
}

// OptionsFromEnv reads HONK_URL, HONK_KEY and the optional HONK_SOURCE, HONK_ENVIRONMENT and
// HONK_CHANNEL defaults.
func OptionsFromEnv() Options {
	return Options{
		URL: os.Getenv("HONK_URL"),
		Key: os.Getenv("HONK_KEY"),
		Defaults: Defaults{
			Source:      os.Getenv("HONK_SOURCE"),
			Environment: os.Getenv("HONK_ENVIRONMENT"),
			Channel:     os.Getenv("HONK_CHANNEL"),
		},
	}
}

// FromEnv is New(OptionsFromEnv()).
func FromEnv() (*Client, error) { return New(OptionsFromEnv()) }

// Send posts one event and returns once Honk has durably stored it (202), which does not
// mean a push was delivered. Network errors, 429 and 5xx are retried with the same
// Idempotency-Key until Retries or the deadline runs out. Errors are *Error.
func (c *Client) Send(ctx context.Context, m Message, opts ...Option) (*Accepted, error) {
	call := sendCall{msg: m}
	for _, o := range opts {
		o(&call)
	}
	body, err := encodeBody(call.msg, c.defaults, c.skipCheck)
	if err != nil {
		return nil, err
	}
	key := call.idempotencyKey
	if key == "" {
		key = NewIdempotencyKey()
	} else if !validIdempotencyKey(key) {
		e := localValidation([]FieldError{{Field: "Idempotency-Key", Code: "invalid_format", Message: "use 1-128 printable ASCII characters without spaces"}})
		return nil, e
	}

	deadline := time.Now().Add(c.deadline)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	for attempt := 1; ; attempt++ {
		acc, fail := c.attempt(ctx, body, key, min(c.timeout, time.Until(deadline)), attempt)
		if fail == nil {
			return acc, nil
		}
		if ctx.Err() != nil {
			return nil, contextError(ctx, key, attempt)
		}
		if !(fail.Kind == KindNetwork || fail.Kind == KindTimeout || fail.Kind == KindServer || fail.Kind == KindQuota) {
			return nil, fail
		}
		if attempt > c.retries {
			return nil, fail
		}
		wait := c.jitter(attempt)
		if fail.RetryAfter > wait {
			wait = fail.RetryAfter
		}
		if !time.Now().Add(wait).Before(deadline) {
			return nil, fail
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, contextError(ctx, key, attempt)
		case <-t.C:
		}
	}
}

func (c *Client) jitter(attempt int) time.Duration {
	ceiling := c.backoff.Max
	if shift := attempt - 1; shift < 30 {
		if d := c.backoff.Base << shift; d > 0 && d < ceiling {
			ceiling = d
		}
	}
	return time.Duration(rand.Int64N(int64(ceiling) + 1))
}

func contextError(ctx context.Context, key string, attempts int) *Error {
	kind := KindCanceled
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		kind = KindTimeout
	}
	return &Error{Kind: kind, Code: string(kind), Message: "gave up: " + ctx.Err().Error(), IdempotencyKey: key, Attempts: attempts, Err: ctx.Err()}
}

func (c *Client) attempt(ctx context.Context, body []byte, key string, timeout time.Duration, attempt int) (*Accepted, *Error) {
	if timeout <= 0 {
		timeout = time.Millisecond
	}
	actx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(actx, http.MethodPost, c.url+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, &Error{Kind: KindHTTP, Message: err.Error(), IdempotencyKey: key, Attempts: attempt, Err: err}
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Idempotency-Key", key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)

	res, err := c.http.Do(req)
	if err != nil {
		return nil, transportError(ctx, actx, err, timeout, key, attempt)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10)) // let the connection be reused
	res.Body.Close()
	if err != nil {
		return nil, transportError(ctx, actx, err, timeout, key, attempt)
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		var a Accepted
		if json.Unmarshal(data, &a) != nil || a.ID == "" {
			return nil, &Error{Kind: KindHTTP, Status: res.StatusCode, Message: "answer without a message id", IdempotencyKey: key, Attempts: attempt}
		}
		return &a, nil
	}
	return nil, responseError(res, data, key, attempt)
}

func transportError(ctx, actx context.Context, err error, timeout time.Duration, key string, attempt int) *Error {
	if ctx.Err() == nil && errors.Is(actx.Err(), context.DeadlineExceeded) {
		return &Error{Kind: KindTimeout, Code: "timeout", IdempotencyKey: key, Attempts: attempt, Err: err,
			Message: fmt.Sprintf("no answer within %s (the event may or may not have been stored; retrying with the same idempotency key is safe)", timeout)}
	}
	return &Error{Kind: KindNetwork, Code: "network_error", Message: "could not reach Honk: " + err.Error(), IdempotencyKey: key, Attempts: attempt, Err: err}
}

func responseError(res *http.Response, data []byte, key string, attempt int) *Error {
	var env struct {
		Error struct {
			Code      string       `json:"code"`
			Message   string       `json:"message"`
			RequestID string       `json:"request_id"`
			Fields    []FieldError `json:"fields"`
		} `json:"error"`
	}
	_ = json.Unmarshal(data, &env)
	e := &Error{
		Status: res.StatusCode, Code: env.Error.Code, Message: env.Error.Message, Fields: env.Error.Fields,
		RequestID: env.Error.RequestID, IdempotencyKey: key, Attempts: attempt,
		RetryAfter: parseRetryAfter(res.Header.Get("Retry-After"), time.Now()),
	}
	if e.RequestID == "" {
		e.RequestID = res.Header.Get("X-Request-ID")
	}
	if e.Message == "" {
		e.Message = strings.TrimSpace(string(data))
		if len(e.Message) > 200 {
			e.Message = e.Message[:200]
		}
		if e.Message == "" {
			e.Message = http.StatusText(res.StatusCode)
		}
	}
	switch s := res.StatusCode; {
	case s == 400 || s == 413 || s == 415 || s == 422:
		e.Kind = KindValidation
	case s == 401 || s == 403:
		e.Kind = KindAuth
	case s == 409:
		e.Kind = KindConflict
	case s == 429:
		e.Kind = KindQuota
	case s >= 500:
		e.Kind = KindServer
	case s >= 300 && s < 400:
		e.Kind = KindHTTP
		e.Message = "redirect"
		if loc := res.Header.Get("Location"); loc != "" {
			e.Message += " to " + loc
		}
		e.Message += "; set URL to the final https address"
	case s == 404:
		e.Kind = KindHTTP
		e.Message += " (is URL the base address of your Honk server?)"
	default:
		e.Kind = KindHTTP
	}
	return e
}

// parseRetryAfter reads delta-seconds or an HTTP date; 0 when absent or invalid.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now).Round(time.Second)
	}
	return 0
}

// Problem reports a problem for groupKey (opens or continues its incident). Severity defaults
// to Long (error); options may override it.
func (c *Client) Problem(ctx context.Context, groupKey, title, message string, opts ...Option) (*Accepted, error) {
	return c.helper(ctx, Message{Severity: SeverityError}, title, message, opts, func(m *Message) {
		m.GroupKey, m.EventType = groupKey, EventTypeProblem
	})
}

// Recovery reports that groupKey recovered (closes its open incident). Severity defaults to
// Beep (success); options may override it.
func (c *Client) Recovery(ctx context.Context, groupKey, title, message string, opts ...Option) (*Accepted, error) {
	return c.helper(ctx, Message{Severity: SeveritySuccess}, title, message, opts, func(m *Message) {
		m.GroupKey, m.EventType = groupKey, EventTypeRecovery
	})
}

// Light sends a light honk (severity info). An empty title lets the server derive one.
func (c *Client) Light(ctx context.Context, title, message string, opts ...Option) (*Accepted, error) {
	return c.withSeverity(ctx, SeverityInfo, title, message, opts)
}

// Beep sends a beep-beep (severity success).
func (c *Client) Beep(ctx context.Context, title, message string, opts ...Option) (*Accepted, error) {
	return c.withSeverity(ctx, SeveritySuccess, title, message, opts)
}

// Loud sends a loud honk (severity warning).
func (c *Client) Loud(ctx context.Context, title, message string, opts ...Option) (*Accepted, error) {
	return c.withSeverity(ctx, SeverityWarning, title, message, opts)
}

// Long sends a long honk (severity error; pushes at least as high priority).
func (c *Client) Long(ctx context.Context, title, message string, opts ...Option) (*Accepted, error) {
	return c.withSeverity(ctx, SeverityError, title, message, opts)
}

// Blast sends a blast (severity critical; pushes at least as high priority).
func (c *Client) Blast(ctx context.Context, title, message string, opts ...Option) (*Accepted, error) {
	return c.withSeverity(ctx, SeverityCritical, title, message, opts)
}

// Info is a synonym of Light.
func (c *Client) Info(ctx context.Context, title, message string, opts ...Option) (*Accepted, error) {
	return c.withSeverity(ctx, SeverityInfo, title, message, opts)
}

// Success is a synonym of Beep.
func (c *Client) Success(ctx context.Context, title, message string, opts ...Option) (*Accepted, error) {
	return c.withSeverity(ctx, SeveritySuccess, title, message, opts)
}

// Warning is a synonym of Loud.
func (c *Client) Warning(ctx context.Context, title, message string, opts ...Option) (*Accepted, error) {
	return c.withSeverity(ctx, SeverityWarning, title, message, opts)
}

// Error is a synonym of Long.
func (c *Client) Error(ctx context.Context, title, message string, opts ...Option) (*Accepted, error) {
	return c.withSeverity(ctx, SeverityError, title, message, opts)
}

// Critical is a synonym of Blast.
func (c *Client) Critical(ctx context.Context, title, message string, opts ...Option) (*Accepted, error) {
	return c.withSeverity(ctx, SeverityCritical, title, message, opts)
}

func (c *Client) withSeverity(ctx context.Context, s Severity, title, message string, opts []Option) (*Accepted, error) {
	return c.helper(ctx, Message{}, title, message, opts, func(m *Message) { m.Severity = s })
}

func (c *Client) helper(ctx context.Context, base Message, title, message string, opts []Option, force func(*Message)) (*Accepted, error) {
	base.Title, base.Message = title, message
	all := append(append([]Option{}, opts...), func(sc *sendCall) { force(&sc.msg) })
	return c.Send(ctx, base, all...)
}
