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
| **Last Tested**       | Not yet tested — no implemented features            |
| **Expected Duration** | N/A                                                 |
| **Risk Level**        | Medium                                              |
| **Repo**              | <https://github.com/kevinpinscoe/bao-policy-editor> |

---

## Purpose

This runbook covers how to build, run, and safely interrupt Bao Policy Editor (BPE) as it exists today. BPE is **Experimental** (see `README.md`) and currently ships only a build/run stub — this document is intentionally minimal and will grow section by section as real features land. Connection handling, TLS, recovery, and policy-conflict procedures are **not documented here yet** because those features do not exist yet; documenting them now would describe behavior that doesn't exist.

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

**Why:** the current entry point is a stub; this confirms the toolchain and module are working.

```bash
mise install
go build -o ./bin/bpe ./cmd/bpe
./bin/bpe
```

**Expected output:**

```text
bao-policy-editor: not yet implemented
```

**If this fails:** confirm `go version` matches the `go 1.27` line in `go.mod`, and that `mise install` completed without error.

---

### Step 2 — Exit without saving (safe interruption)

**Why:** BPE has no autosave implemented yet. Nothing is written back to a local policy file or to OpenBao until an explicit save action completes and that save action does not exist yet either.

```bash
# Ctrl+C, or kill the process
```

**Expected output:** the process exits; no file on disk or policy in OpenBao is modified.

**If this fails:** N/A — there is currently nothing for an interrupted session to leave in an inconsistent state.

---

## Credential Handling

`BAO_TOKEN` and any other `BAO_*` credential-bearing environment variables must never appear in logs, diagnostic output, terminal screenshots, or bug reports. See `README.md`'s Security section for the vulnerability-reporting process.

---

## Maintenance Notes

- **Last game-day test:** none yet — no features to test.
- **Next scheduled review:** when the first real TUI feature (local policy load/edit) lands.
- **Known drift risks:** this runbook describes a build/run stub only; connection, TLS, recovery, and policy-conflict procedures must be added as those features are implemented, not backfilled from assumption.
