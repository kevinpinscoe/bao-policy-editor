package baoclient

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kevinpinscoe/bao-policy-editor/internal/apperr"
	"github.com/kevinpinscoe/bao-policy-editor/internal/config"
)

// testToken is an obviously synthetic value. No test in this package uses,
// reads, or requires a real credential, and none contacts a real server.
const testToken = "test-token-not-a-real-credential"

// recordedRequest is what the fake server saw.
type recordedRequest struct {
	Method      string
	Path        string
	ContentType string
	Token       string
	Body        map[string]any
}

// fakeBao is an httptest server that records what BPE sent and replies
// with whatever the test told it to.
type fakeBao struct {
	t        *testing.T
	server   *httptest.Server
	mu       sync.Mutex
	requests []recordedRequest
	handler  http.HandlerFunc
}

func newFakeBao(t *testing.T, handler http.HandlerFunc) *fakeBao {
	t.Helper()
	f := &fakeBao{t: t, handler: handler}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func newFakeBaoTLS(t *testing.T, handler http.HandlerFunc) *fakeBao {
	t.Helper()
	f := &fakeBao{t: t, handler: handler}
	f.server = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeBao) serve(w http.ResponseWriter, r *http.Request) {
	rec := recordedRequest{
		Method:      r.Method,
		Path:        r.URL.Path,
		ContentType: r.Header.Get("Content-Type"),
		Token:       r.Header.Get("X-Vault-Token"),
	}
	if raw, err := io.ReadAll(r.Body); err == nil && len(raw) > 0 {
		decoded := map[string]any{}
		if err := json.Unmarshal(raw, &decoded); err == nil {
			rec.Body = decoded
		}
	}

	f.mu.Lock()
	f.requests = append(f.requests, rec)
	f.mu.Unlock()

	f.handler(w, r)
}

func (f *fakeBao) recorded() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedRequest(nil), f.requests...)
}

func (f *fakeBao) only(t *testing.T) recordedRequest {
	t.Helper()
	got := f.recorded()
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 request, got %d: %+v", len(got), got)
	}
	return got[0]
}

// client builds a Client pointed at the fake server, with retries off.
//
// Retries are disabled for all but the one test that is about them
// (TestAServerErrorIsRetried). A 5xx otherwise costs every test that
// provokes one the client's full exponential backoff, which turned this
// package's suite from under a second into forty.
func (f *fakeBao) client(t *testing.T) *Client {
	t.Helper()
	return newTestClient(t, config.Config{
		Address: f.server.URL,
		Token:   config.SensitiveString(testToken),
	}, 0)
}

func newClient(t *testing.T, cfg config.Config) *Client {
	t.Helper()
	return newTestClient(t, cfg, 0)
}

func newTestClient(t *testing.T, cfg config.Config, retries int) *Client {
	t.Helper()
	c, err := newClientWithRetries(cfg, retries)
	if err != nil {
		t.Fatalf("building the client returned an error: %v", err)
	}
	return c
}

// TestAServerErrorIsRetried covers the retry policy New actually ships
// with, which the rest of the suite turns off for speed.
func TestAServerErrorIsRetried(t *testing.T) {
	var attempts int
	fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts <= defaultMaxRetries {
			writeJSON(w, http.StatusInternalServerError, errorBody("temporarily unavailable"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{"policy": "path \"a\" {}\n", "version": 1},
		})
	})

	client := newTestClient(t, config.Config{
		Address: fake.server.URL,
		Token:   config.SensitiveString(testToken),
	}, defaultMaxRetries)

	if _, err := client.Read(context.Background(), "deploy"); err != nil {
		t.Fatalf("Read returned an error despite the server recovering: %v", err)
	}
	if attempts != defaultMaxRetries+1 {
		t.Errorf("the server saw %d attempt(s), want %d", attempts, defaultMaxRetries+1)
	}
}

