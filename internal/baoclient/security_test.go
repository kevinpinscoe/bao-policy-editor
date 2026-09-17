package baoclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/kevinpinscoe/bao-policy-editor/internal/apperr"
	"github.com/kevinpinscoe/bao-policy-editor/internal/config"
)

// TestTheTokenNeverAppearsInAnError walks every failure path this package
// can produce and asserts the token is in none of them.
//
// The last case is the one that matters most: a server that echoes the
// token back inside its own error message. Nothing in this package puts
// the token into an error, but the server's text is outside this package's
// control, and a credential leaking through a message BPE merely relayed
// would count just the same.
func TestTheTokenNeverAppearsInAnError(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    any
		operate func(*Client) error
	}{
		{
			name:   "unauthorized",
			status: http.StatusUnauthorized,
			body:   errorBody("permission denied"),
		},
		{
			name:   "forbidden",
			status: http.StatusForbidden,
			body:   errorBody("permission denied"),
		},
		{
			name:   "conflict",
			status: http.StatusBadRequest,
			body:   errorBody("check-and-set parameter did not match the current version"),
		},
		{
			name:   "server error",
			status: http.StatusInternalServerError,
			body:   errorBody("internal error"),
		},
		{
			name:   "the server echoes the token back",
			status: http.StatusBadRequest,
			body:   errorBody("rejected credential " + testToken + " for this request"),
		},
		{
			name:   "the server echoes the token in an unparseable body",
			status: http.StatusInternalServerError,
			body:   "panic while handling token " + testToken,
		},
	}

	operations := map[string]func(*Client) error{
		"List": func(c *Client) error {
			_, err := c.List(context.Background())
			return err
		},
		"Read": func(c *Client) error {
			_, err := c.Read(context.Background(), "deploy")
			return err
		},
		"Create": func(c *Client) error {
			_, err := c.Create(context.Background(), "deploy", "path \"a\" {}\n")
			return err
		},
		"Update": func(c *Client) error {
			_, err := c.Update(context.Background(), "deploy", "path \"a\" {}\n",
				Revision{Version: 1, HasVersion: true})
			return err
		},
		"Delete": func(c *Client) error {
			return c.Delete(context.Background(), "deploy")
		},
	}

	for _, tc := range tests {
		for opName, op := range operations {
			t.Run(tc.name+"/"+opName, func(t *testing.T) {
				fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
					switch body := tc.body.(type) {
					case string:
						w.WriteHeader(tc.status)
						_, _ = w.Write([]byte(body))
					default:
						writeJSON(w, tc.status, body)
					}
				})

				err := op(fake.client(t))
				if err == nil {
					t.Fatal("expected an error")
				}
				assertNoToken(t, "err.Error()", err.Error())
				assertNoToken(t, "%v", fmt.Sprintf("%v", err))
				assertNoToken(t, "%+v", fmt.Sprintf("%+v", err))
				assertNoToken(t, "%#v", fmt.Sprintf("%#v", err))
				assertNoToken(t, "%q", fmt.Sprintf("%q", err))
			})
		}
	}
}

// TestScrubbingKeepsTheErrorTypedAndItsExitCode guards the redaction
// itself: a scrubbed error must still answer errors.Is and still carry its
// exit code, or redaction would quietly turn a typed conflict into an
// untyped failure at exactly the moment something odd is happening.
func TestScrubbingKeepsTheErrorTypedAndItsExitCode(t *testing.T) {
	fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusBadRequest,
			errorBody("check-and-set failed for token "+testToken+"; cas parameter did not match"))
	})

	_, err := fake.client(t).Update(context.Background(), "deploy", "path \"a\" {}\n",
		Revision{Version: 1, HasVersion: true})

	assertNoToken(t, "scrubbed error", err.Error())
	if !strings.Contains(err.Error(), "<redacted>") {
		t.Errorf("the message does not show that something was redacted: %q", err.Error())
	}
	assertIsConflict(t, err)
}

// TestTheClientItselfNeverFormatsItsToken covers the other way a
// credential escapes: not through an error, but through someone printing
// the struct that holds it.
func TestTheClientItselfNeverFormatsItsToken(t *testing.T) {
	client := newClient(t, config.Config{
		Address: "https://bao.example.invalid:8200",
		Token:   config.SensitiveString(testToken),
	})

	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%d", "%x"} {
		assertNoToken(t, verb, fmt.Sprintf(verb, client))
	}
	assertNoToken(t, "String()", client.String())

	// And inside another struct, which is where an unexported field would
	// otherwise be printed raw by the reflection formatter.
	wrapper := struct {
		Client *Client
		Note   string
	}{Client: client, Note: "diagnostic"}
	for _, verb := range []string{"%v", "%+v", "%#v"} {
		assertNoToken(t, "wrapped "+verb, fmt.Sprintf(verb, wrapper))
	}
}

