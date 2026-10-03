package honk

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits from contracts/openapi.yaml (and the server's validator).
const (
	MaxBodyBytes      = 16 << 10
	MaxMessageBytes   = 8192
	MaxTitle          = 160
	MaxSource         = 64
	MaxEnvironment    = 32
	MaxChannel        = 64
	MaxGroupKey       = 128
	MaxURLBytes       = 2048
	MaxMetadataKeys   = 16
	MaxMetadataString = 512
	MinTTLSeconds     = 60
	MaxTTLSeconds     = 86400
	maxSafeInteger    = 1<<53 - 1
)

var metadataKeyRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

type fieldErrs []FieldError

func (f *fieldErrs) add(field, code, msg string) {
	*f = append(*f, FieldError{Field: field, Code: code, Message: msg})
}

// hasControl mirrors the server: Unicode control characters and the line/paragraph separators.
func hasControl(s string, allowBreaks bool) bool {
	for _, r := range s {
		if allowBreaks && (r == '\n' || r == '\t' || r == '\r') {
			continue
		}
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return true
		}
	}
	return false
}

func shortText(f *fieldErrs, name, v string, max int) {
	if v == "" {
		return
	}
	s := strings.TrimSpace(v)
	switch {
	case !utf8.ValidString(v):
		f.add(name, "invalid_utf8", "must be valid UTF-8")
	case s == "":
		f.add(name, "too_short", "must not be empty")
	case utf8.RuneCountInString(s) > max:
		f.add(name, "too_long", "must be at most "+strconv.Itoa(max)+" characters")
	case hasControl(s, false):
		f.add(name, "invalid_format", "must not contain control characters or line breaks")
	}
}

func oneOf[T ~string](f *fieldErrs, name string, v T, allowed []T) {
	if v != "" && !slices.Contains(allowed, v) {
		names := make([]string, len(allowed))
		for i, a := range allowed {
			names[i] = string(a)
		}
		f.add(name, "invalid_enum", "must be one of "+strings.Join(names, ", "))
	}
}

// validate applies the cheap checks the server would apply anyway.
func validate(m Message) []FieldError {
	var f fieldErrs
	switch {
	case m.Message == "":
		f.add("message", "required", "message is required")
	case !utf8.ValidString(m.Message):
		f.add("message", "invalid_utf8", "must be valid UTF-8")
	case len(m.Message) > MaxMessageBytes:
		f.add("message", "too_long", fmt.Sprintf("must be at most %d bytes of UTF-8 (got %d)", MaxMessageBytes, len(m.Message)))
	case strings.TrimSpace(m.Message) == "":
		f.add("message", "too_short", "must not be blank")
	case hasControl(m.Message, true):
		f.add("message", "invalid_format", "must not contain control characters other than line breaks and tabs")
	}
	shortText(&f, "title", m.Title, MaxTitle)
	shortText(&f, "source", m.Source, MaxSource)
	shortText(&f, "environment", m.Environment, MaxEnvironment)
	shortText(&f, "channel", m.Channel, MaxChannel)
	shortText(&f, "group_key", m.GroupKey, MaxGroupKey)
	if m.Severity != "" && !slices.Contains(severities, m.Severity) {
		f.add("severity", "invalid_enum", "must be one of light (info), beep (success), loud (warning), long (error), blast (critical)")
	}
	oneOf(&f, "priority", m.Priority, priorities)
	oneOf(&f, "event_type", m.EventType, eventTypes)
	oneOf(&f, "category", m.Category, categories)

	if m.SourceSequence != nil && (*m.SourceSequence < 0 || *m.SourceSequence > maxSafeInteger) {
		f.add("source_sequence", "out_of_range", "must be between 0 and 2^53-1")
	}
	if m.GroupKey == "" {
		if m.EventType == EventTypeRecovery {
			f.add("group_key", "requires_group_key", "recovery events require group_key")
		}
		if m.SourceSequence != nil {
			f.add("source_sequence", "requires_group_key", "source_sequence requires group_key")
		}
	}
	if m.URL != "" && !validURL(m.URL, false) {
		f.add("url", "invalid_format", "must be an https URL without credentials, at most 2048 bytes")
	}
	if m.ImageURL != "" && !validURL(m.ImageURL, true) {
		f.add("image_url", "invalid_format", "must be an https URL without credentials or fragment, at most 2048 bytes")
	}

	if len(m.Metadata) > MaxMetadataKeys {
		f.add("metadata", "too_long", fmt.Sprintf("at most %d keys", MaxMetadataKeys))
	}
	keys := make([]string, 0, len(m.Metadata))
	for k := range m.Metadata {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		field := "metadata." + k
		if !metadataKeyRe.MatchString(k) {
			f.add(field, "invalid_format", "keys must match [A-Za-z0-9_.-]{1,64}")
			continue
		}
		if msg := checkMetadataValue(m.Metadata[k]); msg != "" {
			f.add(field, "invalid_format", msg)
		}
	}

	if m.TTLSeconds != 0 && (m.TTLSeconds < MinTTLSeconds || m.TTLSeconds > MaxTTLSeconds) {
		f.add("ttl_seconds", "out_of_range", fmt.Sprintf("must be between %d and %d", MinTTLSeconds, MaxTTLSeconds))
	}
	return f
}

