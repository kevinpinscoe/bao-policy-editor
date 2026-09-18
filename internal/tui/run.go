package tui

import (
	"context"
	"errors"
	"io"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/kevinpinscoe/bao-policy-editor/internal/apperr"
	"github.com/kevinpinscoe/bao-policy-editor/internal/config"
)

// Options configures a run of the interactive editor.
type Options struct {
	// PolicyFile is the file to open, or "" to start with an empty
	// document.
	PolicyFile string

	// Remote asks the editor to open on the remote policy browser rather
	// than on a local document. It is mutually exclusive with PolicyFile,
	// which the CLI enforces as a usage error before ever reaching here.
	Remote bool

	// Config is BPE's resolved configuration. It is carried whether or not
	// remote mode is used; nothing reads it on a local run.
	Config config.Config

	// NewStore builds the OpenBao client. Nil means the real one. It is
	// injectable so a test can assert it is never called for a local
	// session — see StoreFactory.
	NewStore StoreFactory

	// Input and Output are the terminal to run on. Both default to the
	// process's own when nil, and are injectable so a test can drive the
	// program without a TTY.
	Input  io.Reader
	Output io.Writer
}

// Run starts the interactive editor and blocks until the user leaves it.
//
// It returns an *apperr.AppError, so the caller maps the outcome to an
// exit code the same way every other bpe command does rather than
// inventing a second convention for the TUI. Cancellation — Ctrl+C at the
// signal level, or ctx being cancelled — comes back as
// apperr.Interrupted() and exit 130.
//
// # Offline by construction
//
// A run with Remote false builds no OpenBao client and opens no
// connection. The client is constructed inside the command that enters
// remote mode and nowhere else, so `bpe` and `bpe policy.hcl` cannot
// contact a server even with a fully populated BAO_ADDR and BAO_TOKEN in
// the environment.
//
// # Terminal restoration
//
// Bubble Tea restores the terminal on every path out of Run: a normal
// quit, an interrupt, and a recovered panic (panic catching is left
// enabled deliberately, so a bug in a view leaves a usable shell rather
// than a terminal stuck in raw mode with the alternate screen still up).
// Nothing in this package manipulates terminal modes itself, which is what
// keeps that guarantee in one place.
func Run(ctx context.Context, opts Options) error {
	session, err := openSession(opts.PolicyFile)
	if err != nil {
		return err
	}

	input := opts.Input
	if input == nil {
		input = os.Stdin
	}
	output := opts.Output
	if output == nil {
		output = os.Stdout
	}

	model := NewWithOptions(session, ModelOptions{
		Context:     ctx,
		Config:      opts.Config,
		NewStore:    opts.NewStore,
		StartRemote: opts.Remote,
	})

	program := tea.NewProgram(
		model,
		tea.WithContext(ctx),
		tea.WithInput(input),
		tea.WithOutput(output),
	)

	if _, err := program.Run(); err != nil {
		switch {
		case errors.Is(err, tea.ErrInterrupted), errors.Is(err, context.Canceled):
			return apperr.Interrupted()
		default:
			return apperr.Wrap(apperr.ExitOperational, "the interactive editor failed", err)
		}
	}
	return nil
}

// openSession loads the document the editor starts on, translating the
// failures worth distinguishing into the exit-code contract.
//
// A file that cannot be read is operational (3). A file that reads but
// does not parse is not a failure at all: it opens read-only with its
// diagnostics on display, which is more use to someone fixing it than a
// refusal would be.
func openSession(path string) (*Session, error) {
	if path == "" {
		return NewSession(), nil
	}

	session, err := OpenSession(path)
	if err != nil {
		return nil, apperr.Wrap(apperr.ExitOperational, "failed to open policy file", err).WithDetail(path)
	}
	return session, nil
}
