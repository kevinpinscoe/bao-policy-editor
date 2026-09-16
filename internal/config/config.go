// Package config resolves BPE's configuration from command-line flags,
// BAO_* environment variables, VAULT_* fallbacks, and safe defaults, in
// that precedence order. It never reads a persistent BPE configuration
// file and never contacts OpenBao — see Resolve's doc comment.
package config

import (
	"strconv"

	"github.com/kevinpinscoe/bao-policy-editor/internal/apperr"
)

// Config holds the values needed for future OpenBao connectivity. Nothing
// in this ticket uses them to contact a server; Resolve only computes
// them.
//
// Token is deliberately typed SensitiveString rather than string: keeping
// it as a normal exported string field would let it leak through an
// unguarded fmt.Printf/%v/%+v/%#v of the whole Config (verified against
// Go's fmt semantics — see sensitive.go's doc comment), and keeping it
// unexported would be worse, not better: fmt bypasses a field's own
// String/Format methods for unexported struct fields and prints the raw
// underlying value instead. SensitiveString is the one representation that
// stays safe under every formatting path while still being an ordinary,
// directly accessible struct field.
type Config struct {
	// Address is the OpenBao server URL (BAO_ADDR / VAULT_ADDR).
	Address string

	// Token is the OpenBao authentication token (BAO_TOKEN / VAULT_TOKEN).
	// Call Token.Reveal() only where the raw value is actually required —
	// see SensitiveString's doc comment.
	Token SensitiveString

	// Namespace is the optional OpenBao namespace (BAO_NAMESPACE /
	// VAULT_NAMESPACE).
	Namespace string

	// CACert is an optional CA certificate file path (BAO_CACERT /
	// VAULT_CACERT).
	CACert string

	// CAPath is an optional CA certificate directory path (BAO_CAPATH /
	// VAULT_CAPATH).
	CAPath string

	// ClientCert is an optional client certificate file path
	// (BAO_CLIENT_CERT / VAULT_CLIENT_CERT).
	ClientCert string

	// ClientKey is an optional client private key file path
	// (BAO_CLIENT_KEY / VAULT_CLIENT_KEY).
	ClientKey string

	// TLSServerName is an optional TLS server name override
	// (BAO_TLS_SERVER_NAME / VAULT_TLS_SERVER_NAME).
	TLSServerName string

	// SkipVerify disables TLS certificate verification when true
	// (BAO_SKIP_VERIFY / VAULT_SKIP_VERIFY). Defaults to false — BPE never
	// disables verification unless explicitly told to.
	SkipVerify bool
}

// EnvLookup matches os.LookupEnv's signature: (value, present). Resolve
// takes this as a parameter, rather than reading os.Environ itself, so
// tests can supply a synthetic environment without mutating the real one.
type EnvLookup func(key string) (string, bool)

// Flags holds explicitly-supplied command-line flag values. A nil field
// means the flag was not given on the command line at all; a non-nil
// field — including a pointer to an empty string — means it was given
// explicitly, even if the value itself is empty. This distinction is what
// makes "empty value" behavior deliberate rather than accidental: an
// explicit --address="" is a real, intentional override of a lower-
// precedence value, not the same as omitting --address.
type Flags struct {
	Address       *string
	Token         *string
	Namespace     *string
	CACert        *string
	CAPath        *string
	ClientCert    *string
	ClientKey     *string
	TLSServerName *string

	// SkipVerify holds the flag's raw text (e.g. "true", "1"), parsed by
	// Resolve, so that a malformed value produces the same clear error
	// whether it came from a flag or an environment variable.
	SkipVerify *string
}

// setting describes where a resolved value came from, for building clear
// error messages (in particular for a boolean parse failure).
type setting struct {
	value string
	set   bool
	label string
}

// firstSet resolves one configuration value in BPE's documented
// precedence: explicit flag, then BAO_<name>, then VAULT_<name>, then
// "not set" (the caller applies the safe default). An environment
// variable that is present but empty still counts as set at that tier —
// it does not fall through to VAULT_* or to the default — matching how
// the flag case works and satisfying "empty values must have deliberate
// and tested behavior."
func firstSet(flag *string, flagLabel, baoKey, vaultKey string, lookup EnvLookup) setting {
	if flag != nil {
		return setting{value: *flag, set: true, label: flagLabel}
	}
	if v, ok := lookup(baoKey); ok {
		return setting{value: v, set: true, label: baoKey}
	}
	if v, ok := lookup(vaultKey); ok {
		return setting{value: v, set: true, label: vaultKey}
	}
	return setting{}
}

// Resolve computes a Config from explicit flags, BAO_*/VAULT_* environment
// variables (via lookup), and safe defaults. It performs no I/O beyond
// calling lookup: it does not read a persistent BPE configuration file,
// does not contact OpenBao, and does not validate network reachability —
// those are out of scope for this ticket. The only failure mode is a
// malformed boolean value for BAO_SKIP_VERIFY/VAULT_SKIP_VERIFY/
// --skip-verify, reported as an *apperr.AppError with ExitUsage.
func Resolve(flags Flags, lookup EnvLookup) (Config, error) {
	var cfg Config

	cfg.Address = firstSet(flags.Address, "--address", "BAO_ADDR", "VAULT_ADDR", lookup).value
	cfg.Token = SensitiveString(firstSet(flags.Token, "--token", "BAO_TOKEN", "VAULT_TOKEN", lookup).value)
	cfg.Namespace = firstSet(flags.Namespace, "--namespace", "BAO_NAMESPACE", "VAULT_NAMESPACE", lookup).value
	cfg.CACert = firstSet(flags.CACert, "--ca-cert", "BAO_CACERT", "VAULT_CACERT", lookup).value
	cfg.CAPath = firstSet(flags.CAPath, "--ca-path", "BAO_CAPATH", "VAULT_CAPATH", lookup).value
	cfg.ClientCert = firstSet(flags.ClientCert, "--client-cert", "BAO_CLIENT_CERT", "VAULT_CLIENT_CERT", lookup).value
	cfg.ClientKey = firstSet(flags.ClientKey, "--client-key", "BAO_CLIENT_KEY", "VAULT_CLIENT_KEY", lookup).value
	cfg.TLSServerName = firstSet(flags.TLSServerName, "--tls-server-name", "BAO_TLS_SERVER_NAME", "VAULT_TLS_SERVER_NAME", lookup).value

	skip := firstSet(flags.SkipVerify, "--skip-verify", "BAO_SKIP_VERIFY", "VAULT_SKIP_VERIFY", lookup)
	if skip.set {
		parsed, err := strconv.ParseBool(skip.value)
		if err != nil {
			return Config{}, apperr.Usagef(
				"invalid boolean value for %s: %q (want one of: 1, t, T, TRUE, true, True, 0, f, F, FALSE, false, False)",
				skip.label, skip.value,
			)
		}
		cfg.SkipVerify = parsed
	}
	// else: safe default — false. BPE never disables TLS verification
	// unless explicitly told to.

	return cfg, nil
}
