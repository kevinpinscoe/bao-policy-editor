package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/kevinpinscoe/bao-policy-editor/internal/baoclient"
	"github.com/kevinpinscoe/bao-policy-editor/internal/config"
)

// fakeStore is an in-memory RemoteStore.
//
// It exists so every remote workflow in this package can be driven without
// a server, a network, or a credential — the real HTTP path is exercised
// in internal/baoclient's own tests against httptest, and duplicating that
// here would test the client twice and the editor not at all.
//
// It models the one server behaviour the editor's safety rests on:
// check-and-set. Create refuses a name that exists, Update refuses a
// version that has moved on, and both refusals are baoclient.ErrConflict,
// so the editor's handling is tested against the same error it will see in
// production.
type fakeStore struct {
	mu sync.Mutex

	address  string
	warnings []string
	policies map[string]fakePolicy

	// errs, when set for an operation name, is returned instead of doing
	// the work.
	errs map[string]error

	// gate, when non-nil, blocks every operation until it is closed or the
	// operation's context is cancelled — which is how an in-flight request
	// is held still long enough to cancel or supersede it.
	gate chan struct{}

	// calls records every operation attempted, in order, so a test can
	// assert that something did *not* happen.
	calls []string
}

type fakePolicy struct {
	body    string
	version int
}

func newFakeStore(address string) *fakeStore {
	return &fakeStore{
		address:  address,
		policies: map[string]fakePolicy{},
		errs:     map[string]error{},
	}
}

func (f *fakeStore) Address() string            { return f.address }
func (f *fakeStore) SecurityWarnings() []string { return f.warnings }

// seed puts a policy on the fake server at a known version.
func (f *fakeStore) seed(name, body string, version int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.policies[name] = fakePolicy{body: body, version: version}
}

// bodyOf reports what the fake server currently holds, which is how a test
// tells "the write was refused" from "the write happened".
func (f *fakeStore) bodyOf(name string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.policies[name]
	return p.body, ok
}

func (f *fakeStore) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// countCalls reports how many times an operation was attempted.
//
// A test that wants to prove something did *not* happen counts before and
// after rather than scanning for the call's absence: the interesting
// assertion is usually "no *further* attempt", and an operation that
// legitimately happened once would defeat a bare absence check.
func countCalls(f *fakeStore, op string) int {
	n := 0
	for _, call := range f.callLog() {
		if call == op {
			n++
		}
	}
	return n
}

// enter records the call and honours the error hook and the gate. Every
// operation starts here, so a cancelled context is respected everywhere
// rather than only where a test happened to need it.
func (f *fakeStore) enter(ctx context.Context, op string) error {
	f.mu.Lock()
	f.calls = append(f.calls, op)
	err := f.errs[op]
	gate := f.gate
	f.mu.Unlock()

	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return err
}

