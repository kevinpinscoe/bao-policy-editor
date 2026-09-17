---
title: RUNBOOK.md — bao-policy-editor
tags: [runbook, operations]
vault_link: runbooks/home-kinscoe-projects-public-bao-policy-editor.md
source_path: /home/kinscoe/Projects/public/bao-policy-editor/RUNBOOK.md
---

> 📓 Indexed in the PKM knowledge vault at `runbooks/home-kinscoe-projects-public-bao-policy-editor.md` (symlink → this file).

# RUNBOOK.md — bao-policy-editor

> Before committing, run `mdlint` (or `mdlint-fix`) against this file — see
> `when-creating-a-runbook.md` step 7 and `when-building-or-scaffolding-code-in-a-git-repo.md` §7.

## Metadata

| Field                 | Value                                                                            |
| --------------------- | -------------------------------------------------------------------------------- |
| **Owner**             | Kevin Inscoe                                                                     |
| **Last Updated**      | 2026-09-17                                                                       |
| **Last Tested**       | 2026-09-17 — build, CLI, `validate`, `format`, `test`, and interruption verified |
| **Expected Duration** | N/A                                                                              |
| **Risk Level**        | Medium                                                                           |
| **Repo**              | <https://github.com/kevinpinscoe/bao-policy-editor>                              |

---

## Purpose

This runbook covers how to build, run, validate a policy with, format a policy file with, simulate an effective-access check against, and safely interrupt Bao Policy Editor (BPE) as it exists today. BPE is **Experimental** (see `README.md`) and currently ships an application foundation — CLI argument parsing, command dispatch, configuration resolution, and signal handling — plus HCL parsing, semantic policy validation (`bpe validate`), safe local-file formatting and persistence (`bpe format`, `internal/fileio`), and effective-access simulation (`bpe test`); the interactive editor still parses its arguments correctly and reports "not implemented yet", or shows help/version output. This document is intentionally minimal and will grow section by section as real features land. Connection handling, TLS, and remote-policy-conflict procedures are **not documented here yet** because those features do not exist yet; documenting them now would describe behavior that doesn't exist. Local-file conflict handling (a policy file that changed on disk since it was read) is documented below, in Step 6.

---

## When to Use This Runbook

- **Use when:** building BPE from source, running it locally, or safely exiting a session.
- **Do NOT use when:** looking for usage, development, or architecture documentation — see `README.md` for that.

---

## Prerequisites

- [ ] Go 1.27 available (via `mise`)
- [ ] `mise` installed
- [ ] Run `mise install && mise doctor` before proceeding

---

## Stack

| Component                   | Details                                                                    |
| --------------------------- | -------------------------------------------------------------------------- |
| **Language / Runtime**      | Go 1.27                                                                    |
| **External Services**       | OpenBao — optional, only for remote policy operations                      |
| **Databases / File Stores** | Local HCL policy files                                                     |
| **Credentials / Secrets**   | `BAO_TOKEN` (and related `BAO_*` env vars) — see Credential Handling below |

---

## Step-by-Step Procedure

### Step 1 — Build and run locally

**Why:** confirms the toolchain, module, and CLI dispatch are all working.

```bash
mise install
go build -o ./bin/bpe ./cmd/bpe
./bin/bpe
```

**Expected output:**

```text
the interactive policy editor is not implemented yet
```

exit code `3`. `./bin/bpe --version` prints a version line and exits `0`; `./bin/bpe --help` prints the full command reference to standard output and exits `0`.

**If this fails:** confirm `go version` matches the `go 1.27` line in `go.mod`, and that `mise install` completed without error.

---

### Step 2 — Exit without saving (safe interruption)

**Why:** BPE has no autosave implemented yet. Nothing is written back to a local policy file or to OpenBao until an explicit save action completes and that save action does not exist yet either. `cmd/bpe/main.go` derives its context from `signal.NotifyContext` (Ctrl+C / SIGINT and SIGTERM), so any running command is cancelled cleanly rather than left in an unknown state.

