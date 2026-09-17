// Package baoclient is BPE's OpenBao API client: a narrow, mockable
// surface over the ACL policy endpoints (sys/policies/acl), built on the
// official OpenBao Go client.
//
// It has no dependency on Bubble Tea, on internal/tui, or on the terminal
// at all — enforced by a test, not just by convention — so the editor can
// be built against the PolicyStore interface and tested with a fake while
// the real HTTP path is exercised only here, against httptest. No test in
// this package needs a live server, a network, or a credential.
//
// # Conflict safety
//
// Updates are conflict-safe through OpenBao's own check-and-set, not
// through anything this package invents. A read captures the policy's
// `version`; an update sends it back as `cas`, and the server rejects the
// write if the policy has moved on. Where a server does not report a
// version, Update refuses rather than falling back to a read-compare-write
// sequence — that sequence has a race between the compare and the write,
// and offering it would mean telling the user an update was protected when
// it was not. Kevin's instruction, 2026-09-17.
//
// Deletion has no check-and-set equivalent on this endpoint, and this
// package does not pretend otherwise; see PolicyStore.Delete.
//
// # Credentials
//
// The token is held as a config.SensitiveString and is never logged,
// never placed in an error, and never written anywhere. Errors leaving
// this package are additionally scrubbed of it as a backstop — see scrub
// in errors.go. Nothing here persists a token, reads a secret value, or
// touches any credential store.
package baoclient

import (
	"crypto/tls"
	"fmt"
	"net/http"

	openbao "github.com/openbao/openbao/api/v2"

	"github.com/kevinpinscoe/bao-policy-editor/internal/apperr"
	"github.com/kevinpinscoe/bao-policy-editor/internal/config"
)

// defaultMaxRetries is how many times a request is retried after a 5xx or
// a connection failure, with the client's own exponential backoff.
//
// It is set explicitly rather than inherited. The OpenBao client defaults
// to 2, and BPE would otherwise be carrying a retry policy it never chose
// — which matters here because these are interactive operations: a user
// who has just pressed save is waiting through every one of those
// attempts, and a retried write is a write that may already have landed.
// Two is kept, as a reasonable middle ground for a transient 5xx, and
// named so it is a decision rather than an accident.
//
// Note that BAO_MAX_RETRIES has no effect on BPE. Reading it would mean
// re-enabling the client's own environment reading, which is exactly what
// New avoids; see New's doc comment.
const defaultMaxRetries = 2

// Client is a configured OpenBao API client scoped to policy operations.
type Client struct {
	api *openbao.Client

	// token is the raw token, held only so errors can be scrubbed of it
	// before they leave the package. It is never logged or returned.
	token string

	warnings []string
}

// New builds a client from BPE's already-resolved configuration.
//
// # Why not api.DefaultConfig
//
// api.DefaultConfig calls ReadEnvironment, which reads BAO_*/VAULT_*
// itself. BPE has already resolved those, in a documented order that
// includes distinctions the client's own reader does not make — notably
// that an explicitly empty flag value is a deliberate override rather than
// an absent one (see internal/config). Letting the client read the
// environment a second time would silently overrule that: `--address ""`
// would stop meaning what README.md says it means, and the bug would only
// show up for someone who had BAO_ADDR set.
//
// So the config is built with DisableEnvironment set and every field
// populated from config.Config. BPE's resolution is the only resolution.
func New(cfg config.Config) (*Client, error) {
	return newClientWithRetries(cfg, defaultMaxRetries)
}