func (f *fakeStore) List(ctx context.Context) (baoclient.PolicyList, error) {
	if err := f.enter(ctx, "list"); err != nil {
		return baoclient.PolicyList{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	names := make([]string, 0, len(f.policies))
	for name := range f.policies {
		names = append(names, name)
	}
	return baoclient.PolicyList{Names: names}, nil
}

func (f *fakeStore) Read(ctx context.Context, name string) (baoclient.Policy, error) {
	if err := f.enter(ctx, "read:"+name); err != nil {
		return baoclient.Policy{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.policies[name]
	if !ok {
		return baoclient.Policy{}, fmt.Errorf("%w: %s", baoclient.ErrPolicyNotFound, name)
	}
	return baoclient.Policy{
		Name: name,
		Body: p.body,
		Revision: baoclient.Revision{
			Version:    p.version,
			HasVersion: true,
		},
	}, nil
}

func (f *fakeStore) Create(ctx context.Context, name, body string) (baoclient.WriteResult, error) {
	if err := f.enter(ctx, "create:"+name); err != nil {
		return baoclient.WriteResult{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.policies[name]; exists {
		// cas = -1 on a name that is taken: the same conflict the real
		// endpoint produces, so the editor's create-conflict handling is
		// exercised against the real error value.
		return baoclient.WriteResult{}, fmt.Errorf("%w: %s already exists", baoclient.ErrConflict, name)
	}
	f.policies[name] = fakePolicy{body: body, version: 1}
	return baoclient.WriteResult{
		Revision: baoclient.Revision{Version: 1, HasVersion: true},
	}, nil
}

func (f *fakeStore) Update(ctx context.Context, name, body string, rev baoclient.Revision) (baoclient.WriteResult, error) {
	if err := f.enter(ctx, "update:"+name); err != nil {
		return baoclient.WriteResult{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.policies[name]
	if !ok {
		return baoclient.WriteResult{}, fmt.Errorf("%w: %s", baoclient.ErrPolicyNotFound, name)
	}
	if !rev.HasVersion {
		return baoclient.WriteResult{}, baoclient.ErrConflictProtectionUnsupported
	}
	if rev.Version != p.version {
		return baoclient.WriteResult{}, fmt.Errorf(
			"%w: sent cas %d, current version is %d", baoclient.ErrConflict, rev.Version, p.version)
	}
	next := p.version + 1
	f.policies[name] = fakePolicy{body: body, version: next}
	return baoclient.WriteResult{
		Revision: baoclient.Revision{Version: next, HasVersion: true},
	}, nil
}

func (f *fakeStore) Delete(ctx context.Context, name string) error {
	if err := f.enter(ctx, "delete:"+name); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.policies, name)
	return nil
}

// stripANSI removes escape sequences from a rendered frame.
//
// It is what makes "does not depend on colour" testable: a warning that
// survives this is a warning made of words, which is the build brief's
// requirement and the reason NO_COLOR works at all.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			// Skip to the end of the sequence: CSI sequences terminate on a
			// byte in the range @ to ~.
			i++
			for i < len(s) && (s[i] < '@' || s[i] > '~') {
				i++
			}
			if i < len(s) {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// --- driving the model ---

// runCmd executes a Bubble Tea command and returns its message, giving up
// after a short wait.
//
// The deadline is there for the commands that are not remote operations:
// a focused text input returns a blink command that sleeps until its next
// tick, and calling it inline would stall the test for no purpose. Every
// remote command here is backed by an in-memory fake and answers
// immediately, so nothing this suite cares about is lost to the timeout —
// it is short deliberately, because it is paid on every keystroke that
// focuses a field.
const cmdDeadline = 150 * time.Millisecond

func runCmd(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		return msg
	case <-time.After(cmdDeadline):
		return nil
	}
}

// deliver runs a command and feeds its message back into the model,
// repeating for whatever that produces — one full turn of the runtime's
// loop, and then any it cascades into (a write, for instance, refreshes
// the listing behind it).
func deliver(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	for i := 0; cmd != nil && i < 8; i++ {
		msg := runCmd(t, cmd)
		if msg == nil {
			return
		}
		_, cmd = m.Update(msg)
	}
}

// step sends one keystroke and completes whatever it started.
func step(t *testing.T, m *Model, keystrokes ...string) {
	t.Helper()
	for _, k := range keystrokes {
		_, cmd := m.Update(pressKey(k))
		deliver(t, m, cmd)
	}
}

// typeInto sends a run of ordinary characters.
func typeInto(t *testing.T, m *Model, text string) {
	t.Helper()
	for _, msg := range typeText(text) {
		_, cmd := m.Update(msg)
		deliver(t, m, cmd)
	}
}

// newRemoteModel builds a model connected to the fake, as though the user
// had already pressed r and connected.
func newRemoteModel(t *testing.T, store *fakeStore, session *Session) *Model {
	t.Helper()
	m := NewWithOptions(session, ModelOptions{
		Config: config.Config{Address: store.address},
		NewStore: func(config.Config) (RemoteStore, error) {
			return store, nil
		},
	})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return m
}

// connectTestModel builds a model and drives it through a connection, so a
// test that is about the browser does not have to repeat the handshake.
func connectTestModel(t *testing.T, store *fakeStore) *Model {
	t.Helper()
	m := newRemoteModel(t, store, NewSession())
	deliver(t, m, m.enterRemote())
	if m.screen != screenBrowse {
		t.Fatalf("after connecting, screen = %d, want the browser (%d); problem = %q",
			m.screen, screenBrowse, m.problem)
	}
	return m
}

// openRemotePolicy connects and opens one policy for editing.
func openRemotePolicy(t *testing.T, store *fakeStore, name string) *Model {
	t.Helper()
	m := connectTestModel(t, store)
	selectPolicy(t, m, name)
	step(t, m, "enter")
	if !m.session.IsRemote() {
		t.Fatalf("opening %s did not produce a remote session; problem = %q", name, m.problem)
	}
	return m
}

// selectPolicy moves the browser's selection onto a named policy.
func selectPolicy(t *testing.T, m *Model, name string) {
	t.Helper()
	for i := 0; i < len(m.browse.visible()); i++ {
		if m.browse.current() == name {
			return
		}
		step(t, m, "down")
	}
	t.Fatalf("policy %q is not in the browser listing %v", name, m.browse.visible())
}
