package apperr

import (
	"errors"
	"testing"
)

func TestAppError_Error(t *testing.T) {
	cases := []struct {
		name string
		err  *AppError
		want string
	}{
		{
			name: "message only",
			err:  New(ExitUsage, "missing filename"),
			want: "missing filename",
		},
		{
			name: "message and detail",
			err:  New(ExitUsage, "unknown flag").WithDetail("--bogus"),
			want: "unknown flag: --bogus",
		},
		{
			name: "message and cause",
			err:  Wrap(ExitOperational, "resolve failed", errors.New("boom")),
			want: "resolve failed: boom",
		},
		{
			name: "message, detail, and cause",
			err:  Wrap(ExitOperational, "resolve failed", errors.New("boom")).WithDetail("BAO_ADDR"),
			want: "resolve failed: BAO_ADDR: boom",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Errorf("Error() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAppError_Unwrap_ErrorsIsAs(t *testing.T) {
	cause := errors.New("underlying")
	err := Wrap(ExitOperational, "wrapped", cause)

	if !errors.Is(err, cause) {
		t.Error("errors.Is(err, cause) = false, want true")
	}

	var target *AppError
	if !errors.As(err, &target) {
		t.Fatal("errors.As(err, &target) = false, want true")
	}
	if target != err {
		t.Errorf("errors.As target = %v, want %v", target, err)
	}
}

func TestAppError_WithDetail_DoesNotMutateOriginal(t *testing.T) {
	original := New(ExitUsage, "bad arg")
	_ = original.WithDetail("--path")

	if original.Detail != "" {
		t.Errorf("original.Detail = %q, want empty — WithDetail must not mutate the receiver", original.Detail)
	}
}

func TestConstructors(t *testing.T) {
	if got := Usage("bad"); got.Code != ExitUsage {
		t.Errorf("Usage code = %v, want %v", got.Code, ExitUsage)
	}
	if got := Usagef("bad %s", "arg"); got.Message != "bad arg" || got.Code != ExitUsage {
		t.Errorf("Usagef = %+v, want message %q code %v", got, "bad arg", ExitUsage)
	}
	if got := NotImplemented("policy validation"); got.Code != ExitOperational {
		t.Errorf("NotImplemented code = %v, want %v", got.Code, ExitOperational)
	} else if got.Message != "policy validation is not implemented yet" {
		t.Errorf("NotImplemented message = %q", got.Message)
	}
	if got := Interrupted(); got.Code != ExitInterrupted {
		t.Errorf("Interrupted code = %v, want %v", got.Code, ExitInterrupted)
	}
}

func TestCodeOf(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want ExitCode
	}{
		{"nil error", nil, ExitSuccess},
		{"app error", Usage("bad"), ExitUsage},
		{"wrapped app error", errWrapper{Usage("bad")}, ExitUsage},
		{"plain error", errors.New("boom"), ExitOperational},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CodeOf(tc.err); got != tc.want {
				t.Errorf("CodeOf(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// errWrapper wraps another error to exercise errors.As unwrapping through a
// layer that is not itself an *AppError.
type errWrapper struct{ inner error }

func (w errWrapper) Error() string { return w.inner.Error() }
func (w errWrapper) Unwrap() error { return w.inner }
