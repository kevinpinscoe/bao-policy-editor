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

| Field                 | Value                                               |
| --------------------- | --------------------------------------------------- |
| **Owner**             | Kevin Inscoe                                        |
| **Last Updated**      | 2026-09-16                                          |
| **Last Tested**       | 2026-09-16 — build, CLI, and interruption verified  |
| **Expected Duration** | N/A                                                 |
| **Risk Level**        | Medium                                              |
| **Repo**              | <https://github.com/kevinpinscoe/bao-policy-editor> |

---

## Purpose

This runbook covers how to build, run, and safely interrupt Bao Policy Editor (BPE) as it exists today. BPE is **Experimental** (see `README.md`) and currently ships an application foundation — CLI argument parsing, command dispatch, configuration resolution, and signal handling — with no policy domain behavior yet; every command either parses its arguments correctly and reports "not implemented yet", or shows help/version output. This document is intentionally minimal and will grow section by section as real features land. Connection handling, TLS, recovery, and policy-conflict procedures are **not documented here yet** because those features do not exist yet; documenting them now would describe behavior that doesn't exist.

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

## Credential Handling

`BAO_TOKEN` and any other `BAO_*` credential-bearing environment variables must never appear in logs, diagnostic output, terminal screenshots, or bug reports. `internal/config.Config` holds the resolved token as a `SensitiveString`, a type whose formatting is redacted under every `fmt` verb (`%v`, `%+v`, `%#v`, `%s`, `%q`) and in error messages; only an explicit `.Reveal()` call returns the raw value, reserved for the OpenBao client landing in a later ticket. See `README.md`'s Security section for the vulnerability-reporting process.

---

## Maintenance Notes

- **Last game-day test:** 2026-09-16 — build, `--help`/`--version`, a usage error, an unimplemented-command error, and Ctrl+C/SIGTERM interruption all manually exercised against the built binary (FSM-10).
- **Next scheduled review:** when the first real TUI feature (local policy load/edit) lands.
- **Known drift risks:** this runbook describes the CLI/application foundation only — no HCL parsing, validation, formatting, capability testing, file persistence, or OpenBao connectivity exists yet. Connection, TLS, recovery, and policy-conflict procedures must be added as those features are implemented, not backfilled from assumption.
