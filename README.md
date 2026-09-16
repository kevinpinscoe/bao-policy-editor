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

## Requirements

- Go 1.27
- `mise` for the repository-managed development environment
- A terminal with UTF-8 and ANSI color support
- OpenBao is optional; local policy editing and validation do not require a running server
- Network access to an OpenBao server is required only for remote policy operations
- Remote operations require an OpenBao token with `list` on `sys/policies/acl`, `read` on `sys/policies/acl/*`, `create` or `update` for policy changes, and `delete` for policy deletion

## Configuration

BPE uses OpenBao's established environment variables rather than inventing BPE-specific equivalents:

| Variable | Meaning |
| --- | --- |
| `BAO_ADDR` | OpenBao server URL |
| `BAO_TOKEN` | Authentication token |
| `BAO_NAMESPACE` | Optional namespace |
| `BAO_CACERT` | Optional CA certificate |
| `BAO_CAPATH` | Optional CA certificate directory |
| `BAO_CLIENT_CERT` | Optional client certificate |
| `BAO_CLIENT_KEY` | Optional client private key |
| `BAO_TLS_SERVER_NAME` | Optional TLS server name |

Following OpenBao CLI convention, `BAO_*` variables are preferred and fall back to their corresponding `VAULT_*` variables where OpenBao itself does so.

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
```

## Repository Layout

```text
bao-policy-editor/
├── cmd/
│   └── bpe/                 # main.go — application entry point
├── internal/
│   ├── tui/                 # Bubble Tea screens, components, and styles
│   ├── policy/               # UI-independent policy domain model
│   ├── hclpolicy/            # HCL parsing, validation, and generation
│   ├── evaluator/             # Matching and effective-access simulation
│   ├── baoclient/             # OpenBao API integration
│   └── config/                # Environment and file configuration
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

The TUI collects user actions and renders policy rules but does not implement policy semantics itself. The `policy` package holds the UI-independent domain model. `hclpolicy` translates between that model and HCL, while `evaluator` determines effective access using OpenBao path-matching and capability rules. `baoclient` lists, retrieves, and updates remote policies through the OpenBao API. `config` resolves command-line and environment configuration. This separation allows the parser, evaluator, and client to be tested without running the terminal interface.

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

## Build and Release

Releases are planned as cross-platform binaries using GoReleaser.

## Deployment

There is no deployment process because BPE is a local executable.

## Operations / Runbook

See [`RUNBOOK.md`](RUNBOOK.md) for operational procedures.

## Troubleshooting

Common connection, TLS, terminal-rendering, logging, and recovery procedures belong in [`RUNBOOK.md`](RUNBOOK.md). Operational guidance there is still under development until those features exist.

## Security

Do not report suspected security vulnerabilities in a public issue. Use GitHub's private vulnerability reporting feature when available. BPE processes authentication tokens and authorization policies; diagnostic output and bug reports must not contain tokens, credentials, or secret values.

A dedicated `SECURITY.md` can be added before the first public release.

## Ownership and Support

Bao Policy Editor is currently maintained by Kevin Inscoe. Community issues and pull requests are welcome. Support is provided on a best-effort basis through GitHub Issues; the project currently has no commercial support or response-time guarantee.

## Related Documentation

- [Runbook](RUNBOOK.md)

## License

Licensed under the [Mozilla Public License 2.0](LICENSE) (MPL-2.0).
