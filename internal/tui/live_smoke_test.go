//go:build livesmoke

// Package tui's live smoke test — opt in, loopback only, never run by CI.
//
// Everything else in this repository's test suite runs against fakes and
// httptest, by design: no test needs a server, a network, or a credential.
// This file is the deliberate exception, and it exists because one thing
// cannot be answered by a fake.
//
// After a write, BPE re-reads the policy to learn the version the write
// produced — the endpoint answers a write with 204 and no body — and it
// adopts that version only if the policy read back is byte-for-byte what
// it just wrote. That guard is what stops another client's concurrent
// write being adopted as BPE's own version and silently overwritten
// (FSM-17, pull request 9). Its cost is that any server-side normalization
// of a stored policy body would make every second consecutive save in a
// session fail. Only a real OpenBao can say whether that happens, and only
// this test asks.
//
// Nothing here runs unless it is asked to, twice over: the `livesmoke`
// build tag means `go test ./...` never compiles it, and even compiled it
// skips unless BPE_LIVE_ADDR and BPE_LIVE_TOKEN are set. It then refuses
// outright any address that is not loopback.
//
// Run it through scripts/live-smoke.sh, which stands up the disposable
// server and enforces the rest of the safeguards — see RUNBOOK.md Step 14.
// Do not point it at a server whose data matters.
//
// Everything here drives the real editor against a real server: the model
// is the one `bpe --remote` runs, the store is *baoclient.Client, and the
// "other client" is raw HTTP that shares none of BPE's code.

package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/kevinpinscoe/bao-policy-editor/internal/baoclient"
	"github.com/kevinpinscoe/bao-policy-editor/internal/config"
)

// smokeBody is the seeded policy: three rules, so two of them can be
// removed in two consecutive saves without the document becoming empty
// and without any path being duplicated.
const smokeBody = `path "secret/data/smoke/one/*" {
  capabilities = ["read"]
}

path "secret/data/smoke/two/*" {
  capabilities = ["read", "list"]
}

path "secret/data/smoke/three/*" {
  capabilities = ["read"]
}
`

func liveEnv(t *testing.T) (addr, token string) {
	t.Helper()
	addr = os.Getenv("BPE_LIVE_ADDR")
	token = os.Getenv("BPE_LIVE_TOKEN")
	if addr == "" || token == "" {
		t.Skip("BPE_LIVE_ADDR / BPE_LIVE_TOKEN unset — this test only runs under the smoke harness")
	}
	// The address is parsed rather than pattern-matched — see loopbackOnly in
	// livesmoke_guard_test.go for why a prefix check is not good enough.
	if err := loopbackOnly(addr); err != nil {
		t.Fatalf("refusing to run: %v", err)
	}
	return addr, token
}

// --- the other client: raw HTTP, sharing none of BPE's code ---

func apiCall(t *testing.T, method, addr, token, path string, body map[string]any) (int, map[string]any) {
	t.Helper()
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		payload = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, addr+"/v1/"+path, payload)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Vault-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	decoded := map[string]any{}
	raw, _ := io.ReadAll(resp.Body)
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &decoded)
	}
	return resp.StatusCode, decoded
}

// seedPolicy writes a policy as somebody other than BPE, with whatever
// metadata the test wants on it.
func seedPolicy(t *testing.T, addr, token, name, body string, extra map[string]any) {
	t.Helper()
	payload := map[string]any{"policy": body}
	for k, v := range extra {
		payload[k] = v
	}
	status, resp := apiCall(t, http.MethodPost, addr, token, "sys/policies/acl/"+name, payload)
	if status >= 300 {
		t.Fatalf("seeding %s returned %d: %v", name, status, resp)
	}
	t.Cleanup(func() {
		apiCall(t, http.MethodDelete, addr, token, "sys/policies/acl/"+name, nil) //nolint:errcheck
	})
}

// serverState is what the server holds now, read without BPE.
func serverState(t *testing.T, addr, token, name string) map[string]any {
	t.Helper()
	status, resp := apiCall(t, http.MethodGet, addr, token, "sys/policies/acl/"+name, nil)
	if status != http.StatusOK {
		t.Fatalf("reading %s returned %d: %v", name, status, resp)
	}
	data, ok := resp["data"].(map[string]any)
	if !ok {
		t.Fatalf("reading %s returned no data: %v", name, resp)
	}
	return data
}