// TestRetriesAreNotUsedForAClientError confirms a 4xx is answered at once
// rather than retried — a rejected token or a stale check-and-set is not
// going to succeed on a second attempt, and retrying a write that the
// server may already have applied is worse than reporting it.
func TestRetriesAreNotUsedForAClientError(t *testing.T) {
	var attempts int
	fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		writeJSON(w, http.StatusForbidden, errorBody("permission denied"))
	})

	client := newTestClient(t, config.Config{
		Address: fake.server.URL,
		Token:   config.SensitiveString(testToken),
	}, defaultMaxRetries)

	if _, err := client.Read(context.Background(), "deploy"); err == nil {
		t.Fatal("expected an error")
	}
	if attempts != 1 {
		t.Errorf("a 403 was attempted %d times, want 1", attempts)
	}
}

// writeJSON replies with a JSON body, as OpenBao would.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// errorBody is OpenBao's error response shape.
func errorBody(messages ...string) map[string]any {
	return map[string]any{"errors": messages}
}

func TestReadCapturesRevisionMetadataAndWarnings(t *testing.T) {
	const body = `path "secret/data/a" {
  capabilities = ["read"]
}
`
	fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{
				"name":         "deploy",
				"policy":       body,
				"version":      3,
				"modified":     "2025-03-25T16:50:49.348095648-05:00",
				"cas_required": true,
			},
			"warnings": []string{"this policy will expire soon"},
		})
	})

	policy, err := fake.client(t).Read(context.Background(), "deploy")
	if err != nil {
		t.Fatalf("Read returned an error: %v", err)
	}

	if policy.Body != body {
		t.Errorf("policy body = %q", policy.Body)
	}
	if !policy.Revision.HasVersion || policy.Revision.Version != 3 {
		t.Errorf("revision = %+v, want version 3", policy.Revision)
	}
	if policy.Revision.Metadata.CASRequired == nil || !*policy.Revision.Metadata.CASRequired {
		t.Error("cas_required was not carried through")
	}
	if policy.Revision.Modified.IsZero() {
		t.Error("modified was not parsed")
	}
	if policy.Revision.ContentHash == "" {
		t.Error("no content hash was recorded")
	}
	if len(policy.Warnings) != 1 || policy.Warnings[0] != "this policy will expire soon" {
		t.Errorf("warnings = %v, want the server's warning carried through", policy.Warnings)
	}

	req := fake.only(t)
	if req.Method != http.MethodGet || req.Path != "/v1/sys/policies/acl/deploy" {
		t.Errorf("request = %s %s", req.Method, req.Path)
	}
}

func TestReadAcceptsAVersionOfZero(t *testing.T) {
	// Policies created before versioning start at 0, so 0 is a real
	// version. Treating it as "missing" would make every such policy
	// un-updatable.
	fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{"policy": "path \"a\" {}\n", "version": 0},
		})
	})

	policy, err := fake.client(t).Read(context.Background(), "old")
	if err != nil {
		t.Fatalf("Read returned an error: %v", err)
	}
	if !policy.Revision.HasVersion {
		t.Error("a version of 0 was treated as missing")
	}
	if policy.Revision.Version != 0 {
		t.Errorf("version = %d, want 0", policy.Revision.Version)
	}
}

func TestUpdateSendsPostCarryingThePreviouslyReadVersion(t *testing.T) {
	// POST, not PATCH. OpenBao 2.5.2's sys/policies/acl answers 405
	// "unsupported operation" to PATCH — measured against a live instance
	// on 2026-09-18 — so the verb BPE sent until then could never succeed.
	fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"version": 8}})
	})

	rev := Revision{Version: 7, HasVersion: true}
	if _, err := fake.client(t).Update(context.Background(), "deploy", "path \"a\" {}\n", rev); err != nil {
		t.Fatalf("Update returned an error: %v", err)
	}

	req := fake.only(t)
	if req.Method != http.MethodPost {
		t.Errorf("method = %s, want POST — this endpoint does not implement PATCH", req.Method)
	}
	if req.Method == http.MethodPatch {
		t.Error("PATCH is not implemented by this endpoint and must never be sent")
	}
	if req.Path != "/v1/sys/policies/acl/deploy" {
		t.Errorf("path = %s", req.Path)
	}
	if got := jsonNumber(t, req.Body["cas"]); got != 7 {
		t.Errorf("cas = %v, want the version read earlier (7)", req.Body["cas"])
	}
	if req.Body["policy"] != "path \"a\" {}\n" {
		t.Errorf("policy = %v, want the body being saved", req.Body["policy"])
	}
}