func checkMetadataValue(v any) string {
	switch x := v.(type) {
	case string:
		if !utf8.ValidString(x) || utf8.RuneCountInString(x) > MaxMetadataString || hasControl(x, true) {
			return fmt.Sprintf("strings must be valid UTF-8, at most %d characters, without control characters", MaxMetadataString)
		}
	case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return "numbers must be finite"
		}
	case float32:
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return "numbers must be finite"
		}
	case json.Number:
		if _, err := x.Float64(); err != nil {
			return "invalid number"
		}
	default:
		return fmt.Sprintf("values must be strings, numbers or booleans (got %T)", v)
	}
	return ""
}

// validURL is the server's syntactic check: https, a host, no credentials, ≤ 2048 bytes;
// image URLs additionally have no fragment and a valid port.
func validURL(raw string, image bool) bool {
	s := strings.TrimSpace(raw)
	if s == "" || len(s) > MaxURLBytes || hasControl(s, false) || strings.ContainsAny(s, " \\") {
		return false
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" {
		return false
	}
	if image {
		if strings.Contains(s, "#") || u.Hostname() == "" {
			return false
		}
		if p := u.Port(); p != "" {
			if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
				return false
			}
		}
	}
	return true
}

// validIdempotencyKey: 1–128 printable ASCII characters (0x21–0x7E).
func validIdempotencyKey(k string) bool {
	if len(k) == 0 || len(k) > 128 {
		return false
	}
	for i := 0; i < len(k); i++ {
		if k[i] < 0x21 || k[i] > 0x7e {
			return false
		}
	}
	return true
}

// EncodeMessage validates m (with defaults applied) and returns the exact JSON body Send would
// post. Useful for logging, dry runs and tests.
func EncodeMessage(m Message, defaults Defaults) ([]byte, error) {
	return encodeBody(m, defaults, false)
}

// encodeBody returns the JSON body for m (defaults applied), validating it unless skip is set.
func encodeBody(m Message, d Defaults, skipValidation bool) ([]byte, error) {
	m = m.normalized(d)
	if skipValidation {
		if m.Message == "" {
			return nil, localValidation([]FieldError{{Field: "message", Code: "required", Message: "message is required"}})
		}
	} else if errs := validate(m); len(errs) > 0 {
		return nil, localValidation(errs)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m.wire()); err != nil {
		return nil, localValidation([]FieldError{{Field: "body", Code: "invalid_format", Message: err.Error()}})
	}
	body := bytes.TrimRight(buf.Bytes(), "\n")
	if len(body) > MaxBodyBytes {
		return nil, localValidation([]FieldError{{Field: "body", Code: "too_long",
			Message: fmt.Sprintf("the JSON body is %d bytes; Honk accepts at most 16 KiB", len(body))}})
	}
	return body, nil
}
