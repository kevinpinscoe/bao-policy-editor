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
	if !policy.Revision.CASRequired {
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

func TestUpdateSendsPatchCarryingThePreviouslyReadVersion(t *testing.T) {
	fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"version": 8}})
	})

	rev := Revision{Version: 7, HasVersion: true}
	if _, err := fake.client(t).Update(context.Background(), "deploy", "path \"a\" {}\n", rev); err != nil {
		t.Fatalf("Update returned an error: %v", err)
	}

	req := fake.only(t)
	if req.Method != http.MethodPatch {
		t.Errorf("method = %s, want PATCH — POST resets unspecified fields to defaults", req.Method)
	}
	if req.Path != "/v1/sys/policies/acl/deploy" {
		t.Errorf("path = %s", req.Path)
	}
	if got := jsonNumber(t, req.Body["cas"]); got != 7 {
		t.Errorf("cas = %v, want the version read earlier (7)", req.Body["cas"])
	}
	if req.ContentType != mergePatchContentType {
		t.Errorf("content type = %q, want %q", req.ContentType, mergePatchContentType)
	}
}

func TestUpdateSendsOnlyThePolicyAndCas(t *testing.T) {
	// The fields BPE does not model — expiration, ttl, cas_required, the
	// identity-template flags — must not appear in the body at all. PATCH
	// preserves what it is not told about; naming a field would set it.
	fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{})
	})

	rev := Revision{Version: 2, HasVersion: true, CASRequired: true}
	if _, err := fake.client(t).Update(context.Background(), "deploy", "path \"a\" {}\n", rev); err != nil {
		t.Fatalf("Update returned an error: %v", err)
	}

	body := fake.only(t).Body
	if len(body) != 2 {
		t.Errorf("body carried %d fields, want exactly policy and cas: %+v", len(body), body)
	}
	for _, unwanted := range []string{"cas_required", "expiration", "ttl",
		"allow_wildcards_in_identity_templates", "allow_slashes_in_identity_templates"} {
		if _, present := body[unwanted]; present {
			t.Errorf("body included %q — PATCH preserves fields it is not told about, "+
				"so naming one would overwrite the server's own setting", unwanted)
		}
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
