package platform

import (
	"regexp"
	"strings"
)

// Free-text values supplied by clients (device names, user agents, labels)
// regularly end up in persistent audit metadata. They are a PII leak
// channel: users paste email addresses, phone numbers and names into
// "device name" fields, and ADR-0005 forbids PII in log/audit attributes.
// The Redact helpers neutralise that channel while keeping the operational
// value of the field (a distinguishable device label).
//
// Used by audit writers (identity service) before metadata enters the
// audit trail; see docs/PII-HANDLING.md for the full policy.

var (
	emailPattern   = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	phonePattern   = regexp.MustCompile(`(\+?\d[\d\-()\s]{8,}\d)`)
	controlChars   = regexp.MustCompile(`[\x00-\x1f\x7f]`)
	whitespaceRuns = regexp.MustCompile(`\s+`)
)

// MaxRedactedFieldLen bounds how much free text survives redaction.
const MaxRedactedFieldLen = 64

// RedactFreeText sanitises a client-supplied string for audit metadata:
// control characters are stripped, email addresses are masked to their
// domain, phone-number-shaped runs are masked, runs of whitespace
// collapse, and the result is truncated to MaxRedactedFieldLen.
func RedactFreeText(s string) string {
	s = controlChars.ReplaceAllString(s, " ")
	s = emailPattern.ReplaceAllStringFunc(s, func(match string) string {
		at := strings.LastIndexByte(match, '@')
		return "***@redacted" + match[at:]
	})
	s = phonePattern.ReplaceAllString(s, "***redacted-phone***")
	s = whitespaceRuns.ReplaceAllString(strings.TrimSpace(s), " ")
	if len(s) > MaxRedactedFieldLen {
		s = s[:MaxRedactedFieldLen]
	}
	return s
}

// RedactEmail masks the local part of an email while keeping the domain —
// enough to correlate registrations per domain without storing the raw
// address in free-form metadata. (The canonical email itself stays in the
// users table, which is its proper home.)
func RedactEmail(email string) string {
	at := strings.LastIndexByte(email, '@')
	if at < 1 || at == len(email)-1 {
		return "***@redacted"
	}
	local := email[:at]
	if len(local) > 2 {
		local = local[:2] + "***"
	} else {
		local = "***"
	}
	return local + "@redacted" + email[at:]
}