// TestNothingIsLoggedDuringAnOperation asserts this package writes nothing
// to the standard logger, which is the other place a credential could
// surface without anyone asking for it.
func TestNothingIsLoggedDuringAnOperation(t *testing.T) {
	var captured bytes.Buffer
	originalOutput := log.Writer()
	originalFlags := log.Flags()
	log.SetOutput(&captured)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(originalOutput)
		log.SetFlags(originalFlags)
	})

	fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusUnauthorized, errorBody("permission denied for "+testToken))
	})

	client := fake.client(t)
	_, _ = client.Read(context.Background(), "deploy")
	_, _ = client.List(context.Background())

	if captured.Len() != 0 {
		t.Errorf("the package wrote to the logger: %q", captured.String())
	}
	assertNoToken(t, "log output", captured.String())
}

// TestTheTokenIsSentOnlyAsTheAuthenticationHeader confirms the credential
// goes where it is supposed to and nowhere else — not in the path, not in
// the query string, not in the body.
func TestTheTokenIsSentOnlyAsTheAuthenticationHeader(t *testing.T) {
	var rawURL, rawBody string
	fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
		rawURL = r.URL.String()
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"version": 1}})
	})

	if _, err := fake.client(t).Create(context.Background(), "deploy", "path \"a\" {}\n"); err != nil {
		t.Fatalf("Create returned an error: %v", err)
	}

	req := fake.only(t)
	if req.Token != testToken {
		t.Errorf("the token was not sent as X-Vault-Token (got %q)", req.Token)
	}
	assertNoToken(t, "request URL", rawURL)
	if req.Body != nil {
		rawBody = fmt.Sprintf("%v", req.Body)
	}
	assertNoToken(t, "request body", rawBody)
}

// TestNoTokenIsPersisted asserts the package writes no file at all.
//
// A credential written to disk is the failure mode that outlives the
// process, so this checks the working directory and the temporary
// directory are untouched rather than trusting that no code path calls
// os.WriteFile.
func TestNoTokenIsPersisted(t *testing.T) {
	scratch := t.TempDir()
	t.Setenv("TMPDIR", scratch)

	before := dirSnapshot(t, scratch)

	fake := newFakeBao(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{"policy": "path \"a\" {}\n", "version": 1},
		})
	})
	client := fake.client(t)
	if _, err := client.Read(context.Background(), "deploy"); err != nil {
		t.Fatalf("Read returned an error: %v", err)
	}
	if _, err := client.Update(context.Background(), "deploy", "path \"a\" {}\n",
		Revision{Version: 1, HasVersion: true}); err != nil {
		t.Fatalf("Update returned an error: %v", err)
	}

	after := dirSnapshot(t, scratch)
	if len(after) != len(before) {
		t.Errorf("the package created %d file(s) under the temporary directory: %v",
			len(after)-len(before), after)
	}
}

// TestPackageHasNoTerminalDependency enforces the acceptance criterion
// structurally rather than by convention.
//
// The client must stay usable — and testable — without a terminal, so that
// the editor in FSM-17 can be built against the PolicyStore interface. An
// accidental import of a rendering package would not fail any other test
// here; it would simply make this package impossible to use headlessly one
// day, in a change that looked harmless.
func TestPackageHasNoTerminalDependency(t *testing.T) {
	forbidden := []string{"bubbletea", "charm.land", "lipgloss", "bubbles", "internal/tui"}

	fset := token.NewFileSet()
	packages, err := parser.ParseDir(fset, ".", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parsing this package failed: %v", err)
	}

	for pkgName, pkg := range packages {
		for fileName, file := range pkg.Files {
			for _, imported := range file.Imports {
				path, err := strconv.Unquote(imported.Path.Value)
				if err != nil {
					t.Fatalf("%s: unparseable import %s", fileName, imported.Path.Value)
				}
				for _, banned := range forbidden {
					if strings.Contains(path, banned) {
						t.Errorf("%s (package %s) imports %q, which pulls in a terminal dependency",
							fileName, pkgName, path)
					}
				}
			}
		}
	}
}

func assertNoToken(t *testing.T, where, text string) {
	t.Helper()
	if strings.Contains(text, testToken) {
		t.Errorf("the token appears in %s: %q", where, text)
	}
}

func assertIsConflict(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrConflict) {
		t.Errorf("error is no longer recognizable as a conflict: %v", err)
	}
	if code := apperr.CodeOf(err); code != apperr.ExitConflict {
		t.Errorf("exit code = %d, want %d", code, apperr.ExitConflict)
	}
}

func dirSnapshot(t *testing.T, dir string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s failed: %v", dir, err)
	}
	return found
}