func TestUpdatePreservesTheMetadataTheReadReported(t *testing.T) {
	// POST resets every field it is not sent, so the writable fields a
	// read reported have to be handed back or the update destroys them.
	// Measured against OpenBao 2.5.2: a POST carrying only policy and cas
	// cleared both expiration and cas_required.
	expiration := "2030-01-01T00:00:00Z"
	casRequired := true
	wildcards := true
	slashes := false

	fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"version": 9}})
	})

	rev := Revision{Version: 8, HasVersion: true, Metadata: Metadata{
		Expiration:                        &expiration,
		CASRequired:                       &casRequired,
		AllowWildcardsInIdentityTemplates: &wildcards,
		AllowSlashesInIdentityTemplates:   &slashes,
	}}
	if _, err := fake.client(t).Update(context.Background(), "deploy", "path \"a\" {}\n", rev); err != nil {
		t.Fatalf("Update returned an error: %v", err)
	}

	body := fake.only(t).Body
	for field, want := range map[string]any{
		"expiration":                            expiration,
		"cas_required":                          true,
		"allow_wildcards_in_identity_templates": true,
		"allow_slashes_in_identity_templates":   false,
	} {
		got, present := body[field]
		if !present {
			t.Errorf("%s was not sent back; POST would clear it", field)
			continue
		}
		if got != want {
			t.Errorf("%s = %v, want %v exactly as the server reported it", field, got, want)
		}
	}
}

func TestUpdateOmitsMetadataTheServerDidNotReport(t *testing.T) {
	// A field the server never mentioned is not invented. Sending a zero
	// value would *set* it — turning cas_required on for a policy that
	// never had it, or giving an unexpiring policy an expiry.
	fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"version": 3}})
	})

	rev := Revision{Version: 2, HasVersion: true}
	if _, err := fake.client(t).Update(context.Background(), "deploy", "path \"a\" {}\n", rev); err != nil {
		t.Fatalf("Update returned an error: %v", err)
	}

	body := fake.only(t).Body
	if len(body) != 2 {
		t.Errorf("body carried %d fields, want exactly policy and cas: %+v", len(body), body)
	}
	for _, unwanted := range []string{"expiration", "cas_required",
		"allow_wildcards_in_identity_templates", "allow_slashes_in_identity_templates"} {
		if _, present := body[unwanted]; present {
			t.Errorf("body included %q, which the read never reported", unwanted)
		}
	}
}

func TestUpdateDistinguishesAbsentFromFalse(t *testing.T) {
	// The reason Metadata's fields are pointers. An explicitly false
	// cas_required and an absent one are different server states, and
	// collapsing them means a POST that silently changes one of them.
	no := false

	for _, tc := range []struct {
		name     string
		metadata Metadata
		wantSent bool
		wantVal  any
	}{
		{"explicitly false is sent as false", Metadata{CASRequired: &no}, true, false},
		{"absent is not sent at all", Metadata{}, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"version": 2}})
			})
			rev := Revision{Version: 1, HasVersion: true, Metadata: tc.metadata}
			if _, err := fake.client(t).Update(context.Background(), "p", "path \"a\" {}\n", rev); err != nil {
				t.Fatalf("Update returned an error: %v", err)
			}
			got, present := fake.only(t).Body["cas_required"]
			if present != tc.wantSent {
				t.Fatalf("cas_required present = %v, want %v", present, tc.wantSent)
			}
			if present && got != tc.wantVal {
				t.Errorf("cas_required = %v, want %v", got, tc.wantVal)
			}
		})
	}
}

func TestUpdateNeverSendsTtl(t *testing.T) {
	// The server stores ttl as an absolute expiration. Replaying the
	// original relative value on every update would push the expiry
	// further out each time the policy was edited, so a policy meant to
	// lapse would quietly become permanent.
	expiration := "2026-10-18T15:30:50.805710913-04:00"
	fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"version": 4}})
	})

	// A read of a ttl-created policy reports expiration, never ttl — so a
	// ttl can only reach an update by being invented.
	rev := Revision{Version: 3, HasVersion: true, Metadata: Metadata{Expiration: &expiration}}
	if _, err := fake.client(t).Update(context.Background(), "p", "path \"a\" {}\n", rev); err != nil {
		t.Fatalf("Update returned an error: %v", err)
	}

	body := fake.only(t).Body
	if _, present := body["ttl"]; present {
		t.Error("an update sent ttl; it must preserve the absolute expiration instead")
	}
	if body["expiration"] != expiration {
		t.Errorf("expiration = %v, want the server's own value %q unreformatted",
			body["expiration"], expiration)
	}
}

