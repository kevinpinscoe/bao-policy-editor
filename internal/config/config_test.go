package config

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/kevinpinscoe/bao-policy-editor/internal/apperr"
)

// envLookup builds an EnvLookup from a plain map, matching os.LookupEnv's
// (value, present) contract so tests can express "unset" distinctly from
// "set to empty string".
func envLookup(env map[string]string) EnvLookup {
	return func(key string) (string, bool) {
		v, ok := env[key]
		return v, ok
	}
}

func strptr(s string) *string { return &s }

func TestResolve_Precedence_FlagOverBaoOverVault(t *testing.T) {
	flags := Flags{Address: strptr("flag-address")}
	env := envLookup(map[string]string{
		"BAO_ADDR":   "bao-address",
		"VAULT_ADDR": "vault-address",
	})

	cfg, err := Resolve(flags, env)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if cfg.Address != "flag-address" {
		t.Errorf("Address = %q, want %q (flag must win over both env vars)", cfg.Address, "flag-address")
	}
}

func TestResolve_Precedence_BaoOverVault(t *testing.T) {
	env := envLookup(map[string]string{
		"BAO_ADDR":   "bao-address",
		"VAULT_ADDR": "vault-address",
	})

	cfg, err := Resolve(Flags{}, env)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if cfg.Address != "bao-address" {
		t.Errorf("Address = %q, want %q (BAO_* must win over VAULT_*)", cfg.Address, "bao-address")
	}
}

func TestResolve_Precedence_VaultFallback(t *testing.T) {
	env := envLookup(map[string]string{
		"VAULT_ADDR": "vault-address",
	})

	cfg, err := Resolve(Flags{}, env)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if cfg.Address != "vault-address" {
		t.Errorf("Address = %q, want %q (VAULT_* must be used when BAO_* is absent)", cfg.Address, "vault-address")
	}
}

func TestResolve_Precedence_SafeDefault(t *testing.T) {
	cfg, err := Resolve(Flags{}, envLookup(nil))
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if cfg.Address != "" {
		t.Errorf("Address = %q, want empty default", cfg.Address)
	}
	if cfg.SkipVerify != false {
		t.Errorf("SkipVerify = %v, want false default", cfg.SkipVerify)
	}
	if cfg.Token.IsSet() {
		t.Error("Token.IsSet() = true, want false when nothing configures it")
	}
}

func TestResolve_EmptyValueBehavior(t *testing.T) {
	t.Run("explicit empty env value wins over VAULT_* fallback", func(t *testing.T) {
		env := envLookup(map[string]string{
			"BAO_ADDR":   "", // present but empty
			"VAULT_ADDR": "vault-address",
		})
		cfg, err := Resolve(Flags{}, env)
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if cfg.Address != "" {
			t.Errorf("Address = %q, want empty — a present-but-empty BAO_ADDR must not fall through to VAULT_ADDR", cfg.Address)
		}
	})

	t.Run("explicit empty flag wins over env entirely", func(t *testing.T) {
		env := envLookup(map[string]string{"BAO_ADDR": "bao-address"})
		cfg, err := Resolve(Flags{Address: strptr("")}, env)
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if cfg.Address != "" {
			t.Errorf("Address = %q, want empty — an explicit --address='' must override BAO_ADDR", cfg.Address)
		}
	})

	t.Run("unset flag and unset env fall through to default", func(t *testing.T) {
		cfg, err := Resolve(Flags{}, envLookup(nil))
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if cfg.Namespace != "" {
			t.Errorf("Namespace = %q, want empty default", cfg.Namespace)
		}
	})
}

