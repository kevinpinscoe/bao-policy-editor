package config

import (
	"fmt"
	"io"
)

// redactedPlaceholder is what a non-empty SensitiveString prints as under
// every formatting path. It never varies with the wrapped value, so its
// length and shape give no information about the value itself.
const redactedPlaceholder = "<redacted>"

// emptyPlaceholder is what an unset (zero-value) SensitiveString prints as.
// Distinguishing "empty" from "set" in diagnostic output is useful (e.g.
// "was BAO_TOKEN even provided?") and leaks nothing, since an empty string
// carries no secret material.
const emptyPlaceholder = `""`

// SensitiveString wraps a value — currently only the OpenBao token — that
// must never appear in logs, error messages, diagnostic output, help text,
// or test failure output. The only way to retrieve the wrapped value is
// the explicit Reveal method; ordinary field access (Config.Token) yields
// a SensitiveString that is safe to print, log, or embed in an error.
//
// Config.Token is deliberately an exported field of this type rather than
// an unexported plain string. That is not a stylistic choice: Go's fmt
// package only consults a value's Stringer/Formatter/GoStringer methods
// for a struct field when reflect.Value.CanInterface() is true for that
// field, which is false for unexported fields. An unexported string field
// prints its raw underlying value when the parent struct is formatted —
// completely bypassing any String/Format method the field's type
// defines — while an exported field of a type implementing fmt.Formatter
// is fully protected. This was verified empirically against the Go
// toolchain before choosing this design (see FSM-10's implementation
// notes); do not "harden" this by making the field unexported, as that
// would silently defeat the whole mechanism.
//
// SensitiveString implements fmt.Formatter, which fmt consults before
// fmt.Stringer/fmt.GoStringer and before the default reflection-based
// struct/field printer, so it controls every verb — %v, %+v, %#v, %s, %q,
// %x, and so on — for both a bare SensitiveString value and a
// SensitiveString field inside another struct (such as Config), including
// when that struct is embedded in an error via fmt.Errorf. String and
// GoString are implemented too, as a second line of defense for any code
// path that checks those interfaces directly rather than going through
// fmt (some logging libraries do this).
type SensitiveString string

// Reveal returns the wrapped value. Call this only where the raw value is
// actually required — e.g. handing an OpenBao token to the API client in a
// later ticket — never for logging, printing, error construction, or
// diagnostic output. The name is deliberately loud so every call site is
// easy to find with a plain-text search.
func (s SensitiveString) Reveal() string {
	return string(s)
}

// IsSet reports whether a value was ever assigned, without exposing it.
func (s SensitiveString) IsSet() bool {
	return s != ""
}

func (s SensitiveString) placeholder() string {
	if s == "" {
		return emptyPlaceholder
	}
	return redactedPlaceholder
}

// Format implements fmt.Formatter. It takes full control of formatting for
// this type regardless of verb or flags, so no combination of fmt verbs
// (%v, %+v, %#v, %s, %q, %x, %d, ...) can expose the wrapped value.
func (s SensitiveString) Format(f fmt.State, verb rune) {
	_, _ = io.WriteString(f, s.placeholder())
}

// String implements fmt.Stringer as a second line of defense for code that
// checks Stringer directly instead of going through fmt.
func (s SensitiveString) String() string {
	return s.placeholder()
}

// GoString implements fmt.GoStringer as a second line of defense for code
// that checks GoStringer directly instead of going through fmt.
func (s SensitiveString) GoString() string {
	return s.placeholder()
}