func TestUpdateSendsNoEnvelopeFields(t *testing.T) {
	// name, version, modified and warnings describe the policy rather than
	// configure it. Echoing them back is at best ignored and at worst
	// rejected.
	expiration := "2030-01-01T00:00:00Z"
	casRequired := true
	fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"version": 6}})
	})

	rev := Revision{
		Version: 5, HasVersion: true,
		Modified: time.Now(),
		Metadata: Metadata{Expiration: &expiration, CASRequired: &casRequired},
	}
	if _, err := fake.client(t).Update(context.Background(), "p", "path \"a\" {}\n", rev); err != nil {
		t.Fatalf("Update returned an error: %v", err)
	}

	body := fake.only(t).Body
	for _, envelope := range []string{"name", "version", "modified", "warnings"} {
		if _, present := body[envelope]; present {
			t.Errorf("body included the envelope field %q", envelope)
		}
	}
}

func TestMetadataRoundTripsFromAReadIntoAnUpdate(t *testing.T) {
	// The whole point, end to end: what a read reports is what an update
	// puts back, without the caller having to know the field names.
	var requests int
	fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
				"name":                                  "deploy",
				"policy":                                "path \"a\" {}\n",
				"version":                               11,
				"cas_required":                          true,
				"expiration":                            "2031-06-01T00:00:00Z",
				"allow_wildcards_in_identity_templates": true,
			}})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"version": 12}})
	})

	client := fake.client(t)
	policy, err := client.Read(context.Background(), "deploy")
	if err != nil {
		t.Fatalf("Read returned an error: %v", err)
	}
	if _, err := client.Update(context.Background(), "deploy", "path \"b\" {}\n", policy.Revision); err != nil {
		t.Fatalf("Update returned an error: %v", err)
	}

	body := fake.requests[len(fake.requests)-1].Body
	if body["cas_required"] != true {
		t.Errorf("cas_required = %v, want true carried from the read", body["cas_required"])
	}
	if body["expiration"] != "2031-06-01T00:00:00Z" {
		t.Errorf("expiration = %v, want the read's value", body["expiration"])
	}
	if body["allow_wildcards_in_identity_templates"] != true {
		t.Errorf("wildcard flag = %v, want true", body["allow_wildcards_in_identity_templates"])
	}
	if _, present := body["allow_slashes_in_identity_templates"]; present {
		t.Error("the slashes flag was sent although the read never reported it")
	}
	if got := jsonNumber(t, body["cas"]); got != 11 {
		t.Errorf("cas = %v, want the version the read reported", body["cas"])
	}
}

func TestUpdateRefusesWithoutVersionMetadata(t *testing.T) {
	var reached bool
	fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
		reached = true
		writeJSON(w, http.StatusOK, map[string]any{})
	})

	_, err := fake.client(t).Update(context.Background(), "deploy", "path \"a\" {}\n", Revision{})

	if !errors.Is(err, ErrConflictProtectionUnsupported) {
		t.Fatalf("error = %v, want ErrConflictProtectionUnsupported", err)
	}
	if reached {
		t.Error("the update was sent anyway; it must not reach the server without conflict protection")
	}
	if strings.Contains(strings.ToLower(err.Error()), "read-before-write") {
		t.Error("the error suggests a non-atomic fallback")
	}
}

