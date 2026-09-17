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

To format a policy file in place:

```bash
./bin/bpe format policy.hcl
```

Or check whether it needs formatting, without writing it — suitable for CI:

```bash
./bin/bpe format policy.hcl --check
```

See [File writes](#file-writes) for what "in place" guarantees and does not.

`bpe` with no subcommand opens the [interactive editor](#interactive-editor):
with a file, on that policy; without one, on an empty policy. Opening a
file does not modify it.

## Requirements

- Go 1.27
- `mise` for the repository-managed development environment
- A terminal with UTF-8 support. ANSI color is used but not required — see
  [Terminal behavior](#terminal-behavior)
- OpenBao is optional; local policy editing and validation do not require a running server
- Network access to an OpenBao server is required only for remote policy operations
- Remote operations require an OpenBao token with `list` on `sys/policies/acl`, `read` on `sys/policies/acl/*`, `create` or `update` for policy changes, and `delete` for policy deletion

## CLI Reference

```text
bpe                                                              Start the interactive editor with an empty policy
bpe <policy.hcl>                                                 Start the interactive editor, opening a policy file
bpe validate <policy.hcl>                                        Validate a policy without starting the interactive editor
bpe format <policy.hcl> [--check]                                Format a policy file, or check whether it is formatted
bpe test <policy.hcl> --path <path> --capability <capability>    Simulate an effective-access check
bpe help [command]                                                Show help, optionally for one command
bpe --help / bpe <command> --help                                 Show help
bpe --version                                                      Show version information
```

`--path` and `--capability` may appear before or after the policy file, and so
may `format`'s `--check`. Global configuration flags (`--address`, `--token`,
`--namespace`, `--ca-cert`, `--ca-path`, `--client-cert`, `--client-key`,
`--tls-server-name`, `--skip-verify`) are accepted anywhere on the command
line — see [Configuration](#configuration).

**Currently implemented:** every command above. Argument parsing and
validation, `--help`/`help`, `bpe <command> --help`, `--version`,
`bpe validate <policy.hcl>`, `bpe format <policy.hcl> [--check]`,
`bpe test <policy.hcl> --path <path> --capability <capability>`, and the
interactive editor (`bpe` / `bpe <policy.hcl>`) all behave as documented
below. OpenBao connectivity is not implemented — no command in this
release contacts a server.

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

`bpe format` reads the file and canonicalizes its HCL formatting
(spacing, indentation, alignment) via `hclwrite.Format` — the same
token-level formatter HashiCorp tools like `terraform fmt` use. It never
decodes into the policy domain model, so it is gated on HCL syntax
validity alone: a syntactically valid file with an unknown attribute, an
attribute value the domain model can't parse (e.g. a non-RFC-3339
`expiration`), or contradictory permission constraints (the kind `bpe
validate` would flag) still formats successfully, with none of that
content lost. Only a genuine HCL syntax error is refused, printing the
same diagnostics `bpe validate` would and exiting `1`. An already-
formatted file is left untouched, including its modification time — it
is a true no-op, not a write of identical content. `--check` reports
whether the file would change without writing it (exit `0` already
formatted, `1` would reformat), and is the only mode that follows a
symlink; see [File writes](#file-writes) for what writing "in place"
does and does not guarantee.

`bpe` and `bpe <policy.hcl>` open the interactive editor — see
[Interactive editor](#interactive-editor) for what it does, how it edits,
and what it refuses to do.

### Interactive editor

`bpe` opens an empty policy; `bpe <policy.hcl>` opens that file. Opening a
file is a read — nothing is written and the file's modification time is
untouched.

**It edits the file you opened rather than regenerating it.** Every change
is applied to the parsed HCL token stream, so a comment, an attribute BPE
does not model, an unsupported nested block, and the file's own formatting
all survive an edit untouched. Only the attribute you actually changed is
rewritten. Opening a rule and accepting the form without changing anything
produces a byte-identical file.

The consequence worth knowing: a value BPE cannot reproduce faithfully is
not rewritten at all. A variable reference, an interpolated string, a
heredoc, or an expression carrying its own comment makes that **one field**
read-only, with the reason shown beside it; the rest of the rule, and the
rest of the file, stay editable. A capability BPE does not recognize has
no checkbox, is never dropped, and is named on screen so you know it is
there.

A file whose HCL does not parse opens **read-only**, with its diagnostics
on display. It can be browsed, previewed, and inspected, but not edited —
BPE will not rewrite a file it could not fully read.

#### Screens

| Key | Screen |
| --- | --- |
| *(start)* | Rule list and the selected rule's details |
| `enter` / `a` | Edit or add a rule — path, all nine capabilities, comment, and an **advanced** section for expiration and the three parameter constraints |
| `p` | HCL preview — the document exactly as it would be written |
| `g` | Validation diagnostics, with severity and source line |
| `t` | Effective-access test, run against the policy including unsaved edits |
| `s` | Save review — a diff of what is about to be written against what was read |
| `?` | Full key reference |

Every screen lists the keys it accepts in a footer. On a narrow terminal
the footer abbreviates its labels rather than dropping entries, so every
key stays visible.

#### Keyboard and mouse controls

| Key | Does |
| --- | --- |
| `↑` / `↓` | Move the selection (`j` / `k` also work) |
| `tab` / `shift+tab` | Move between fields in a form |
| `pgup` / `pgdn`, `home` / `end` | Page or jump through a long view |
| `enter` | Open the selected rule, or the highlighted field |
| `space` | Toggle the highlighted capability |
| `esc` | Go back, or cancel the current form |
| `a` | Add a rule |
| `d` | Duplicate the selected rule, comments and all |
| `x` | Remove the selected rule |
| `ctrl+d` | Unset a field in the rule form — removes the attribute rather than emptying it |
| `ctrl+s` | Apply the form, or write the file from the review screen |
| `q` / `ctrl+c` | Quit, with confirmation when there are unsaved changes |

Mouse: click a rule to select it, and use the wheel to scroll a long view.
Everything the mouse does has a key that does the same thing — mouse input
is never required.

#### Absent, empty, and populated are three different things

`comment = ""` and no `comment` attribute are different files, and BPE
keeps them apart. An optional field shows `(not set)` when the attribute
is absent and `"" (set, but empty)` when it is present and empty. `ctrl+d`
removes the attribute; clearing its text does not.

The same distinction applies to parameter constraints, where an empty
value list is meaningful to OpenBao rather than merely blank: under
`allowed_parameters` it permits any value for that parameter, and under
`denied_parameters` it forbids the parameter entirely. The parameter
subform says so on screen.

#### Saving

`s` opens the save review: a line-level diff of the document against the
file as it was read. Nothing is written until `ctrl+s` on that screen, so
anything that would disappear — a comment removed along with the rule it
documented, for instance — is visible first.

Writing goes through the same `internal/fileio` path `bpe format` uses,
with the same guarantees and the same caveats (see [File writes](#file-writes)).
If the file changed on disk since it was read, the write is **refused**:
your in-memory edits are kept, and the conflict dialog offers to show what
changed on disk before you decide between reloading and cancelling.

Removing a rule takes that rule's own leading comments with it, and the
confirmation says so and shows them. An orphaned comment left above the
*next* rule would read as documentation for that rule, which in an
access-control file is worse than an untidy leftover.

A document started with bare `bpe` has no file yet. Saving it asks for a
path, and BPE will create that file but **will not replace an existing
one** — it has not read that file, so it cannot tell an earlier version of
this policy from something unrelated.

#### Terminal behavior

- The editor runs full-window (alternate screen) and restores the terminal
  on a normal quit, on an interrupt, and on a recovered panic.
- `SIGINT` / `SIGTERM` exit `130`. `ctrl+c` typed in the editor behaves
  like `q`: it asks before discarding unsaved work.
- No state is communicated by color alone. Every colored thing also carries
  a word or a glyph — `modified`, `read-only`, `[x]`, `DENY`, `error` — so
  the interface reads correctly with `NO_COLOR` set. Colors are terminal
  palette indices rather than fixed hex values, so they follow your own
  theme on a light or a dark background.
- The layout stacks the rule list above the details panel below 80
  columns, and no screen emits a line wider than the terminal at any width.

### File writes

`bpe format` and the interactive editor's save are the two things that
write to a local file, and both go through the same `internal/fileio`
package; any future local side of a remote edit (FSM-16) will reuse it too
rather than duplicating this logic.

- **Atomic.** A write goes to a temporary file in the same directory,
  is `fsync`ed, has its permissions set, and is renamed over the
  target — a reader never observes a half-written file. The temporary
  file is removed on any failure before the rename; the target itself is
  never deleted as a fallback.
- **Permission-preserving.** The target's existing Unix permission bits
  are carried onto the new content. Extended attributes and ACLs are
  **not** preserved.
- **Conflict-detecting, not a true compare-and-swap.** Before writing,
  a write re-checks the file's on-disk state against a snapshot
  taken when it was read — content hash and file identity, not just size
  and modification time, so a same-size edit within one mtime tick, a
  deletion, a replacement, or a permission change are all caught (exit
  `4`). This narrows, but cannot close, the window between that check
  and the rename: an uncooperative concurrent writer can still race it.
  Treat it as a good-faith detector for the ordinary case — another
  editor or another `bpe` invocation touching the same file — not a
  guarantee against a deliberate race.
- **Crash-durable, with one caveat.** The rename itself is atomic on
  both of BPE's supported platforms (Linux, macOS), and the containing
  directory is `fsync`ed afterward where the platform supports it, so
  the replacement is durable across a crash, not merely atomic in
  memory. If that post-rename step fails, BPE reports that the file
  **was** replaced — never that nothing happened.
- **Symlinks and hard links.** Writing in place refuses a symlink
  outright (exit `3`) rather than following it or replacing the link
  itself — `--check` may still follow one, since it never writes.
  Writing also refuses a file reliably detected to have more than one
  hard link (Unix platforms only; undetectable elsewhere, in which case
  this check is silently skipped).

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

## Remote policies

BPE can read and write policies on an OpenBao server as well as local
files. The client lives in `internal/baoclient`; **no command in this
release uses it yet** — wiring it into the editor is FSM-17 — so nothing
BPE currently does contacts a server.

### What it needs from a token

| Operation | Capability on |
| --- | --- |
| List policies | `list` on `sys/policies/acl` |
| Read a policy | `read` on `sys/policies/acl/*` |
| Create or update | `create` and `update` on `sys/policies/acl/*` |
| Delete | `delete` on `sys/policies/acl/*` |

A rejected token (401) and a token missing a capability (403) are reported
differently, because they lead to different fixes.

### Conflict safety

Updates use OpenBao's own check-and-set rather than anything BPE invents.
Reading a policy captures its `version`; updating sends that version back
as `cas`, and the server refuses the write if the policy has changed since
— exit code `4`.

Three consequences worth knowing:

- **An update is PATCH, not POST.** OpenBao resets unspecified fields to
  their defaults on POST and preserves them on PATCH. A policy can carry
  an `expiration`, a `ttl`, `cas_required`, or the identity-template
  flags, none of which BPE models — so POST would silently clear whichever
  of them were set. BPE sends exactly two fields: `policy` and `cas`.
- **A create is POST with `cas = -1`**, OpenBao's "this must not already
  exist" value. A policy that appeared between listing and writing is a
  conflict, not something to overwrite.
- **`cas_required` is read but never sent.** It is the server's own
  per-policy setting, and turning it on would change the rules for every
  other client of that policy on BPE's say-so.

**If a server does not return usable version metadata, an update is
refused** rather than attempted. BPE does not fall back to re-reading and
comparing before writing: that sequence has a race between the compare and
the write, so it would report an update as conflict-safe when it was not.

**Deletion has no equivalent.** This endpoint offers no check-and-set for
DELETE, so a delete cannot be made atomic against a concurrent change. BPE
says so rather than implying a safety it cannot provide, and the
confirmation for a delete belongs in the interface that asks for it.

One detail is undocumented upstream: OpenBao's API reference states
neither the HTTP status code nor the error body for a failed
check-and-set. BPE's detection is therefore deliberately broad — 409 and
412 on their own, and 400 only when the body names a check-and-set
problem — and biased toward reporting a conflict, since a conflict
misreported as a bad request invites someone to force the write.

### Security behavior

- The token is held in a type that redacts itself under every formatting
  verb, is sent only as the `X-Vault-Token` header, and appears in no
  error, no log line, and nothing written to disk. Errors leaving the
  client are additionally scrubbed of it, so a server that echoes the
  token back inside its own error message cannot leak it through BPE.
- Certificate verification is **on** unless `--skip-verify` /
  `BAO_SKIP_VERIFY` explicitly turns it off, and doing so produces a
  warning that says what it exposes rather than noting that verification
  is off.
- BPE reads no secret values and never persists a token.
- `BAO_MAX_RETRIES` has no effect. BPE resolves its own configuration and
  disables the OpenBao client's separate environment reading, so that the
  precedence documented under [Configuration](#configuration) is the only
  precedence in play; the retry count is a fixed, deliberate 2 for a 5xx
  or a connection failure, and a 4xx is never retried.

## Exit Codes

| Code | Meaning | Status |
| --- | --- | --- |
| `0` | Successful command, or `--help`/`help`/`--version` output | Active |
| `1` | `validate`: a validation failure. `test`: a denied result, or a policy it cannot trust enough to simulate against (unsupported content or decode errors). `format`: a genuine HCL syntax error, or — with `--check` only — the file would be reformatted. | Active for `validate` (FSM-12), `test` (FSM-13), and `format` (FSM-14) — corrected from an earlier, mistaken "reserved for FSM-17" note; FSM-17 is TUI/remote integration, unrelated to this exit code |
| `2` | Command-line usage or configuration error | Active |
| `3` | Operational failure: a policy file that could not be read, a `test` result `bpe` cannot reduce to a trustworthy decision from path and capability alone (see [Evaluation limits](#evaluation-limits)), `format` refusing to write through a symlink or a detected hard-linked file, or the interactive editor failing to start | Active |
| `4` | A detected write conflict: the local file changed on disk between being read and being written (see [File writes](#file-writes)), or a remote check-and-set write was rejected because the policy changed on the server (see [Remote policies](#remote-policies)). The interactive editor reports the local case on screen and keeps your edits rather than exiting | Active |
| `130` | Interrupted by the user (Ctrl+C or SIGTERM) | Active |

## Common Commands

```bash
# Install the configured toolchain
mise install

# Open the interactive editor on an empty policy
go run ./cmd/bpe

# Open the interactive editor on a policy file
go run ./cmd/bpe policy.hcl

# Build
go build -o ./bin/bpe ./cmd/bpe

# Format a policy file, or check whether it needs formatting (CI)
go run ./cmd/bpe format policy.hcl
go run ./cmd/bpe format policy.hcl --check

# Format Go source
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
│   ├── tui/                 # Bubble Tea v2 interactive editor — screens, forms, styles
│   ├── policy/               # UI-independent policy domain model
│   ├── hclpolicy/            # HCL parsing, validation, generation, and surgical editing
│   ├── evaluator/             # Matching and effective-access simulation
│   ├── baoclient/             # OpenBao API client — policies, check-and-set, no terminal dependency
│   ├── config/                # Environment and file configuration
│   ├── fileio/                 # Safe local file persistence — atomic writes, conflict detection
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

The TUI collects user actions and renders policy rules but does not implement policy semantics itself. The `policy` package holds the UI-independent domain model. `hclpolicy` translates between that model and HCL — and edits HCL in place, applying one attribute, label, or block change to the parsed token stream so that everything it was not asked to change survives byte for byte; the TUI is an editable projection of that document rather than a second copy of it. `evaluator` determines effective access using OpenBao path-matching and capability rules. `baoclient` lists, retrieves, and updates remote policies through the OpenBao API, using the endpoint's native check-and-set so an update cannot silently overwrite someone else's change; it deliberately depends on nothing terminal-related, so the editor is built against its interface rather than the other way round. `fileio` reads a local file together with a snapshot of its on-disk state and later replaces it only if that state has not changed — the shared local-persistence primitive `bpe format` uses today and the TUI's save and `baoclient`'s local side will reuse later; it is local-filesystem only; remote OpenBao CAS handling stays `baoclient`'s concern. `config` resolves command-line and environment configuration. `apperr` defines the structured application error and exit-code contract shared by the CLI and the TUI. `cmd/bpe` itself is split into argument parsing (`cli.go`), configuration-independent command dispatch (`dispatch.go`), help text (`help.go`), and a thin `main.go` that wires a signal-derived context and is the only place that calls `os.Exit`. This separation allows the parser, evaluator, client, and CLI dispatch to be tested without running the terminal interface or invoking a subprocess.

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
- Tests must not require access to a live OpenBao server unless explicitly marked as integration tests. `internal/baoclient` is tested entirely against `httptest`: no test in it needs a server, a network, or a credential, and the token it uses is an obviously synthetic string.
- CLI argument parsing, configuration precedence, and command dispatch (help/usage/exit codes/cancellation) are covered by table-driven tests in `cmd/bpe` and `internal/config` that call Go functions directly rather than invoking a subprocess. A small number of process-level tests in `cmd/bpe` verify end-to-end exit-code behavior through the compiled binary.
- `internal/fileio`'s atomic write, permission preservation, and conflict detection (content change, deletion, replacement, permission change) are covered by table-driven tests using `t.TempDir()`; none touch a real user file.
- `internal/hclpolicy`'s editing layer carries losslessness tests: a no-op open and close, comments before, inside and after a block, unknown top-level and in-block content, duplicate path blocks, expression-valued attributes, and each of rename, capability toggle, add, duplicate and remove. Each asserts on the resulting bytes, because "nothing was lost" is a statement about the file rather than about the domain model.
- `internal/tui` is tested by driving the root model's `Update` with synthesized key presses, exactly as the runtime would: navigation, applying and cancelling a form, add, duplicate, remove-with-confirmation, quit-with-unsaved-changes, the save review, an external-change conflict with the edits retained, and a read-only document refusing every editing action. No test needs a TTY, and none touches a file outside its own `t.TempDir()`.
- Every screen is rendered at 40, 60 and 100 columns and asserted not to emit a line wider than the terminal.
- `internal/baoclient` pins its two security properties rather than describing them: the token appears in no error across every failure path and every operation — including when the server echoes it back, parseably or not — in no formatting verb on the client, in no log output, and in nothing written to disk; and the package imports nothing terminal-related, checked by parsing its own imports.

## Build and Release

Releases are planned as cross-platform binaries using GoReleaser.

## Deployment

There is no deployment process because BPE is a local executable.

## Operations / Runbook

See [`RUNBOOK.md`](RUNBOOK.md) for operational procedures.

## Troubleshooting

Common connection, TLS, terminal-rendering, logging, and recovery procedures belong in [`RUNBOOK.md`](RUNBOOK.md). Operational guidance there is still under development until those features exist.

**Current limitations:** no command in this release contacts a server. The OpenBao client exists and is tested (FSM-16, `internal/baoclient` — see [Remote policies](#remote-policies)), but nothing calls it yet; joining it to the editor is FSM-17, and until then the `BAO_*`/`VAULT_*` configuration is resolved and unused. HCL parsing (FSM-11), policy validation (FSM-12, `bpe validate`), effective-access simulation (FSM-13, `bpe test`), safe local-file formatting and persistence (FSM-14, `bpe format`, `internal/fileio`), and the interactive editor (FSM-15) are implemented — `bpe test` with the scope limits in [Evaluation limits](#evaluation-limits), and local writes with the guarantees and caveats in [File writes](#file-writes).

Three limits of the editor worth knowing before you meet them:

- A field whose value is not a plain literal is read-only, with the reason shown. That is deliberate: rewriting it would replace the expression with whatever it happened to evaluate to.
- The effective-access screen refuses to answer for a policy containing content BPE cannot fully represent, for the same reason `bpe test` does — the decoded policy would be only part of what OpenBao enforces. The diagnostics screen names what.
- A duplicated rule is appended at the end of the file rather than inserted after the original, because inserting mid-file would mean re-emitting every block after it.

## Security

Do not report suspected security vulnerabilities in a public issue. Use GitHub's private vulnerability reporting feature when available. BPE processes authentication tokens and authorization policies; diagnostic output and bug reports must not contain tokens, credentials, or secret values.

A dedicated `SECURITY.md` can be added before the first public release.

## Ownership and Support

Bao Policy Editor is currently maintained by Kevin Inscoe. Community issues and pull requests are welcome. Support is provided on a best-effort basis through GitHub Issues; the project currently has no commercial support or response-time guarantee.

## Related Documentation

- [Runbook](RUNBOOK.md)

## License

Licensed under the [Mozilla Public License 2.0](LICENSE) (MPL-2.0).