func TestResolve_BooleanParsing(t *testing.T) {
	cases := []struct {
		name    string
		flags   Flags
		env     map[string]string
		want    bool
		wantErr bool
	}{
		{name: "flag true", flags: Flags{SkipVerify: strptr("true")}, want: true},
		{name: "flag false", flags: Flags{SkipVerify: strptr("false")}, want: false},
		{name: "flag 1", flags: Flags{SkipVerify: strptr("1")}, want: true},
		{name: "flag 0", flags: Flags{SkipVerify: strptr("0")}, want: false},
		{name: "BAO_SKIP_VERIFY true", env: map[string]string{"BAO_SKIP_VERIFY": "true"}, want: true},
		{name: "VAULT_SKIP_VERIFY true, no BAO_*", env: map[string]string{"VAULT_SKIP_VERIFY": "true"}, want: true},
		{name: "unset defaults false", want: false},
		{name: "flag malformed", flags: Flags{SkipVerify: strptr("sorta")}, wantErr: true},
		{name: "env malformed", env: map[string]string{"BAO_SKIP_VERIFY": "yes-please"}, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Resolve(tc.flags, envLookup(tc.env))
			if tc.wantErr {
				if err == nil {
					t.Fatal("Resolve() error = nil, want a boolean-parsing error")
				}
				var appErr *apperr.AppError
				if !errors.As(err, &appErr) {
					t.Fatalf("error is not an *apperr.AppError: %v", err)
				}
				if appErr.Code != apperr.ExitUsage {
					t.Errorf("error code = %v, want %v", appErr.Code, apperr.ExitUsage)
				}
				if !strings.Contains(err.Error(), "invalid boolean value") {
					t.Errorf("error message = %q, want it to clearly name a boolean parsing problem", err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if cfg.SkipVerify != tc.want {
				t.Errorf("SkipVerify = %v, want %v", cfg.SkipVerify, tc.want)
			}
		})
	}
}

func TestResolve_NamespaceAndTLSSettings(t *testing.T) {
	flags := Flags{
		Namespace:     strptr("team-a"),
		CACert:        strptr("/etc/bpe/ca.pem"),
		CAPath:        strptr("/etc/bpe/ca.d"),
		ClientCert:    strptr("/etc/bpe/client.pem"),
		ClientKey:     strptr("/etc/bpe/client-key.pem"),
		TLSServerName: strptr("bao.internal"),
	}

	cfg, err := Resolve(flags, envLookup(nil))
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if cfg.Namespace != "team-a" {
		t.Errorf("Namespace = %q", cfg.Namespace)
	}
	if cfg.CACert != "/etc/bpe/ca.pem" {
		t.Errorf("CACert = %q", cfg.CACert)
	}
	if cfg.CAPath != "/etc/bpe/ca.d" {
		t.Errorf("CAPath = %q", cfg.CAPath)
	}
	if cfg.ClientCert != "/etc/bpe/client.pem" {
		t.Errorf("ClientCert = %q", cfg.ClientCert)
	}
	if cfg.ClientKey != "/etc/bpe/client-key.pem" {
		t.Errorf("ClientKey = %q", cfg.ClientKey)
	}
	if cfg.TLSServerName != "bao.internal" {
		t.Errorf("TLSServerName = %q", cfg.TLSServerName)
	}

	// And via env, to confirm the same fields resolve from BAO_*/VAULT_* too.
	env := envLookup(map[string]string{
		"BAO_NAMESPACE":       "env-namespace",
		"VAULT_CACERT":        "/env/ca.pem",
		"BAO_TLS_SERVER_NAME": "env.bao.internal",
	})
	cfg2, err := Resolve(Flags{}, env)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if cfg2.Namespace != "env-namespace" {
		t.Errorf("Namespace (env) = %q", cfg2.Namespace)
	}
	if cfg2.CACert != "/env/ca.pem" {
		t.Errorf("CACert (VAULT_* fallback) = %q", cfg2.CACert)
	}
	if cfg2.TLSServerName != "env.bao.internal" {
		t.Errorf("TLSServerName (env) = %q", cfg2.TLSServerName)
	}
}

func TestResolve_TokenRedaction(t *testing.T) {
	flags := Flags{Token: strptr(rawSecret)}
	cfg, err := Resolve(flags, envLookup(nil))
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if cfg.Token.Reveal() != rawSecret {
		t.Fatalf("Token.Reveal() = %q, want %q", cfg.Token.Reveal(), rawSecret)
	}
	assertNoLeak(t, "resolved config %+v", fmt.Sprintf("%+v", cfg))
}