func TestReadWithMissingOrMalformedVersionFailsSafe(t *testing.T) {
	tests := []struct {
		name string
		data map[string]any
	}{
		{"no version field", map[string]any{"policy": "path \"a\" {}\n"}},
		{"version is a string", map[string]any{"policy": "path \"a\" {}\n", "version": "three"}},
		{"version is null", map[string]any{"policy": "path \"a\" {}\n", "version": nil}},
		{"version is an object", map[string]any{"policy": "path \"a\" {}\n", "version": map[string]any{}}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, http.StatusOK, map[string]any{"data": tc.data})
			})
			client := fake.client(t)

			// The read itself succeeds — the policy body is still usable,
			// and refusing to show it would be worse than saying it cannot
			// be updated safely.
			policy, err := client.Read(context.Background(), "deploy")
			if err != nil {
				t.Fatalf("Read returned an error: %v", err)
			}
			if policy.Revision.HasVersion {
				t.Fatalf("unusable version metadata was accepted: %+v", policy.Revision)
			}

			// The update built from it is what refuses.
			if _, err := client.Update(context.Background(), "deploy", policy.Body, policy.Revision); !errors.Is(err, ErrConflictProtectionUnsupported) {
				t.Errorf("Update error = %v, want ErrConflictProtectionUnsupported", err)
			}
		})
	}
}

func TestReadRejectsAResponseWithNoPolicyBody(t *testing.T) {
	fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"version": 1}})
	})

	if _, err := fake.client(t).Read(context.Background(), "deploy"); err == nil {
		t.Error("a response with no policy body was accepted")
	}
}

func TestCreateSendsPostWithCasMinusOne(t *testing.T) {
	fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"version": 1}})
	})

	result, err := fake.client(t).Create(context.Background(), "fresh", "path \"a\" {}\n")
	if err != nil {
		t.Fatalf("Create returned an error: %v", err)
	}
	if !result.Revision.HasVersion || result.Revision.Version != 1 {
		t.Errorf("write result revision = %+v, want version 1", result.Revision)
	}

	req := fake.only(t)
	if req.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", req.Method)
	}
	if got := jsonNumber(t, req.Body["cas"]); got != -1 {
		t.Errorf("cas = %v, want -1 (create-only)", req.Body["cas"])
	}
}

func TestCreateOnAnExistingPolicyIsAConflict(t *testing.T) {
	fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusBadRequest,
			errorBody("check-and-set parameter did not match the current version"))
	})

	_, err := fake.client(t).Create(context.Background(), "taken", "path \"a\" {}\n")

	if !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
	if code := apperr.CodeOf(err); code != apperr.ExitConflict {
		t.Errorf("exit code = %d, want %d", code, apperr.ExitConflict)
	}
}

func TestStaleCasMapsToConflictAndExitCodeFour(t *testing.T) {
	// OpenBao's documentation does not state the status code or body for a
	// failed check-and-set, so the detection is deliberately broad. Each
	// of these shapes must be recognized.
	tests := []struct {
		name   string
		status int
		body   any
	}{
		// The exact string a live OpenBao 2.5.2 returned on 2026-09-18,
		// captured during the FSM-17 smoke test. This is the one shape
		// that is measured rather than anticipated.
		{"400, verbatim from a live OpenBao 2.5.2", http.StatusBadRequest,
			errorBody("check-and-set parameter did not match the current version")},
		{"400 with the canonical message", http.StatusBadRequest,
			errorBody("check-and-set parameter did not match the current version")},
		{"400 with a cas mismatch phrasing", http.StatusBadRequest,
			errorBody("invalid cas value")},
		{"400 naming the cas parameter", http.StatusBadRequest,
			errorBody("cas parameter is required for this policy")},
		{"409 conflict", http.StatusConflict, errorBody("conflict")},
		{"412 precondition failed", http.StatusPreconditionFailed, errorBody("precondition failed")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, tc.status, tc.body)
			})

			rev := Revision{Version: 1, HasVersion: true}
			_, err := fake.client(t).Update(context.Background(), "deploy", "path \"a\" {}\n", rev)

			if !errors.Is(err, ErrConflict) {
				t.Fatalf("error = %v, want ErrConflict", err)
			}
			if code := apperr.CodeOf(err); code != apperr.ExitConflict {
				t.Errorf("exit code = %d, want %d", code, apperr.ExitConflict)
			}
		})
	}
}