```bash
# Ctrl+C, or kill the process
```

**Expected output:** the process exits with code `130`; no file on disk or policy in OpenBao is modified.

**If this fails:** N/A — there is currently nothing for an interrupted session to leave in an inconsistent state.

---

### Step 3 — Diagnose a usage error

**Why:** confirms argument validation is catching mistakes before any command runs, with the documented exit code.

```bash
./bin/bpe validate
```

**Expected output:** a one-line error on standard error naming the missing argument, followed by a usage summary line, exit code `2`. See README.md's [Exit Codes](README.md#exit-codes) for the full contract.

**If this fails:** confirm the binary was rebuilt after the latest source changes (`go build -o ./bin/bpe ./cmd/bpe`).

---

### Step 4 — Diagnose a policy validation result

**Why:** confirms `bpe validate` reads the file, reports findings with their severity, and exits with the documented code rather than silently passing or failing.

```bash
./bin/bpe validate testdata/policies/valid.hcl
./bin/bpe validate testdata/policies/invalid_syntax.hcl
```

**Expected output:** for `valid.hcl`, one `warning: ... grants broad access under sys/` line (from its third rule's `sys/policies/acl/team-a-*`) and exit code `0` — warnings never fail validation. For `invalid_syntax.hcl`, one or more `filename:line:column: error: ...` lines on standard output and exit code `1`. A missing or unreadable file prints `failed to read policy file: ...` on standard error and exits `3`.

**If this fails:** confirm the binary was rebuilt (`go build -o ./bin/bpe ./cmd/bpe`), and that the path given actually exists relative to the current working directory.

---

### Step 5 — Simulate an effective-access check

**Why:** confirms `bpe test` reads the file, compiles it into
`internal/evaluator`, simulates the requested path and capability, prints
a structured explanation, and exits with the documented code — including
refusing to answer when it cannot trust the result (see README.md's
[Evaluation limits](README.md#evaluation-limits)).

```bash
./bin/bpe test testdata/policies/valid.hcl --path secret/metadata/team-a/foo --capability list
./bin/bpe test testdata/policies/valid.hcl --path secret/data/team-b/foo --capability read
./bin/bpe test testdata/policies/valid.hcl --path secret/data/team-a/foo --capability read
```

**Expected output:** the first command matches `valid.hcl`'s second rule
(`secret/metadata/team-a/*`, `list`/`read`) — `result: ALLOWED`, exit
code `0`. The second matches nothing — `result: DENIED`,
`stage: no match (default deny)`, exit code `1`. The third matches the
first rule (`secret/data/team-a/*`), which carries
`required_parameters`/`allowed_parameters`/`denied_parameters` — `bpe
test` supplies no request parameters, so it cannot trust an allow/deny
answer here: `result: INCOMPLETE`, exit code `3`, not a misleading
`ALLOWED`.

`./bin/bpe test testdata/policies/valid.hcl --path secret/data/team-a/foo --capability frobnicate`
exits `2` (unknown capability, a usage error caught before the file is
even read).

**If this fails:** confirm the binary was rebuilt
(`go build -o ./bin/bpe ./cmd/bpe`), and that `--path`/`--capability`
were both supplied.

---

### Step 6 — Format a policy, and recover from a write conflict

**Why:** confirms `bpe format` canonicalizes HCL formatting safely — refusing only on a
genuine syntax error, never on semantic content it can't fully represent — and that a
policy file changed on disk since it was read is detected rather than silently overwritten.

```bash
cat > /tmp/bpe-format-demo.hcl <<'EOF'
path   "secret/data/example"    {
capabilities=["read"]
expiration="not-a-timestamp"
}
EOF
./bin/bpe format /tmp/bpe-format-demo.hcl --check
./bin/bpe format /tmp/bpe-format-demo.hcl
cat /tmp/bpe-format-demo.hcl
./bin/bpe format /tmp/bpe-format-demo.hcl --check
```

**Expected output:** the first `--check` reports `not formatted` and exits `1` (the
inconsistent spacing above is not canonical). The plain `format` reports `formatted`, exits
`0`, and rewrites the file to canonical spacing and indentation — the
`expiration = "not-a-timestamp"` line is still present afterward, unchanged in meaning: a
decode-time error (this timestamp is not RFC 3339) never blocks or loses content, only a
genuine HCL syntax error does
(`./bin/bpe format testdata/policies/invalid_syntax.hcl` demonstrates that refusal, exit `1`,
file untouched). The second `--check` reports `already formatted` and exits `0`.

To see the write-conflict refusal (exit `4`):

```bash
cp testdata/policies/valid.hcl /tmp/bpe-conflict-demo.hcl
# In one terminal, pause bpe format after it has read the file but
# before it writes — not reproducible as a single copy-paste command,
# since the window is intentionally narrow (see README.md's File
# writes). The reliable way to exercise this is internal/fileio's own
# test suite: go test ./internal/fileio/... -run TestReplace_ConflictDetection -v
```

**If this fails:** confirm the binary was rebuilt (`go build -o ./bin/bpe ./cmd/bpe`), and
that `/tmp/bpe-format-demo.hcl` is writable.

---

## Credential Handling

`BAO_TOKEN` and any other `BAO_*` credential-bearing environment variables must never appear in logs, diagnostic output, terminal screenshots, or bug reports. `internal/config.Config` holds the resolved token as a `SensitiveString`, a type whose formatting is redacted under every `fmt` verb (`%v`, `%+v`, `%#v`, `%s`, `%q`) and in error messages; only an explicit `.Reveal()` call returns the raw value, reserved for the OpenBao client landing in a later ticket. See `README.md`'s Security section for the vulnerability-reporting process.

---

## Maintenance Notes

- **Last game-day test:** 2026-09-17 — build, `--help`/`--version`, a usage error, an unimplemented-command error (interactive editor only), `bpe validate` against a clean policy, a policy with only warnings, a policy with a semantic error, a policy with a syntax error, and a missing file, `bpe format` reformatting a misindented policy, `--check` reporting both "not formatted" and "already formatted" without writing, formatting refusing a genuine syntax error while still formatting a policy with a decode-time error (bad `expiration`) unchanged in meaning, `bpe test` against an allowed request, a denied (default-deny) request, a request whose winning rule carries parameter constraints (`INCOMPLETE`, exit `3`), an unknown-capability usage error, a broader-deny-does-not-override-a-more-specific-allow regression, and a missing file, and Ctrl+C/SIGTERM interruption, all manually exercised against the built binary (FSM-14 added `format`; FSM-13 added `test`; FSM-12 covered everything but those two).
- **Next scheduled review:** when the next real feature (the visual terminal policy editor, FSM-15) lands.
- **Known drift risks:** the interactive editor and OpenBao connectivity do not exist yet. Connection, TLS, and remote policy-conflict procedures must be added as those features are implemented, not backfilled from assumption. `bpe validate`'s KV v2 and list/scan-prefix checks are same-file heuristics only — they have no access to a policy's real OpenBao mount configuration, so they can both miss real problems and flag paths that are actually fine; treat their output as guidance, not ground truth. `bpe test` has its own, separate scope limits — see README.md's [Evaluation limits](README.md#evaluation-limits) — and its expiration handling is a snapshot at the moment it runs, not live (README.md's [Expiration is a snapshot, not live](README.md#expiration-is-a-snapshot-not-live)). `bpe format`'s write-conflict detection narrows but does not close the check-then-rename race, and does not preserve ACLs or extended attributes — see README.md's [File writes](README.md#file-writes). Hard-link detection is Unix-only and silently skipped where unavailable.