func serverVersion(t *testing.T, data map[string]any) int {
	t.Helper()
	raw, ok := data["version"].(float64)
	if !ok {
		t.Fatalf("the server reported no usable version: %v", data["version"])
	}
	return int(raw)
}

// --- the editor, wired to the real client ---

func liveModel(t *testing.T, addr, token string) *Model {
	t.Helper()
	cfg := config.Config{Address: addr, Token: config.SensitiveString(token)}
	client, err := baoclient.New(cfg)
	if err != nil {
		t.Fatalf("building the client returned an error: %v", err)
	}
	m := NewWithOptions(NewSession(), ModelOptions{
		Config:   cfg,
		NewStore: func(config.Config) (RemoteStore, error) { return client, nil },
	})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	deliver(t, m, m.enterRemote())
	if m.screen != screenBrowse {
		t.Fatalf("connecting did not reach the browser: screen = %d, problem = %q", m.screen, m.problem)
	}
	return m
}

func openLive(t *testing.T, addr, token, name string) *Model {
	t.Helper()
	m := liveModel(t, addr, token)
	selectPolicy(t, m, name)
	step(t, m, "enter")
	if !m.session.IsRemote() {
		t.Fatalf("opening %s produced no remote session; problem = %q", name, m.problem)
	}
	return m
}

// removeARuleAndSave is one full edit-and-write cycle through the screens a
// user actually passes through: remove a rule, confirm, review the diff,
// send it.
func removeARuleAndSave(t *testing.T, m *Model) {
	t.Helper()
	before := m.session.Document().RuleCount()
	step(t, m, "x", "y")
	if got := m.session.Document().RuleCount(); got != before-1 {
		t.Fatalf("rule count = %d, want %d after a removal", got, before-1)
	}
	step(t, m, "s")
	if m.screen != screenReview {
		t.Fatalf("s did not reach the review screen: screen = %d", m.screen)
	}
	step(t, m, "ctrl+s")
}

// --- the checks ---

// TestLiveReadUpdateAndASecondSaveWithoutReopening is the decisive one for
// the read-after-write guard added on 2026-09-18.
//
// The guard compares the policy read back after a write against the bytes
// BPE wrote, and adopts the new version only if they match. If a real
// OpenBao normalized the stored policy in any way, the comparison would
// fail against every real server and the *second* consecutive save in a
// session would be refused for lack of conflict protection. A fake cannot
// answer that question; this is the test that can.
func TestLiveReadUpdateAndASecondSaveWithoutReopening(t *testing.T) {
	addr, token := liveEnv(t)

	for _, tc := range []struct {
		name  string
		extra map[string]any
	}{
		{"bpe-smoke-plain", nil},
		{"bpe-smoke-expiration", map[string]any{"expiration": time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)}},
		{"bpe-smoke-ttl", map[string]any{"ttl": "720h"}},
		// OpenBao requires a check-and-set value on the very write that
		// turns cas_required on, so the seed sends cas = -1 — "this must
		// be a create", the same value BPE's own Create uses.
		{"bpe-smoke-cas-required", map[string]any{"cas_required": true, "cas": -1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seedPolicy(t, addr, token, tc.name, smokeBody, tc.extra)
			seeded := serverState(t, addr, token, tc.name)
			startVersion := serverVersion(t, seeded)

			m := openLive(t, addr, token, tc.name)

			// The read carried a usable revision.
			rev, exists, ok := m.session.RemoteRevision()
			if !ok || !exists || !rev.HasVersion {
				t.Fatalf("the read produced no usable revision: %+v exists=%v ok=%v", rev, exists, ok)
			}
			if rev.Version != startVersion {
				t.Errorf("revision version = %d, want the server's %d", rev.Version, startVersion)
			}
			if !rev.Metadata.Preservable() {
				t.Fatalf("a live server's own metadata was reported as unpreservable: %v", rev.Metadata.Unpreservable)
			}

			// First save.
			removeARuleAndSave(t, m)
			if m.session.Dirty() {
				t.Fatalf("the first save was refused: problem = %q", m.problem)
			}
			after := serverState(t, addr, token, tc.name)
			if v := serverVersion(t, after); v != startVersion+1 {
				t.Errorf("version = %d, want %d after one save", v, startVersion+1)
			}
			if body, _ := after["policy"].(string); body != string(m.session.Current()) {
				t.Errorf("the server did not store what BPE sent:\n--- server ---\n%s\n--- sent ---\n%s",
					body, m.session.Current())
			}

			// Second save, with no re-open in between. This is what the
			// read-back guard has to survive.
			removeARuleAndSave(t, m)
			if m.session.Dirty() {
				t.Fatalf("the second consecutive save was refused: problem = %q", m.problem)
			}
			final := serverState(t, addr, token, tc.name)
			if v := serverVersion(t, final); v != startVersion+2 {
				t.Errorf("version = %d, want %d after two saves", v, startVersion+2)
			}

			// Metadata survived both writes.
			for field, want := range tc.extra {
				if field == "cas" {
					// A seeding detail, not policy state to preserve.
					continue
				}
				if field == "ttl" {
					// The server stores a ttl as an absolute expiration.
					if _, present := final["expiration"]; !present {
						t.Error("the expiration derived from ttl was cleared by an update")
					}
					if _, present := final["ttl"]; present {
						t.Error("ttl came back on the policy; BPE must never send it")
					}
					continue
				}
				got, present := final[field]
				if !present {
					t.Errorf("%s was cleared by an update", field)
					continue
				}
				if field == "expiration" {
					// Rendered in the server's own form; compared as an
					// instant rather than as a string.
					gotAt, err1 := time.Parse(time.RFC3339, fmt.Sprint(got))
					wantAt, err2 := time.Parse(time.RFC3339, fmt.Sprint(want))
					if err1 != nil || err2 != nil {
						t.Errorf("expiration %v could not be compared with %v", got, want)
					} else if !gotAt.Equal(wantAt) {
						t.Errorf("expiration = %v, want the instant %v", gotAt, wantAt)
					}
					continue
				}
				if fmt.Sprint(got) != fmt.Sprint(want) {
					t.Errorf("%s = %v, want %v", field, got, want)
				}
			}
		})
	}
}