func TestAnOrdinaryBadRequestIsNotTreatedAsAConflict(t *testing.T) {
	fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusBadRequest, errorBody("policy contains an unknown capability"))
	})

	rev := Revision{Version: 1, HasVersion: true}
	_, err := fake.client(t).Update(context.Background(), "deploy", "path \"a\" {}\n", rev)

	if errors.Is(err, ErrConflict) {
		t.Errorf("an unrelated 400 was reported as a conflict: %v", err)
	}
	if code := apperr.CodeOf(err); code != apperr.ExitOperational {
		t.Errorf("exit code = %d, want %d", code, apperr.ExitOperational)
	}
}

func TestListReturnsNamesAndWarnings(t *testing.T) {
	fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"data":     map[string]any{"keys": []string{"default", "deploy"}},
			"warnings": []string{"some policies were filtered"},
		})
	})

	list, err := fake.client(t).List(context.Background())
	if err != nil {
		t.Fatalf("List returned an error: %v", err)
	}
	if len(list.Names) != 2 || list.Names[0] != "default" || list.Names[1] != "deploy" {
		t.Errorf("names = %v", list.Names)
	}
	if len(list.Warnings) != 1 {
		t.Errorf("warnings = %v", list.Warnings)
	}
}

func TestListOfAnEmptyInstanceIsNotAnError(t *testing.T) {
	fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	list, err := fake.client(t).List(context.Background())
	if err != nil {
		t.Fatalf("List returned an error for an empty instance: %v", err)
	}
	if len(list.Names) != 0 {
		t.Errorf("names = %v, want none", list.Names)
	}
}

func TestReadOfAMissingPolicyIsTyped(t *testing.T) {
	fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	_, err := fake.client(t).Read(context.Background(), "nope")
	if !errors.Is(err, ErrPolicyNotFound) {
		t.Fatalf("error = %v, want ErrPolicyNotFound", err)
	}
	if errors.Is(err, ErrUnauthorized) {
		t.Error("a missing policy was also reported as an authorization failure")
	}
}

func TestDeleteSendsDelete(t *testing.T) {
	fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	if err := fake.client(t).Delete(context.Background(), "deploy"); err != nil {
		t.Fatalf("Delete returned an error: %v", err)
	}

	req := fake.only(t)
	if req.Method != http.MethodDelete || req.Path != "/v1/sys/policies/acl/deploy" {
		t.Errorf("request = %s %s", req.Method, req.Path)
	}
	if _, present := req.Body["cas"]; present {
		t.Error("a cas was sent on delete; this endpoint has no check-and-set for deletion")
	}
}

func TestAuthorizationFailuresAreDistinguished(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		wantDetail string
	}{
		{"rejected token", http.StatusUnauthorized, "rejected"},
		{"insufficient capability", http.StatusForbidden, "capability"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, tc.status, errorBody("permission denied"))
			})

			_, err := fake.client(t).Read(context.Background(), "deploy")
			if !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("error = %v, want ErrUnauthorized", err)
			}
			if !strings.Contains(err.Error(), tc.wantDetail) {
				t.Errorf("message = %q, want it to mention %q", err.Error(), tc.wantDetail)
			}
			if code := apperr.CodeOf(err); code != apperr.ExitOperational {
				t.Errorf("exit code = %d, want %d", code, apperr.ExitOperational)
			}
		})
	}
}

func TestUntrustedTLSIsReportedAsATLSFailure(t *testing.T) {
	fake := newFakeBaoTLS(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{})
	})

	// Verification on, no CA for the server's self-signed certificate.
	client := newClient(t, config.Config{
		Address: fake.server.URL,
		Token:   config.SensitiveString(testToken),
	})

	_, err := client.Read(context.Background(), "deploy")
	if !errors.Is(err, ErrTLS) {
		t.Fatalf("error = %v, want ErrTLS", err)
	}
	if !strings.Contains(err.Error(), "TLS") {
		t.Errorf("message = %q, want it to name TLS", err.Error())
	}
}

