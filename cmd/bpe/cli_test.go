package main

import (
	"errors"
	"testing"

	"github.com/kevinpinscoe/bao-policy-editor/internal/apperr"
)

func TestParseArgs_Success(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want Command
	}{
		{
			name: "no arguments",
			args: nil,
			want: Command{Kind: KindDefault},
		},
		{
			name: "local policy filename",
			args: []string{"policy.hcl"},
			want: Command{Kind: KindDefault, PolicyFile: "policy.hcl"},
		},
		{
			name: "validate subcommand",
			args: []string{"validate", "policy.hcl"},
			want: Command{Kind: KindValidate, PolicyFile: "policy.hcl"},
		},
		{
			name: "format subcommand",
			args: []string{"format", "policy.hcl"},
			want: Command{Kind: KindFormat, PolicyFile: "policy.hcl"},
		},
		{
			name: "test subcommand, flags after filename (as documented)",
			args: []string{"test", "policy.hcl", "--path", "secret/data/example", "--capability", "read"},
			want: Command{Kind: KindTest, PolicyFile: "policy.hcl", TestPath: "secret/data/example", TestCapability: "read"},
		},
		{
			name: "test subcommand, flags reordered before filename",
			args: []string{"test", "--capability", "read", "--path", "secret/data/example", "policy.hcl"},
			want: Command{Kind: KindTest, PolicyFile: "policy.hcl", TestPath: "secret/data/example", TestCapability: "read"},
		},
		{
			name: "test subcommand, flags reordered relative to each other",
			args: []string{"test", "policy.hcl", "--capability", "read", "--path", "secret/data/example"},
			want: Command{Kind: KindTest, PolicyFile: "policy.hcl", TestPath: "secret/data/example", TestCapability: "read"},
		},
		{
			name: "top-level --help",
			args: []string{"--help"},
			want: Command{Kind: KindHelp},
		},
		{
			name: "top-level -h",
			args: []string{"-h"},
			want: Command{Kind: KindHelp},
		},
		{
			name: "help word, no topic",
			args: []string{"help"},
			want: Command{Kind: KindHelp},
		},
		{
			name: "help word with subcommand topic",
			args: []string{"help", "validate"},
			want: Command{Kind: KindHelp, HelpTopic: "validate"},
		},
		{
			name: "subcommand --help",
			args: []string{"validate", "--help"},
			want: Command{Kind: KindHelp, HelpTopic: "validate"},
		},
		{
			name: "test subcommand --help ignores otherwise-invalid args",
			args: []string{"test", "--help"},
			want: Command{Kind: KindHelp, HelpTopic: "test"},
		},
		{
			name: "--version",
			args: []string{"--version"},
			want: Command{Kind: KindVersion},
		},
		{
			name: "global flags reordered around subcommand",
			args: []string{"--address", "https://bao.example.com", "validate", "policy.hcl"},
			want: Command{Kind: KindValidate, PolicyFile: "policy.hcl"},
		},
		{
			name: "global flags after subcommand and its positional arg",
			args: []string{"validate", "policy.hcl", "--address", "https://bao.example.com"},
			want: Command{Kind: KindValidate, PolicyFile: "policy.hcl"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseArgs(tc.args)
			if err != nil {
				t.Fatalf("ParseArgs(%v) error = %v, want nil", tc.args, err)
			}
			if got.Kind != tc.want.Kind {
				t.Errorf("Kind = %v, want %v", got.Kind, tc.want.Kind)
			}
			if got.PolicyFile != tc.want.PolicyFile {
				t.Errorf("PolicyFile = %q, want %q", got.PolicyFile, tc.want.PolicyFile)
			}
			if got.TestPath != tc.want.TestPath {
				t.Errorf("TestPath = %q, want %q", got.TestPath, tc.want.TestPath)
			}
			if got.TestCapability != tc.want.TestCapability {
				t.Errorf("TestCapability = %q, want %q", got.TestCapability, tc.want.TestCapability)
			}
			if got.HelpTopic != tc.want.HelpTopic {
				t.Errorf("HelpTopic = %q, want %q", got.HelpTopic, tc.want.HelpTopic)
			}
		})
	}
}

func TestParseArgs_GlobalFlagsResolve(t *testing.T) {
	got, err := ParseArgs([]string{"--address", "https://bao.example.com", "--skip-verify", "validate", "policy.hcl"})
	if err != nil {
		t.Fatalf("ParseArgs() error = %v", err)
	}
	if got.ConfigFlags.Address == nil || *got.ConfigFlags.Address != "https://bao.example.com" {
		t.Errorf("ConfigFlags.Address = %v, want https://bao.example.com", got.ConfigFlags.Address)
	}
	if got.ConfigFlags.SkipVerify == nil || *got.ConfigFlags.SkipVerify != "true" {
		t.Errorf("ConfigFlags.SkipVerify = %v, want true", got.ConfigFlags.SkipVerify)
	}
}

func TestParseArgs_Errors(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{name: "missing required argument — validate", args: []string{"validate"}},
		{name: "missing required argument — format", args: []string{"format"}},
		{name: "missing required argument — test", args: []string{"test", "--path", "x", "--capability", "read"}},
		{name: "missing --path", args: []string{"test", "policy.hcl", "--capability", "read"}},
		{name: "missing --capability", args: []string{"test", "policy.hcl", "--path", "x"}},
		{name: "empty --path treated as missing", args: []string{"test", "policy.hcl", "--path=", "--capability", "read"}},
		{name: "unexpected argument — validate", args: []string{"validate", "policy.hcl", "extra.hcl"}},
		{name: "unexpected argument — test", args: []string{"test", "policy.hcl", "extra.hcl", "--path", "x", "--capability", "read"}},
		{name: "unknown command", args: []string{"frobnicate", "extra"}},
		{name: "unknown flag at top level", args: []string{"--bogus-flag"}},
		{name: "unknown flag within validate", args: []string{"validate", "--bogus-flag", "policy.hcl"}},
		{name: "unknown flag within test", args: []string{"test", "policy.hcl", "--path", "x", "--capability", "read", "--bogus"}},
		{name: "unexpected argument after --help", args: []string{"--help", "extra"}},
		{name: "unexpected argument after --version", args: []string{"--version", "extra"}},
		{name: "unknown help topic", args: []string{"help", "frobnicate"}},
		{name: "flag missing its value", args: []string{"--address"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseArgs(tc.args)
			if err == nil {
				t.Fatalf("ParseArgs(%v) error = nil, want a usage error", tc.args)
			}
			var appErr *apperr.AppError
			if !errors.As(err, &appErr) {
				t.Fatalf("error is not an *apperr.AppError: %v", err)
			}
			if appErr.Code != apperr.ExitUsage {
				t.Errorf("error code = %v, want %v (%v)", appErr.Code, apperr.ExitUsage, err)
			}
		})
	}
}
