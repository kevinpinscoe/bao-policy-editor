package config

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

const rawSecret = "s.SyntheticTestTokenValue123456"

// assertNoLeak fails the test if s contains the raw secret anywhere.
func assertNoLeak(t *testing.T, label, s string) {
	t.Helper()
	if strings.Contains(s, rawSecret) {
		t.Errorf("%s leaked the raw token: %q", label, s)
	}
}

func TestSensitiveString_BareValue_AllVerbs(t *testing.T) {
	s := SensitiveString(rawSecret)

	cases := map[string]string{
		"%v":  fmt.Sprintf("%v", s),
		"%+v": fmt.Sprintf("%+v", s),
		"%#v": fmt.Sprintf("%#v", s),
		"%s":  fmt.Sprintf("%s", s),
		"%q":  fmt.Sprintf("%q", s),
		"%x":  fmt.Sprintf("%x", s),
	}
	for verb, out := range cases {
		assertNoLeak(t, verb, out)
	}
}

func TestSensitiveString_EmptyValue(t *testing.T) {
	var s SensitiveString

	if got := fmt.Sprintf("%v", s); got != `""` {
		t.Errorf(`fmt.Sprintf("%%v", empty) = %q, want %q`, got, `""`)
	}
	if s.IsSet() {
		t.Error("IsSet() on zero value = true, want false")
	}
	if s.Reveal() != "" {
		t.Errorf("Reveal() on zero value = %q, want empty", s.Reveal())
	}
}

func TestSensitiveString_Reveal(t *testing.T) {
	s := SensitiveString(rawSecret)
	if got := s.Reveal(); got != rawSecret {
		t.Errorf("Reveal() = %q, want %q", got, rawSecret)
	}
	if !s.IsSet() {
		t.Error("IsSet() on non-empty value = false, want true")
	}
}

func TestSensitiveString_FieldInStruct_AllVerbs(t *testing.T) {
	cfg := Config{Token: SensitiveString(rawSecret), Address: "https://bao.example.com"}

	cases := map[string]string{
		"%v":  fmt.Sprintf("%v", cfg),
		"%+v": fmt.Sprintf("%+v", cfg),
		"%#v": fmt.Sprintf("%#v", cfg),
	}
	for verb, out := range cases {
		assertNoLeak(t, verb, out)
	}

	if !strings.Contains(cases["%v"], "https://bao.example.com") {
		t.Error("non-sensitive Address field should still be visible in the default-verb output")
	}
}

func TestSensitiveString_ErrorFormatting(t *testing.T) {
	cfg := Config{Token: SensitiveString(rawSecret)}

	errV := fmt.Errorf("resolving config: %v", cfg)
	assertNoLeak(t, "fmt.Errorf %v", errV.Error())

	errWrapped := fmt.Errorf("outer: %w", errors.New(fmt.Sprintf("inner: %+v", cfg)))
	assertNoLeak(t, "wrapped error chain", errWrapped.Error())
}

func TestSensitiveString_DebugFormatting(t *testing.T) {
	cfg := Config{Token: SensitiveString(rawSecret), Namespace: "team-a"}

	// A plausible "debug dump" call site: Sprintf with %#v for a log line.
	dump := fmt.Sprintf("config debug dump: %#v", cfg)
	assertNoLeak(t, "debug dump %#v", dump)
	if !strings.Contains(dump, "team-a") {
		t.Error("non-sensitive Namespace field should still be visible in debug dump")
	}
}
