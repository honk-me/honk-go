package honk

import (
	"strings"
	"time"
)

// Severity of a message, lowest to highest. Error and critical raise the effective priority
// to at least high. Every severity has a horn name on the Honk scale (Light, Beep, Loud, Long,
// Blast); those constants are aliases of the canonical values.
type Severity string

// The Honk scale: horn names, aliases of the canonical severities (honk.Loud == honk.SeverityWarning).
const (
	Light Severity = SeverityInfo     // light honk
	Beep  Severity = SeveritySuccess  // beep-beep
	Loud  Severity = SeverityWarning  // loud honk
	Long  Severity = SeverityError    // long honk
	Blast Severity = SeverityCritical // blast
)

// Canonical severities, as stored and returned by the server.
const (
	SeverityInfo     Severity = "info"
	SeveritySuccess  Severity = "success"
	SeverityWarning  Severity = "warning"
	SeverityError    Severity = "error"
	SeverityCritical Severity = "critical"
)

// SeverityAliases maps the horn names to the canonical severities.
var SeverityAliases = map[string]Severity{
	"light": SeverityInfo, "beep": SeveritySuccess, "loud": SeverityWarning, "long": SeverityError, "blast": SeverityCritical,
}

// ParseSeverity returns the canonical severity for a canonical value or horn alias,
// case-insensitively: ParseSeverity("LOUD") == SeverityWarning.
func ParseSeverity(s string) (Severity, bool) {
	v := strings.ToLower(strings.TrimSpace(s))
	for _, c := range severities {
		if string(c) == v {
			return c, true
		}
	}
	c, ok := SeverityAliases[v]
	return c, ok
}

// Priority declared by the source. Urgent needs an ingestion key with allow_urgent.
type Priority string

const (
	PriorityLow    Priority = "low"
	PriorityNormal Priority = "normal"
	PriorityHigh   Priority = "high"
	PriorityUrgent Priority = "urgent"
)

// EventType: a problem opens an incident for its group, a recovery closes it. Both need a
// GroupKey.
type EventType string

const (
	EventTypeEvent    EventType = "event"
	EventTypeProblem  EventType = "problem"
	EventTypeRecovery EventType = "recovery"
)

// Category from taxonomy v1.
type Category string

const (
	CategoryInfrastructure Category = "infrastructure"
	CategorySecurity       Category = "security"
	CategoryBackups        Category = "backups"
	CategoryDeployments    Category = "deployments"
	CategoryPayments       Category = "payments"
	CategoryCustomers      Category = "customers"
	CategorySales          Category = "sales"
	CategoryAutomation     Category = "automation"
	CategoryPersonal       Category = "personal"
	CategoryOther          Category = "other"
)

var (
	severities = []Severity{SeverityInfo, SeveritySuccess, SeverityWarning, SeverityError, SeverityCritical}
	priorities = []Priority{PriorityLow, PriorityNormal, PriorityHigh, PriorityUrgent}
	eventTypes = []EventType{EventTypeEvent, EventTypeProblem, EventTypeRecovery}
	categories = []Category{CategoryInfrastructure, CategorySecurity, CategoryBackups, CategoryDeployments,
		CategoryPayments, CategoryCustomers, CategorySales, CategoryAutomation, CategoryPersonal, CategoryOther}
)