// TestLiveStaleSaveIsAConflictAndTheReviewedRetryLands drives the whole
// conflict resolution against a real server: another client writes, BPE's
// save is refused, the user looks at what changed, and only then retries.
func TestLiveStaleSaveIsAConflictAndTheReviewedRetryLands(t *testing.T) {
	addr, token := liveEnv(t)
	const name = "bpe-smoke-conflict"
	seedPolicy(t, addr, token, name, smokeBody, nil)

	m := openLive(t, addr, token, name)

	// Somebody else writes, after BPE has read.
	seedPolicy(t, addr, token, name, `path "secret/data/somebody/else/*" {
  capabilities = ["read", "list", "create"]
}
`, nil)

	// BPE's save is now stale.
	step(t, m, "x", "y", "s", "ctrl+s")
	if m.dialog != dialogRemoteConflict {
		t.Fatalf("a stale save did not raise the conflict dialog: dialog = %d, problem = %q", m.dialog, m.problem)
	}
	if !m.session.Dirty() {
		t.Error("the refused save cleared the modified state")
	}

	// Look at what changed — this writes nothing.
	step(t, m, "v")
	if m.screen != screenReview || m.reviewKind != reviewServerConflict {
		t.Fatalf("v did not show the server's copy: screen = %d kind = %d problem = %q",
			m.screen, m.reviewKind, m.problem)
	}
	if got := serverState(t, addr, token, name); !strings.Contains(got["policy"].(string), "somebody/else") {
		t.Error("looking at the server's copy changed the server")
	}

	// Retry from that screen, having seen it.
	step(t, m, "ctrl+s")
	if m.session.Dirty() {
		t.Fatalf("the reviewed retry was refused: problem = %q", m.problem)
	}
	final := serverState(t, addr, token, name)
	if body, _ := final["policy"].(string); body != string(m.session.Current()) {
		t.Errorf("the retry did not land BPE's version:\n--- server ---\n%s\n--- sent ---\n%s",
			body, m.session.Current())
	}
}

// TestLiveDeleteRemovesThePolicy exercises the delete confirmation against
// a real server, including the typed-name gate.
func TestLiveDeleteRemovesThePolicy(t *testing.T) {
	addr, token := liveEnv(t)
	const name = "bpe-smoke-delete"
	seedPolicy(t, addr, token, name, smokeBody, nil)

	m := liveModel(t, addr, token)
	selectPolicy(t, m, name)
	step(t, m, "x")
	typeInto(t, m, name)
	step(t, m, "enter")

	status, _ := apiCall(t, http.MethodGet, addr, token, "sys/policies/acl/"+name, nil)
	if status != http.StatusNotFound {
		t.Errorf("reading the deleted policy returned %d, want 404", status)
	}
}