func TestSkipVerifyReachesAnUntrustedServerAndWarnsLoudly(t *testing.T) {
	fake := newFakeBaoTLS(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{"policy": "path \"a\" {}\n", "version": 1},
		})
	})

	client := newClient(t, config.Config{
		Address:    fake.server.URL,
		Token:      config.SensitiveString(testToken),
		SkipVerify: true,
	})

	if _, err := client.Read(context.Background(), "deploy"); err != nil {
		t.Fatalf("Read with --skip-verify returned an error: %v", err)
	}

	warnings := client.SecurityWarnings()
	if len(warnings) == 0 {
		t.Fatal("disabling certificate verification produced no warning")
	}
	joined := strings.ToLower(strings.Join(warnings, " "))
	for _, want := range []string{"disabled", "intercept"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warning does not mention %q: %s", want, joined)
		}
	}
}

func TestVerificationIsOnByDefault(t *testing.T) {
	client := newClient(t, config.Config{
		Address: "https://bao.example.invalid:8200",
		Token:   config.SensitiveString(testToken),
	})

	if len(client.SecurityWarnings()) != 0 {
		t.Errorf("a default configuration produced warnings: %v", client.SecurityWarnings())
	}

	// Asserted against the transport that will actually be used, not
	// against the field it was built from — and required to be present, so
	// this cannot quietly become a check that never runs.
	tlsCfg := client.tlsClientConfig()
	if tlsCfg == nil {
		t.Fatal("the client has no TLS configuration to check")
	}
	if tlsCfg.InsecureSkipVerify {
		t.Error("certificate verification is disabled by default")
	}
	if tlsCfg.MinVersion < tls.VersionTLS12 {
		t.Errorf("minimum TLS version = %#x, want TLS 1.2 or better", tlsCfg.MinVersion)
	}
}

func TestSkipVerifyReachesTheTransport(t *testing.T) {
	// The warning is not the safeguard; what matters is whether
	// verification was really turned off where the connection is made.
	client := newClient(t, config.Config{
		Address:    "https://bao.example.invalid:8200",
		Token:      config.SensitiveString(testToken),
		SkipVerify: true,
	})

	tlsCfg := client.tlsClientConfig()
	if tlsCfg == nil {
		t.Fatal("the client has no TLS configuration to check")
	}
	if !tlsCfg.InsecureSkipVerify {
		t.Error("--skip-verify was accepted and warned about but never applied to the transport")
	}
}

func TestCancellationIsReportedAsInterrupted(t *testing.T) {
	released := make(chan struct{})
	fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
		<-released
		writeJSON(w, http.StatusOK, map[string]any{})
	})
	t.Cleanup(func() { close(released) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := fake.client(t).Read(ctx, "deploy")
	if code := apperr.CodeOf(err); code != apperr.ExitInterrupted {
		t.Errorf("exit code = %d, want %d (%v)", code, apperr.ExitInterrupted, err)
	}
}

func TestTimeoutIsNotReportedAsCancellation(t *testing.T) {
	released := make(chan struct{})
	fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
		<-released
		writeJSON(w, http.StatusOK, map[string]any{})
	})
	t.Cleanup(func() { close(released) })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := fake.client(t).Read(ctx, "deploy")
	if code := apperr.CodeOf(err); code != apperr.ExitOperational {
		t.Errorf("exit code = %d, want %d (%v)", code, apperr.ExitOperational, err)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("message = %q, want it to say the request timed out", err.Error())
	}
}

func TestConstructingAClientContactsNoServer(t *testing.T) {
	fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{})
	})

	_ = newClient(t, config.Config{
		Address: fake.server.URL,
		Token:   config.SensitiveString(testToken),
	})

	if got := fake.recorded(); len(got) != 0 {
		t.Errorf("building a client made %d request(s): %+v", len(got), got)
	}
}

// jsonNumber reads a decoded JSON number back as an int, whatever concrete
// type the decoder produced.
func jsonNumber(t *testing.T, value any) int {
	t.Helper()
	switch n := value.(type) {
	case json.Number:
		parsed, err := n.Int64()
		if err != nil {
			t.Fatalf("value %v is not an integer: %v", value, err)
		}
		return int(parsed)
	case float64:
		return int(n)
	case int:
		return n
	default:
		t.Fatalf("value %v (%T) is not a number", value, value)
		return 0
	}
}

