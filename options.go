package honk

import "time"

// Option adjusts one Send (or helper) call: the idempotency key, or message fields.
type Option func(*sendCall)

type sendCall struct {
	msg            Message
	idempotencyKey string
}

// WithIdempotencyKey sets a stable key for this event (1–128 printable ASCII characters),
// e.g. "request-4812". It is reused on every retry. Default: a new UUIDv7 per call.
func WithIdempotencyKey(key string) Option { return func(c *sendCall) { c.idempotencyKey = key } }

// WithSeverity sets the severity: a horn name (honk.Loud) or canonical value, any case
// (helpers like Problem default it; Light…Blast fix it).
func WithSeverity(s Severity) Option { return func(c *sendCall) { c.msg.Severity = s } }

// WithPriority sets the declared priority (urgent needs a key with allow_urgent).
func WithPriority(p Priority) Option { return func(c *sendCall) { c.msg.Priority = p } }

// WithCategory sets the category.
func WithCategory(cat Category) Option { return func(c *sendCall) { c.msg.Category = cat } }

// WithSource sets the source (≤ 64 characters).
func WithSource(s string) Option { return func(c *sendCall) { c.msg.Source = s } }

// WithEnvironment sets the environment (≤ 32 characters).
func WithEnvironment(e string) Option { return func(c *sendCall) { c.msg.Environment = e } }

// WithChannel sets the channel (≤ 64 characters).
func WithChannel(ch string) Option { return func(c *sendCall) { c.msg.Channel = ch } }

// WithGroupKey sets the group key, e.g. "requests/4812".
func WithGroupKey(k string) Option { return func(c *sendCall) { c.msg.GroupKey = k } }

// WithOccurredAt sets when the event happened at the source.
func WithOccurredAt(t time.Time) Option { return func(c *sendCall) { c.msg.OccurredAt = t } }

// WithURL sets the https link shown as "Open link".
func WithURL(u string) Option { return func(c *sendCall) { c.msg.URL = u } }

// WithImageURL sets an https image the server fetches after ingestion.
func WithImageURL(u string) Option { return func(c *sendCall) { c.msg.ImageURL = u } }

// WithActions appends buttons to the message (at most 3 in all, in display order).
func WithActions(actions ...Action) Option {
	return func(c *sendCall) { c.msg.Actions = append(append([]Action(nil), c.msg.Actions...), actions...) }
}

// WithMetadata merges keys into the message metadata.
func WithMetadata(md map[string]any) Option {
	return func(c *sendCall) {
		merged := make(map[string]any, len(c.msg.Metadata)+len(md))
		for k, v := range c.msg.Metadata {
			merged[k] = v
		}
		for k, v := range md {
			merged[k] = v
		}
		c.msg.Metadata = merged
	}
}

// WithTTLSeconds sets the push lifetime (60–86400 seconds).
func WithTTLSeconds(s int) Option { return func(c *sendCall) { c.msg.TTLSeconds = s } }

// WithSourceSequence sets the monotonic sequence for problem/recovery ordering.
func WithSourceSequence(n int64) Option { return func(c *sendCall) { c.msg.SourceSequence = &n } }