// Message is one event for POST /v1/messages. Only Message is required; zero values are
// omitted, so the server defaults apply (severity info, priority normal, source "api",
// environment "default", channel "general", event type event, TTL 3600 s).
type Message struct {
	// Title is one line, at most 160 characters. Defaults to the first line of Message.
	Title string
	// Message is plain text, 1–8192 bytes of UTF-8. Line breaks and tabs are allowed.
	Message string
	// Severity: a horn name (Light, Beep, Loud, Long, Blast) or canonical value; any case.
	// Sent canonical. Default light (info).
	Severity Severity
	Priority Priority
	Category Category
	// Source (≤ 64), Environment (≤ 32) and Channel (≤ 64) default to the client's Defaults.
	Source      string
	Environment string
	Channel     string
	// GroupKey (≤ 128) relates occurrences: messages with the same key (per environment,
	// source and channel) form one group, so the first one pushes and repeats update it calmly.
	// Use one key per customer request ("requests/<id>"), a shared key only for repeats of the
	// same problem.
	GroupKey  string
	EventType EventType
	// OccurredAt is when it happened at the source (informational). Zero means unset.
	OccurredAt time.Time
	// URL is an https link shown as "Open link" (no credentials, ≤ 2048 bytes).
	URL string
	// ImageURL is an https image the server fetches after ingestion (no credentials or
	// fragment, ≤ 2048 bytes).
	ImageURL string
	// Metadata has at most 16 keys matching [A-Za-z0-9_.-]{1,64}; values are strings
	// (≤ 512 characters), numbers or booleans.
	Metadata map[string]any
	// TTLSeconds is the push lifetime, 60–86400. Zero means the server default (3600).
	TTLSeconds int
	// SourceSequence is a monotonic counter per source stream (0 … 2^53-1) so a delayed
	// recovery can never close a newer problem. Requires GroupKey. Use honk.Ptr(n).
	SourceSequence *int64
}

// Defaults are applied to every message that leaves these fields empty.
type Defaults struct {
	Source      string
	Environment string
	Channel     string
}

// Accepted is the 202 answer: the message is durably stored (not necessarily pushed yet).
type Accepted struct {
	// ID of the message (msg_…). The original ID when Duplicate is true.
	ID string `json:"id"`
	// Duplicate is true when this idempotency key was already accepted with the same payload
	// in the last 24 hours.
	Duplicate bool `json:"duplicate"`
	// ReceivedAt is when the server accepted it (the first time, for a duplicate).
	ReceivedAt time.Time `json:"received_at"`
}

// Ptr returns a pointer to v, e.g. Message{SourceSequence: honk.Ptr[int64](42)}.
func Ptr[T any](v T) *T { return &v }

// wireMessage is the exact JSON of contracts/openapi.yaml MessageRequest.
type wireMessage struct {
	Title          string         `json:"title,omitempty"`
	Message        string         `json:"message"`
	Severity       Severity       `json:"severity,omitempty"`
	Priority       Priority       `json:"priority,omitempty"`
	Category       Category       `json:"category,omitempty"`
	Source         string         `json:"source,omitempty"`
	Environment    string         `json:"environment,omitempty"`
	Channel        string         `json:"channel,omitempty"`
	GroupKey       string         `json:"group_key,omitempty"`
	EventType      EventType      `json:"event_type,omitempty"`
	OccurredAt     string         `json:"occurred_at,omitempty"`
	URL            string         `json:"url,omitempty"`
	ImageURL       string         `json:"image_url,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	TTLSeconds     int            `json:"ttl_seconds,omitempty"`
	SourceSequence *int64         `json:"source_sequence,omitempty"`
}

// normalized applies the defaults and turns a horn alias into the canonical severity, so the
// idempotency payload is canonical and older servers understand it.
func (m Message) normalized(d Defaults) Message {
	if s, ok := ParseSeverity(string(m.Severity)); ok {
		m.Severity = s
	}
	return m.withDefaults(d)
}

func (m Message) withDefaults(d Defaults) Message {
	if m.Source == "" {
		m.Source = d.Source
	}
	if m.Environment == "" {
		m.Environment = d.Environment
	}
	if m.Channel == "" {
		m.Channel = d.Channel
	}
	return m
}

func (m Message) wire() wireMessage {
	w := wireMessage{
		Title: m.Title, Message: m.Message, Severity: m.Severity, Priority: m.Priority,
		Category: m.Category, Source: m.Source, Environment: m.Environment, Channel: m.Channel,
		GroupKey: m.GroupKey, EventType: m.EventType, URL: m.URL, ImageURL: m.ImageURL,
		TTLSeconds: m.TTLSeconds, SourceSequence: m.SourceSequence,
	}
	if !m.OccurredAt.IsZero() {
		w.OccurredAt = m.OccurredAt.UTC().Format("2006-01-02T15:04:05.000Z07:00")
	}
	if len(m.Metadata) > 0 {
		w.Metadata = m.Metadata
	}
	return w
}