// TestCreateCollisionFromALiveServerIsAConflict pins the create half of
// the same question, with the message a live OpenBao 2.5.2 actually
// returned when cas = -1 hit an existing policy (measured 2026-09-18).
//
// It matters because a create collision that fell through to a generic
// failure would reach the editor as "something went wrong" rather than as
// the name-is-taken question, which is the one place the user is offered
// the existing policy instead.
func TestCreateCollisionFromALiveServerIsAConflict(t *testing.T) {
	fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusBadRequest,
			errorBody("check-and-set parameter set to -1 on existing entry"))
	})

	_, err := fake.client(t).Create(context.Background(), "deploy", "path \"a\" {}\n")

	if !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
	if apperr.CodeOf(err) != apperr.ExitConflict {
		t.Errorf("exit code = %v, want %v", apperr.CodeOf(err), apperr.ExitConflict)
	}
}

// TestErrorsNameTheOperationExactlyOnce guards a message the user
// actually saw: "updating policy X: updating policy X failed: ...". The
// client names the operation, and the editor used to name it again.
func TestErrorsNameTheOperationExactlyOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(c *Client) error
		op   string
	}{
		{"update", func(c *Client) error {
			_, err := c.Update(context.Background(), "deploy", "path \"a\" {}\n",
				Revision{Version: 1, HasVersion: true})
			return err
		}, "updating policy deploy"},
		{"create", func(c *Client) error {
			_, err := c.Create(context.Background(), "deploy", "path \"a\" {}\n")
			return err
		}, "creating policy deploy"},
		{"read", func(c *Client) error {
			_, err := c.Read(context.Background(), "deploy")
			return err
		}, "reading policy deploy"},
		{"delete", func(c *Client) error {
			return c.Delete(context.Background(), "deploy")
		}, "deleting policy deploy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, http.StatusInternalServerError, errorBody("upstream exploded"))
			})

			err := tc.call(fake.client(t))
			if err == nil {
				t.Fatal("expected an error")
			}
			if got := strings.Count(err.Error(), tc.op); got != 1 {
				t.Errorf("the message names %q %d times, want exactly once:\n%s",
					tc.op, got, err.Error())
			}
		})
	}
}

// TestAWriteWithNoVersionInItsResponseIsReadBack covers the consequence of
// this endpoint answering a successful write with 204 and no body, which
// is what a live OpenBao 2.5.2 does.
//
// Without the read-back, every second update in a session would be refused
// for lack of conflict protection, and the metadata carried into it would
// be empty — which is how a POST silently clears what it is not sent.
func TestAWriteWithNoVersionInItsResponseIsReadBack(t *testing.T) {
	var methods []string
	fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
				"policy":       "path \"a\" {}\n",
				"version":      42,
				"cas_required": true,
			}})
			return
		}
		w.WriteHeader(http.StatusNoContent) // exactly what 2.5.2 answers
	})

	result, err := fake.client(t).Update(context.Background(), "deploy", "path \"a\" {}\n",
		Revision{Version: 41, HasVersion: true})
	if err != nil {
		t.Fatalf("Update returned an error: %v", err)
	}

	if !result.Revision.HasVersion || result.Revision.Version != 42 {
		t.Errorf("revision = %+v, want version 42 read back after the write", result.Revision)
	}
	if result.Revision.Metadata.CASRequired == nil || !*result.Revision.Metadata.CASRequired {
		t.Error("the metadata was not picked up by the read-back, so the next update would clear it")
	}
	if len(methods) != 2 || methods[0] != http.MethodPost || methods[1] != http.MethodGet {
		t.Errorf("requests = %v, want a POST then a GET", methods)
	}
}

// TestAFailedReadBackStillReportsTheWriteAsSucceeded — the policy has
// already changed on the server, so reporting a failure would invite the
// caller to retry a write that already landed.
func TestAFailedReadBackStillReportsTheWriteAsSucceeded(t *testing.T) {
	fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusInternalServerError, errorBody("gone away"))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	result, err := fake.client(t).Update(context.Background(), "deploy",
		"path \"a\" {}\n", Revision{Version: 1, HasVersion: true})

	if err != nil {
		t.Fatalf("Update reported a failure although the write succeeded: %v", err)
	}
	if result.Revision.HasVersion {
		t.Error("a version was reported although the read-back failed")
	}
}
