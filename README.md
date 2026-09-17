# bao-policy-editor

## Summary

Bao Policy Editor (BPE) is an open-source terminal interface for creating, editing, validating, and testing OpenBao HCL ACL policies. It combines a visual capability editor with HCL inspection and effective-access explanations.

## Status

Experimental

## Purpose

BPE helps OpenBao administrators and platform engineers understand policy behavior before granting access. It provides a guided alternative to manually editing HCL, with particular attention to path precedence, wildcards, capabilities, explicit denies, and KV v2 paths. It can work with local policy files without an OpenBao server and will optionally read and update policies through the OpenBao API. Nothing currently depends on it.

## Quick Start

```bash
git clone git@github.com:kevinpinscoe/bao-policy-editor.git
cd bao-policy-editor
mise install
go build -o ./bin/bpe ./cmd/bpe
./bin/bpe
```

To open a local policy:

```bash
./bin/bpe policy.hcl
```

To validate a policy without starting the TUI:

```bash
./bin/bpe validate policy.hcl
```

This prints every diagnostic found — errors and warnings alike, each
labeled — and exits `0` if there are no errors (warnings alone do not fail
validation) or `1` if there are. See [Exit Codes](#exit-codes).

To simulate an effective-access check:

```bash
./bin/bpe test policy.hcl --path secret/data/team-a/example --capability read
```

This prints a structured explanation of the decision and exits `0` if
access is allowed, `1` if it is denied. See
[Evaluation limits](#evaluation-limits) for what it deliberately does not
attempt, and [Exit Codes](#exit-codes) for the full contract.

At this stage `bpe` and `bpe <policy.hcl>` (opening the interactive editor)
print an explicit "not implemented yet" message and exit non-zero rather
than starting a TUI — see [CLI Reference](#cli-reference) below for what
each command currently does.

## Requirements

- Go 1.27
- `mise` for the repository-managed development environment
- A terminal with UTF-8 and ANSI color support
- OpenBao is optional; local policy editing and validation do not require a running server
- Network access to an OpenBao server is required only for remote policy operations
- Remote operations require an OpenBao token with `list` on `sys/policies/acl`, `read` on `sys/policies/acl/*`, `create` or `update` for policy changes, and `delete` for policy deletion

## CLI Reference

```text
bpe                                                              Start the interactive editor with an empty policy
bpe <policy.hcl>                                                 Start the interactive editor, opening a policy file
bpe validate <policy.hcl>                                        Validate a policy without starting the interactive editor
bpe format <policy.hcl>                                          Format a policy file
bpe test <policy.hcl> --path <path> --capability <capability>    Simulate an effective-access check
bpe help [command]                                                Show help, optionally for one command
bpe --help / bpe <command> --help                                 Show help
bpe --version                                                      Show version information
```

`--path` and `--capability` may appear before or after the policy file. Global
configuration flags (`--address`, `--token`, `--namespace`, `--ca-cert`,
`--ca-path`, `--client-cert`, `--client-key`, `--tls-server-name`,
`--skip-verify`) are accepted anywhere on the command line — see
[Configuration](#configuration).

**Currently implemented:** argument parsing and validation, `--help`/`help`,
`bpe <command> --help`, `--version`, `bpe validate <policy.hcl>`, and
`bpe test <policy.hcl> --path <path> --capability <capability>` all behave
as documented below.

`bpe validate` reads and parses the file, then runs semantic checks —
unknown capabilities, `deny` combined with other capabilities, duplicate
path blocks, suspicious wildcard placement, broad `sys/*` access, `sudo`
use, `create` without `update`, `list`/`scan` on a path that does not look
like a prefix, KV v2 path-shape mistakes (only warned about when another
rule in the same file gives evidence the mount is KV v2 — `secret/` is
never assumed to be KV v2 on its own), expired rules, and contradictory
`required_parameters`/`denied_parameters` constraints (an error only when
every value a required parameter is allowed is also denied, or the
parameter is denied or unlisted outright — see
[OpenBao's parameter-constraint semantics](https://openbao.org/docs/concepts/policies/#parameter-constraints);
a partial overlap that still leaves a valid value is never reported).
Every diagnostic is printed with its severity; warnings never fail
validation. It performs no write and never contacts OpenBao.

`bpe test` reads and parses the file, then simulates an effective-access
check against `internal/evaluator`'s OpenBao-compatible matcher: exact,
suffix-glob (`*`), and single-segment (`+`) path matching; default deny;
explicit `deny` precedence; capability union and per-capability source
tracking across policies; OpenBao's own 5-rule wildcard priority
comparator; and `list`/`scan`'s 4-stage lookup fallback (see
[Evaluation limits](#evaluation-limits) below for what it does not
attempt). It prints a structured, human-readable explanation — the
requested path and capability, the result, which stage and pattern
decided it, which policy granted or denied it, and any competing patterns
that lost and why — then exits `0` for an allowed request or `1` for a
denied one. `--capability` is validated against OpenBao's known
capabilities before the file is even read (an unknown value is a usage
error, exit `2`). It performs no write and never contacts OpenBao.

**Currently scaffolded — not implemented yet:** the interactive editor
(`bpe` / `bpe <policy.hcl>`) and `format` parse and validate their
arguments correctly, then report an explicit "not implemented yet" message
on standard error and exit `3`. Neither silently succeeds, writes a
file, or contacts OpenBao. Real behavior lands in FSM-14 through FSM-18.

### Evaluation limits

`bpe test` answers "does this path and capability match, and does the
winning rule grant it?" — it is not a full request simulator. Three kinds
of OpenBao behavior it does not attempt, by design:

- **Request parameters.** `bpe test` supplies only a path and a
  capability, never concrete request data. If the winning rule carries
  `required_parameters`, `allowed_parameters`, or `denied_parameters`,
  whether a real request would satisfy them depends on values `bpe test`
  is never given — it refuses to guess, reporting `INCOMPLETE` and
  exiting `3` rather than printing a misleading `ALLOWED`.
- **Identity templates.** BPE's domain model (from FSM-11) decodes a
  path label as a literal string; it does not resolve OpenBao identity
  templates (`{{identity.entity.id}}` and similar). A winning rule whose
  pattern looks templated is also reported as `INCOMPLETE` rather than
  matched against its literal, unresolved template syntax.
- **Content FSM-11 cannot decode.** If the file contains a block or
  attribute BPE's domain model has no field for, `bpe test` refuses to
  simulate against it at all — the decoded policy would be an incomplete
  picture of what OpenBao would actually enforce. It prints the
  raw parse diagnostics (so unsupported content is never silently
  dropped on the way to a decision) and exits `1`, the same as any other
  policy-level problem.

None of these are silently ignored in favor of a confident answer — see
[Exit Codes](#exit-codes).

### Expiration is a snapshot, not live

A rule's `expiration` is evaluated once, at the moment `bpe test` reads
the file — every invocation uses the current time. The compiled
evaluator is a snapshot: an expired rule is excluded entirely, as if it
had never been written, and that exclusion does not update itself as
time passes within a single run. Running `bpe test` again later, against
a rule whose expiration has since passed, correctly excludes it on that
later run — but nothing about a single invocation is live or
re-evaluated mid-command.

## Configuration

BPE uses OpenBao's established environment variables rather than inventing BPE-specific equivalents. Configuration is resolved in this order, highest precedence first:

1. An explicit command-line flag
2. The corresponding `BAO_*` environment variable
3. The corresponding `VAULT_*` environment variable, as a fallback
4. A safe default (empty string, or `false` for `--skip-verify`)

An environment variable that is set but empty counts as set at that
precedence tier — it does not fall through to the next one. This applies
identically to an explicitly empty flag value.

| Variable | CLI flag | Meaning |
| --- | --- | --- |
| `BAO_ADDR` | `--address` | OpenBao server URL |
| `BAO_TOKEN` | `--token` | Authentication token (never logged, printed, or included in diagnostic output) |
| `BAO_NAMESPACE` | `--namespace` | Optional namespace |
| `BAO_CACERT` | `--ca-cert` | Optional CA certificate |
| `BAO_CAPATH` | `--ca-path` | Optional CA certificate directory |
| `BAO_CLIENT_CERT` | `--client-cert` | Optional client certificate |
| `BAO_CLIENT_KEY` | `--client-key` | Optional client private key |
| `BAO_TLS_SERVER_NAME` | `--tls-server-name` | Optional TLS server name |
| `BAO_SKIP_VERIFY` | `--skip-verify` | Disable TLS certificate verification (default `false`) |

Following OpenBao CLI convention, `BAO_*` variables are preferred and fall back to their corresponding `VAULT_*` variables. Configuration resolution never reads a persistent BPE configuration file and never contacts OpenBao — no command in this release performs network access.

## Exit Codes

| Code | Meaning | Status |
| --- | --- | --- |
| `0` | Successful command, or `--help`/`help`/`--version` output | Active |
| `1` | Policy validation failure, a denied `test` result, or a policy `test` cannot trust enough to simulate against (unsupported content or decode errors) | Active for `validate` (FSM-12) and `test` (FSM-13) — corrected from an earlier, mistaken "reserved for FSM-17" note; FSM-17 is TUI/remote integration, unrelated to this exit code |
| `2` | Command-line usage or configuration error | Active |
| `3` | Operational failure, including a command deliberately not implemented yet, a policy file that could not be read, or a `test` result `bpe` cannot reduce to a trustworthy decision from path and capability alone (see [Evaluation limits](#evaluation-limits)) | Active |
| `4` | Concurrent modification or version conflict | Reserved for FSM-16 |
| `130` | Interrupted by the user (Ctrl+C or SIGTERM) | Active |

## Common Commands

```bash
# Install the configured toolchain
mise install

# Run from source
go run ./cmd/bpe

# Open a policy
go run ./cmd/bpe policy.hcl

# Build
go build -o ./bin/bpe ./cmd/bpe

# Format
go fmt ./...

# Test
go test ./...

# Run static analysis
go vet ./...

# Update dependencies
go mod tidy

# Show CLI help and version
go run ./cmd/bpe --help
go run ./cmd/bpe --version
```

## Repository Layout

```text
bao-policy-editor/
├── cmd/
│   └── bpe/                 # main.go, CLI parsing, command dispatch — application entry point
├── internal/
│   ├── tui/                 # Bubble Tea screens, components, and styles
│   ├── policy/               # UI-independent policy domain model
│   ├── hclpolicy/            # HCL parsing, validation, and generation
│   ├── evaluator/             # Matching and effective-access simulation
│   ├── baoclient/             # OpenBao API integration
│   ├── config/                # Environment and file configuration
│   └── apperr/                 # Structured application errors and the exit-code contract
├── testdata/
│   └── policies/              # Valid, invalid, and edge-case HCL fixtures
├── docs/                       # human decision-making documents
├── .github/
│   └── workflows/              # CI/release workflows
├── go.mod
├── mise.toml
├── LICENSE
├── README.md
└── RUNBOOK.md
```

## How It Works

The TUI collects user actions and renders policy rules but does not implement policy semantics itself. The `policy` package holds the UI-independent domain model. `hclpolicy` translates between that model and HCL, while `evaluator` determines effective access using OpenBao path-matching and capability rules. `baoclient` lists, retrieves, and updates remote policies through the OpenBao API. `config` resolves command-line and environment configuration. `apperr` defines the structured application error and exit-code contract shared by the CLI and the TUI. `cmd/bpe` itself is split into argument parsing (`cli.go`), configuration-independent command dispatch (`dispatch.go`), and a thin `main.go` that wires a signal-derived context and is the only place that calls `os.Exit`. This separation allows the parser, evaluator, client, and CLI dispatch to be tested without running the terminal interface or invoking a subprocess.

```text
Local HCL file ─┐
                 ├─> policy model ─> TUI ─> validation/simulation
OpenBao API ─────┘
```

## Development Workflow

- Create focused branches and keep parsing, evaluator, API, and TUI changes separated where practical.
- Run `go fmt ./...`, `go vet ./...`, and `go test ./...` before submitting a pull request.

## Testing

- Add table-driven tests and HCL fixtures for policy behavior and regressions.
- Tests must not require access to a live OpenBao server unless explicitly marked as integration tests.
- CLI argument parsing, configuration precedence, and command dispatch (help/usage/exit codes/cancellation) are covered by table-driven tests in `cmd/bpe` and `internal/config` that call Go functions directly rather than invoking a subprocess. A small number of process-level tests in `cmd/bpe` verify end-to-end exit-code behavior through the compiled binary.

## Build and Release

Releases are planned as cross-platform binaries using GoReleaser.

## Deployment

There is no deployment process because BPE is a local executable.

## Operations / Runbook

See [`RUNBOOK.md`](RUNBOOK.md) for operational procedures.

## Troubleshooting

Common connection, TLS, terminal-rendering, logging, and recovery procedures belong in [`RUNBOOK.md`](RUNBOOK.md). Operational guidance there is still under development until those features exist.

**Current limitations:** the interactive editor, formatting, file persistence, and OpenBao connectivity are not implemented yet — see [CLI Reference](#cli-reference). HCL parsing (FSM-11), policy validation (FSM-12, `bpe validate`), and effective-access simulation (FSM-13, `bpe test`) are implemented — the latter with the scope limits in [Evaluation limits](#evaluation-limits). Every command that isn't implemented says so explicitly and exits `3`; none of them report false success.

## Security

Do not report suspected security vulnerabilities in a public issue. Use GitHub's private vulnerability reporting feature when available. BPE processes authentication tokens and authorization policies; diagnostic output and bug reports must not contain tokens, credentials, or secret values.

A dedicated `SECURITY.md` can be added before the first public release.

## Ownership and Support

Bao Policy Editor is currently maintained by Kevin Inscoe. Community issues and pull requests are welcome. Support is provided on a best-effort basis through GitHub Issues; the project currently has no commercial support or response-time guarantee.

## Related Documentation

- [Runbook](RUNBOOK.md)

## License

Licensed under the [Mozilla Public License 2.0](LICENSE) (MPL-2.0).