// newClientWithRetries is New with the retry count injectable, so tests
// that provoke a 5xx do not each sit through the backoff.
func newClientWithRetries(cfg config.Config, maxRetries int) (*Client, error) {
	apiConfig := openbao.NewConfig()
	apiConfig.DisableEnvironment = true
	apiConfig.Address = cfg.Address
	apiConfig.MaxRetries = maxRetries

	tlsConfig := &openbao.TLSConfig{
		CACert:        cfg.CACert,
		CAPath:        cfg.CAPath,
		ClientCert:    cfg.ClientCert,
		ClientKey:     cfg.ClientKey,
		TLSServerName: cfg.TLSServerName,
		Insecure:      cfg.SkipVerify,
	}
	if err := apiConfig.ConfigureTLS(tlsConfig); err != nil {
		return nil, apperr.Wrap(apperr.ExitUsage, "the TLS configuration could not be applied", err)
	}
	if apiConfig.Error != nil {
		return nil, apperr.Wrap(apperr.ExitUsage, "the OpenBao client configuration is invalid", apiConfig.Error)
	}

	apiClient, err := openbao.NewClient(apiConfig)
	if err != nil {
		return nil, apperr.Wrap(apperr.ExitUsage, "the OpenBao client could not be created", err)
	}

	token := cfg.Token.Reveal()
	apiClient.SetToken(token)
	if cfg.Namespace != "" {
		apiClient.SetNamespace(cfg.Namespace)
	}

	client := &Client{api: apiClient, token: token}
	client.warnings = securityWarnings(cfg)
	return client, nil
}

// SecurityWarnings returns anything about this client's configuration the
// user should be told before it is used.
//
// They are returned rather than logged so the caller decides how to show
// them — the CLI prints them to stderr, and the terminal editor (FSM-17)
// shows them where they cannot be scrolled past. Disabling certificate
// verification is the kind of thing that gets turned on to get through an
// afternoon and left on for a year, so it says plainly what it exposes
// rather than noting that verification is off.
func (c *Client) SecurityWarnings() []string { return c.warnings }

func securityWarnings(cfg config.Config) []string {
	var warnings []string
	if cfg.SkipVerify {
		warnings = append(warnings,
			"TLS certificate verification is DISABLED for this connection "+
				"(--skip-verify / BAO_SKIP_VERIFY). The server's identity is not being checked, "+
				"so this connection can be intercepted and both the token and the policies sent "+
				"over it read by whoever intercepts it. Use this only against a server you "+
				"control on a network you trust.")
	}
	if cfg.Address == "" {
		warnings = append(warnings,
			"No OpenBao address is configured (--address / BAO_ADDR); "+
				"remote operations will not reach a server.")
	}
	return warnings
}

// Address returns the server address this client is configured for, for
// display. It contains no credential.
func (c *Client) Address() string { return c.api.Address() }

// String renders the client without its token, so a client embedded in a
// formatted message or a debug print cannot leak one. Format is
// implemented alongside it because fmt consults Formatter ahead of
// Stringer, and a %+v or %#v of this struct would otherwise fall through
// to the reflection printer and render the token field verbatim — the
// same trap config.SensitiveString documents.
func (c *Client) String() string {
	if c == nil {
		return "<nil *baoclient.Client>"
	}
	return fmt.Sprintf("baoclient.Client{address: %q, token: <redacted>}", c.api.Address())
}

// Format implements fmt.Formatter for every verb, so no formatting path
// reaches the raw struct.
func (c *Client) Format(f fmt.State, verb rune) {
	switch verb {
	case 'v', 's', 'q':
		fmt.Fprint(f, c.String())
	default:
		fmt.Fprintf(f, "%%!%c(baoclient.Client=%s)", verb, c.String())
	}
}

// tlsClientConfig exposes the TLS settings actually applied to the
// transport, so a test can assert what verification setting is really in
// force rather than that the field it was built from was set. Unexported
// and test-only by intent.
func (c *Client) tlsClientConfig() *tls.Config {
	httpClient := c.api.CloneConfig().HttpClient
	if httpClient == nil {
		return nil
	}
	transport, ok := httpClient.Transport.(*http.Transport)
	if !ok || transport.TLSClientConfig == nil {
		return nil
	}
	return transport.TLSClientConfig.Clone()
}
