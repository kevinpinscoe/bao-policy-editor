package tui

import (
	"fmt"
	"net"
	"net/url"
	"testing"
)

// loopbackOnly reports why an address is unacceptable to the live smoke test,
// or nil if it is genuinely a loopback HTTP endpoint.
//
// The live smoke test writes and deletes policies. Pointed at a real server it
// would edit production, so the address it is given is the last line of
// defence and has to be parsed rather than pattern-matched.
//
// It deliberately lives in an untagged file, so `go test ./...` exercises it
// even though the only caller is behind the `livesmoke` build tag. The guard is
// the part that must not be wrong; it should not be the part nobody runs.
//
// The check this replaced was `strings.HasPrefix(addr, "http://127.0.0.1:")`,
// which is wrong in both directions:
//
//   - `http://127.0.0.1:8211@openbao.example.com` satisfies it, and resolves to
//     openbao.example.com — everything before the `@` is userinfo, not a host.
//     That is the exact shape that would aim a live write test at a real
//     server while looking, in a diff, like a loopback address.
//   - `http://[::1]:8211` fails it, though ::1 is loopback.
func loopbackOnly(addr string) error {
	parsed, err := url.Parse(addr)
	if err != nil {
		return fmt.Errorf("%q is not a parseable URL: %w", addr, err)
	}
	if parsed.Scheme != "http" {
		return fmt.Errorf("%q does not use http; the disposable dev server is plain http on loopback", addr)
	}
	// Userinfo is what makes the prefix check defeatable, so its presence is
	// refused outright rather than ignored: there is no legitimate reason for
	// a dev server address to carry credentials in the URL.
	if parsed.User != nil {
		return fmt.Errorf("%q carries userinfo before the host, which disguises where it really points", addr)
	}
	if parsed.Opaque != "" {
		return fmt.Errorf("%q is not a hierarchical URL", addr)
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return fmt.Errorf("%q has a path; the address must be a bare host and port", addr)
	}
	port := parsed.Port()
	if port == "" {
		return fmt.Errorf("%q names no port", addr)
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	if ip == nil {
		// A name is refused even if it would resolve to 127.0.0.1: what it
		// resolves to is not fixed, and a DNS answer is not something this
		// check can stand behind.
		return fmt.Errorf("%q names a host rather than a loopback IP literal", addr)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("%q points at %s, which is not a loopback address", addr, ip)
	}
	return nil
}

// TestLoopbackGuardAcceptsOnlyRealLoopbackAddresses pins the guard that keeps
// the live smoke test off a real server.
//
// The disguised-host case is the one that matters: it is the only entry here
// that the previous implementation accepted.
func TestLoopbackGuardAcceptsOnlyRealLoopbackAddresses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		addr   string
		accept bool
	}{
		{"the ordinary case", "http://127.0.0.1:8211", true},
		{"another loopback address in 127.0.0.0/8", "http://127.0.0.2:8211", true},
		{"IPv6 loopback", "http://[::1]:8211", true},
		{"a trailing slash is still bare", "http://127.0.0.1:8211/", true},

		{"userinfo disguising a real host", "http://127.0.0.1:8211@openbao.example.com", false},
		{"userinfo with a password disguising a real host", "http://127.0.0.1:pass@example.com:8200", false},
		{"a hostname that merely starts with the loopback digits", "http://127.0.0.1.example.com:8200", false},
		{"a public address", "https://openbao.example.com:8200", false},
		{"a private address that is not loopback", "http://10.1.10.20:8200", false},
		{"https, which the dev server never speaks", "https://127.0.0.1:8211", false},
		{"no port", "http://127.0.0.1", false},
		{"a path, which may be a proxy prefix", "http://127.0.0.1:8211/v1/proxy", false},
		{"localhost, which is a name and can be re-pointed", "http://localhost:8211", false},
		{"empty", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := loopbackOnly(tc.addr)
			if tc.accept && err != nil {
				t.Errorf("loopbackOnly(%q) = %v, want accepted", tc.addr, err)
			}
			if !tc.accept && err == nil {
				t.Errorf("loopbackOnly(%q) accepted an address it must refuse", tc.addr)
			}
		})
	}
}
