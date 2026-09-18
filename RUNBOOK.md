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

| Field                 | Value                                                                                                    |
| --------------------- | -------------------------------------------------------------------------------------------------------- |
| **Owner**             | Kevin Inscoe                                                                                             |
| **Last Updated**      | 2026-09-17                                                                                               |
| **Last Tested**       | 2026-09-17 — build, CLI, `validate`, `format`, `test`, the interactive editor, and interruption verified |
| **Expected Duration** | N/A                                                                                                      |
| **Risk Level**        | Medium                                                                                                   |
| **Repo**              | <https://github.com/kevinpinscoe/bao-policy-editor>                                                      |

---

## Purpose

This runbook covers how to build, run, validate a policy with, format a policy file with, simulate an effective-access check against, edit interactively, and safely interrupt Bao Policy Editor (BPE) as it exists today. BPE is **Experimental** (see `README.md`) and currently ships an application foundation — CLI argument parsing, command dispatch, configuration resolution, and signal handling — plus HCL parsing, semantic policy validation (`bpe validate`), safe local-file formatting and persistence (`bpe format`, `internal/fileio`), effective-access simulation (`bpe test`), and the interactive terminal editor (`bpe`, `bpe <policy.hcl>`). The OpenBao client exists as of FSM-16 (`internal/baoclient`) but **no command calls it yet** — joining it to the editor is FSM-17. So connection, TLS and remote-conflict *procedures* still describe nothing a user can run today, and are deliberately not written up as steps here; what the client does and refuses to do is documented in README.md's [Remote policies](README.md#remote-policies) section, and Step 10 below records how to exercise it without a server. Local-file conflict handling (a policy file that changed on disk since it was read) is documented below, in Steps 6 and 8.

---

## When to Use This Runbook

- **Use when:** building BPE from source, running it locally, recovering an editing session from a file that changed on disk, or safely exiting a session.
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
./bin/bpe --version
./bin/bpe --help
```

**Expected output:** `--version` prints a version line and exits `0`; `--help` prints the full command reference to standard output and exits `0`. Bare `./bin/bpe` opens the interactive editor on an empty policy — see Step 7.

**If this fails:** confirm `go version` matches the `go 1.27` line in `go.mod`, and that `mise install` completed without error.

---

### Step 2 — Exit without saving (safe interruption)

**Why:** BPE has no autosave. Nothing reaches a local policy file until an explicit save completes, so an interrupted session can never leave a half-written policy behind — `internal/fileio` writes to a temporary file and renames it into place, and the rename either happened or it did not. `cmd/bpe/main.go` derives its context from `signal.NotifyContext` (Ctrl+C / SIGINT and SIGTERM), so any running command is cancelled cleanly rather than left in an unknown state.

```bash
# Ctrl+C, or kill the process
kill -TERM "$(pgrep -f 'bin/bpe')"
```

**Expected output:** the process exits with code `130`, the terminal is restored (cursor visible, alternate screen left, mouse reporting off), and no file on disk is modified. Unsaved editor changes are lost, which is what an interrupt means — the confirmation prompt only guards a deliberate `q` or `ctrl+c` typed inside the editor.

**If this fails:** if the terminal is left in raw mode — no echo, no line editing — run `reset` or `stty sane` to recover it, and treat that as a bug worth reporting: Bubble Tea restores the terminal on a normal quit, on an interrupt, and on a recovered panic, so a stuck terminal means one of those paths was missed.

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

### Step 7 — Run the interactive editor

**Why:** confirms the editor opens a policy without modifying it, renders
its screens, and leaves the terminal as it found it.

```bash
cp testdata/policies/comments.hcl /tmp/bpe-editor-demo.hcl
stat -c '%Y %s' /tmp/bpe-editor-demo.hcl
./bin/bpe /tmp/bpe-editor-demo.hcl
# then, in the editor: p (preview) · esc · g (diagnostics) · esc · ? (help) · esc · q
stat -c '%Y %s' /tmp/bpe-editor-demo.hcl
```

**Expected output:** the editor opens full-window showing the rule list,
the selected rule's details, and a footer naming the available keys. `q`
exits `0` with no confirmation, because nothing was changed. **Both `stat`
lines are identical** — opening a policy is a read, and BPE does not
rewrite a file merely because it looked at it.

Three things worth confirming while in there:

- `p` shows the file's own text, comments and all — not a regenerated
  version of it.
- A rule carrying an attribute BPE does not model says so in its details
  panel, and `g` lists it as a warning rather than dropping it — open
  `testdata/policies/unsupported_attribute.hcl` to see that, since the
  fixture above has no unknown attribute.
- `?` lists every key. The footer abbreviates on a narrow terminal but
  never drops an entry.

To confirm colour is not load-bearing:

```bash
NO_COLOR=1 ./bin/bpe /tmp/bpe-editor-demo.hcl
```

Every state the interface reports — `modified`, `read-only`, `[x]`,
`DENY`, `error` — is still legible, because each is a word or a glyph
rather than a colour.

**If this fails:** if the editor exits immediately with
`failed to open policy file`, the path does not exist or is not readable.
If it starts but the screen is garbled, confirm `TERM` is set to something
your terminal actually is; BPE requires UTF-8 but not colour.

---

### Step 8 — Recover an editing session from a file that changed on disk

**Why:** this is the one situation where the editor refuses to do what was
asked, and the recovery is not obvious from the screen alone.

```bash
cp testdata/policies/valid.hcl /tmp/bpe-conflict-demo.hcl
./bin/bpe /tmp/bpe-conflict-demo.hcl
# in the editor: press d to duplicate a rule (the document is now modified)
# in ANOTHER terminal, while the editor is still open:
#   echo '# changed underneath' >> /tmp/bpe-conflict-demo.hcl
# back in the editor: s, then ctrl+s
```

**Expected output:** the write is refused and a dialog says the file
changed on disk. **The in-memory edits are untouched** — nothing has been
written and nothing has been discarded. Three ways out:

| Key | Does |
| --- | ----- |
| `v` | Shows a diff of the file as it is on disk now against your version, so the decision is made with the change in front of you |
| `r` | Reloads from disk. **This discards your unsaved edits** and is the only destructive answer |
| `esc` | Cancels. You stay where you were, still modified, still unsaved |

There is no "overwrite anyway". To keep your version, copy it out of the
editor first: `p` shows the document exactly as it would be written, or
quit with `q` → `s` to write it somewhere else via the save-path screen.

**If this fails:** if the save succeeds when it should not have, the
snapshot comparison in `internal/fileio` is not doing its job — run
`go test ./internal/fileio/... -run TestReplace -v` and treat a passing
suite with a failing binary as a wiring problem in `internal/tui`'s save
path rather than in `fileio` itself.

---

### Step 9 — Save a policy that has no file yet

**Why:** confirms BPE creates a new file but never silently replaces one
it has not read.

```bash
rm -f /tmp/bpe-new-policy.hcl
./bin/bpe
# in the editor: a (add a rule) · enter · type a path · enter · ctrl+s
# then: s (review) · ctrl+s (write) · type /tmp/bpe-new-policy.hcl · enter
cat /tmp/bpe-new-policy.hcl
```

**Expected output:** the file is created with the rule that was added, and
the header switches from `(unsaved policy)` to the new path. Pointing the
same flow at a path that already exists is **refused**, with a message
saying BPE will not replace a file it has not read — choose another name,
or open that file and edit it instead.

**If this fails:** confirm the target directory exists and is writable.
The new file is created with mode `0600`; adjust it afterwards if the
policy is meant to be world-readable.

---

### Step 10 — Exercise the OpenBao client without a server

**Why:** the client is the one part of BPE that would otherwise need live
infrastructure to have any confidence in. It does not: every path through
it, including the failure paths, is reachable from the test suite.

```bash
go test ./internal/baoclient/... -v
```

**Expected output:** every test passes, in roughly four seconds, having
started no process and opened no connection beyond `httptest` servers on
the loopback interface. Nothing in the run reads a credential, a
configuration file, or an environment variable.

The tests worth knowing about by name, because they are the ones that
answer a question someone will eventually ask:

| Test | Answers |
| --- | --- |
| `TestUpdateSendsPostCarryingThePreviouslyReadVersion` | Is an update really conflict-safe, and does it use POST rather than the PATCH this endpoint does not implement? |
| `TestUpdatePreservesTheMetadataTheReadReported` | Does an update put back the `expiration` and `cas_required` a POST would otherwise clear? |
| `TestUpdateDistinguishesAbsentFromFalse` | Is "the server did not report this" kept apart from "the server reported false"? |
| `TestUpdateNeverSendsTtl` | Could repeated edits push a policy's expiry further out each time? |
| `TestAWriteWithNoVersionInItsResponseIsReadBack` | After a `204` with no body, does the session still have conflict protection for the next save? |
| `TestErrorsNameTheOperationExactlyOnce` | Does a failure read as "updating policy X: updating policy X failed"? |
| `TestUpdateSendsOnlyThePolicyAndCas` | Could an update quietly clear a policy's `expiration` or `ttl`? |
| `TestUpdateRefusesWithoutVersionMetadata` | What happens against a server that reports no version? |
| `TestStaleCasMapsToConflictAndExitCodeFour` | Is a rejected check-and-set recognized across the shapes it might arrive in? |
| `TestTheTokenNeverAppearsInAnError` | Can the token leak through an error, including one the server wrote? |
| `TestPackageHasNoTerminalDependency` | Is the client still usable headlessly? |

**One thing the suite cannot tell you.** OpenBao's API reference states
neither the HTTP status code nor the error body for a failed
check-and-set, so the detection is matched rather than read off a
documented contract (see README.md's [Remote policies](README.md#remote-policies)).
The tests cover the shapes a failure is expected to take; they cannot
prove a live server uses one of them. The first time BPE is pointed at a
real instance, deliberately provoke a stale write — read a policy, change
it from another client, then save — and confirm it comes back as a
conflict rather than as a generic failure.

**If this fails:** a failure in `TestAServerErrorIsRetried` is usually a
slow machine rather than a defect; that test is the only one that waits
out the real retry backoff, and it is the reason the suite takes seconds
rather than milliseconds.

### Step 11 — Work on an OpenBao server's policies

**Why:** remote mode is the only part of BPE that touches a network, and
everything that makes it safe is a gate the user has to pass through. This
is how to see each one.

```bash
export BAO_ADDR=https://openbao.example.com:8200
export BAO_TOKEN=...          # exported, never typed into BPE
bpe --remote
```

**Expected:** the policy browser, listing the policies the token can see.
`--remote` takes no file argument — `bpe --remote policy.hcl` and
`bpe --remote team-a` are both usage errors (exit `2`). Pressing `r` in the
editor does the same thing without restarting.

| To | Press | And expect |
| --- | --- | --- |
| Open a policy | `↑`/`↓`, `enter` | The ordinary editor, with `remote · <address>` in the header |
| Save a change | `s`, then `ctrl+s` | A diff first; nothing is sent until `ctrl+s`. A success leaves you in the editor, with the confirmation on the status line |
| Create a policy | `n`, name it, edit, `s`, `ctrl+s` | A create; a name already taken is refused, not converted to an update |
| Delete a policy | `x`, type the exact name, `enter` | Refusal on any other input |
| Abandon a slow request | `esc` | The request cancelled and nothing applied when it later returns |

**The token is never entered here.** The connect screen shows the address
and namespace as editable fields and the token only as `configured` or
`not configured`. If it says `not configured`, the fix is in the
environment, not on that screen.

**Verified against a live server**, 2026-09-18, on a disposable in-memory
OpenBao 2.5.2 bound to loopback, with disposable policies:

| Case | Result |
| --- | --- |
| Ordinary policy, edit and save | version advanced, body changed |
| Policy with an explicit `expiration` | `expiration` unchanged |
| Policy whose expiration came from `ttl` | same instant preserved; see the note below |
| Policy with `cas_required = true` | setting unchanged |
| Stale write provoked by a second client | presented as a conflict, not a generic failure; the refused write never reached the server |
| The server's version reviewed, then retried | the retry succeeded and replaced it |

The disposable policies were deleted afterwards and no credential reached
any file. To repeat it, run a `bao server -dev` on a loopback port, point
`BAO_ADDR` and `BAO_TOKEN` at it, and work through the table above by hand
— the interface is the thing under test, so there is no scripted form of
it in the repository.

**One thing that looks like a bug and is not.** A policy whose expiration
came from a `ttl` reads back in the server's local offset until its first
update, and in UTC afterwards — `…T16:29:20.691566319-04:00` becomes
`…T20:29:20.691566319Z`. That is the same instant to the nanosecond. BPE
sends the string exactly as the server gave it; the server normalizes it
on write, and it is byte-stable from then on. Confirmed by echoing the
same value with plain `curl`, which produces the identical change.

**Without a server to hand**, every one of these workflows is exercised
against an in-memory fake by the test suite, which is the faster way to
confirm behaviour after a change:

```bash
go test ./internal/tui/... -run 'Remote|Conflict|Delete|Token|SkipVerify|Local' -v
```

---

### Step 12 — Recover from a rejected remote write

**Why:** a refused write is the normal, healthy outcome of two people
editing one policy, and the recovery is a decision rather than a repair.
Nothing here is automatic.

**What you will see:** *The policy changed on the server* — the write was
refused because the policy is no longer at the version BPE read. Your
edits are intact and still marked modified. Three answers:

| Key | Does | Costs you |
| --- | --- | --- |
| `v` | Re-reads the server's copy and shows it as a diff against yours | Nothing — this only looks |
| `r` | Offers to take the server's version instead | Your edits, after a second confirmation |
| `esc` | Cancels | Nothing; the server is not written to |

**To keep your version**, press `v`, read the diff — it is the change you
would be replacing — and then `ctrl+s` from that screen. That retries with
the revision the re-read reported. The footer on that screen says
*replace the server's version with yours*, because that is what it does.

**To take theirs**, press `r` and confirm. This is the only path that
discards your edits, and it asks twice for that reason.

**If the message is instead** *this server did not report a version*, BPE
refused the update rather than attempting it: without version metadata
there is no conflict-safe write to be had, and a read-compare-write
fallback has a race in the middle. Re-open the policy and try again; if
the server never reports a version, remote updates are not safe on it and
BPE will keep refusing.

**If a create was refused** because the name is taken, that is a different
question with a different answer — BPE offers to open the existing policy.
Opening is a read, which produces a real revision, so the write after it
is a genuine conflict-checked update. The refused create is never retried
as one.

Opening it asks a second time first. The create was refused, so the draft
is not on the server, and it was never on disk either — opening the
server's copy replaces the only copy of it that exists. Decline, and the
draft is untouched and nothing is read; save it under another name to keep
it.

**Backing out of the conflict review does not leave a licence behind, and
neither does a retry that failed.** The revision that review's re-read
reported is handed to the one write it authorizes and is not kept
anywhere. Press Escape instead of confirming, or confirm and have the
write cancelled or fail, and BPE still holds the version it originally
read — so the next ordinary save is refused again and returns here. If you
meant to overwrite, go back through `s` → `ctrl+s` → `v` → `ctrl+s`.

That is worth knowing after a flaky connection in particular: a retry that
timed out has changed nothing, including BPE's idea of what version the
policy is at, so there is no state to clean up and nothing to check before
trying again.

---

### Step 13 — Diagnose a connection, TLS, or authorization failure

**Why:** four failures look similar from the outside and have entirely
different fixes.

| Message | Means | Fix |
| --- | --- | --- |
| *could not establish a trusted TLS connection* | The certificate did not verify, or the handshake failed | Point `BAO_CACERT`/`BAO_CAPATH` at the issuing CA, or `BAO_TLS_SERVER_NAME` at the name on the certificate |
| *the OpenBao token was rejected* (401) | The token is wrong, expired, or revoked | Get a new one; BPE cannot renew it |
| *the token lacks the capability this operation needs* (403) | The token is valid but not permitted | Grant the capability — the message names which |
| *connection refused* / a timeout | Nothing is listening, or the address is wrong | Check `BAO_ADDR`; `bao status` from the same shell is the quickest confirmation |

**Required capabilities**, which is what a 403 is about:

| Operation | Capability on |
| --- | --- |
| List policies | `list` on `sys/policies/acl` |
| Read a policy | `read` on `sys/policies/acl/*` |
| Create or update | `create` and `update` on `sys/policies/acl/*` |
| Delete | `delete` on `sys/policies/acl/*` |

**A failure never disturbs your work.** A refused connection, a rejected
token, a TLS failure, and a cancelled request all leave the document and
every unsaved edit exactly as they were. If a document changed across a
failed connection, that is a bug worth reporting.

**If certificate verification is disabled** — `--skip-verify` or
`BAO_SKIP_VERIFY` — a warning sits above the status line on every screen
for as long as the connection lasts. It is text rather than a colour, so
it is still there under `NO_COLOR`. It is not dismissible, deliberately:
this is the setting that gets turned on for an afternoon and left on for a
year.

**Debug logging:** there is none, and that is deliberate. There is no
verbose flag that prints requests, because a request carries the token in
a header and a debug mode is exactly where credentials escape. Diagnose
from the messages above; if something is genuinely unexplainable, the
reproduction belongs in `internal/baoclient`'s httptest suite, where the
wire traffic is visible without a real credential anywhere near it.

---

## Credential Handling

`BAO_TOKEN` and any other `BAO_*` credential-bearing environment variables must never appear in logs, diagnostic output, terminal screenshots, or bug reports. `internal/config.Config` holds the resolved token as a `SensitiveString`, a type whose formatting is redacted under every `fmt` verb (`%v`, `%+v`, `%#v`, `%s`, `%q`) and in error messages; only an explicit `.Reveal()` call returns the raw value, and the sole caller is `internal/baoclient.New`, which needs it to authenticate.

`internal/baoclient` keeps that guarantee on its own side: the token is sent only as the `X-Vault-Token` header, the client redacts itself under every formatting verb, the package writes nothing to the logger or to disk, and every error leaving it is scrubbed of the token — so even a server that echoes the credential back inside its own error message cannot leak it through BPE. All of that is asserted by tests rather than described; see Step 10. See `README.md`'s Security section for the vulnerability-reporting process.

---

## Maintenance Notes

- **Last game-day test:** 2026-09-17 — build, `--help`/`--version`, a usage error, the interactive editor opened on a policy with comments and an unknown attribute (opened, previewed, diagnosed, rule added, rule duplicated, rule removed with its comments, saved, and the file confirmed to still carry every comment and unknown attribute), the editor started empty and saved to a new path, the editor refusing to replace an existing file, `NO_COLOR=1` confirmed to emit no colour-setting escape sequences, the editor rendered on a pty at 60, 100 and 120 columns, `bpe validate` against a clean policy, a policy with only warnings, a policy with a semantic error, a policy with a syntax error, and a missing file, `bpe format` reformatting a misindented policy, `--check` reporting both "not formatted" and "already formatted" without writing, formatting refusing a genuine syntax error while still formatting a policy with a decode-time error (bad `expiration`) unchanged in meaning, `bpe test` against an allowed request, a denied (default-deny) request, a request whose winning rule carries parameter constraints (`INCOMPLETE`, exit `3`), an unknown-capability usage error, a broader-deny-does-not-override-a-more-specific-allow regression, and a missing file, and Ctrl+C/SIGTERM interruption, all manually exercised against the built binary (FSM-14 added `format`; FSM-13 added `test`; FSM-12 covered everything but those two).
- **Last live smoke test:** 2026-09-18 against a disposable in-memory OpenBao 2.5.2 on loopback — RUNBOOK Step 11. Covered an ordinary policy, one with an explicit `expiration`, one whose expiration came from a `ttl`, and one with `cas_required = true`; each updated through the real TUI with the body changing, the version advancing and the metadata intact. A stale write was provoked with a second client and presented as a conflict rather than a generic failure, the server's version was reviewed, and the deliberate retry succeeded. Every disposable policy was deleted afterwards and no credential reached any file.
- **Next scheduled review:** when BPE is first used against an OpenBao older or newer than 2.5.x, since the update path's compatibility is the thing that has been version-specific before.
- **Known drift risks:** the two wire-level assumptions that used to sit here are resolved, and resolved differently from each other. The failed check-and-set is `400` carrying `check-and-set parameter did not match the current version`, which BPE's matcher recognizes — that one held. The other did not: `sys/policies/acl` does **not** implement PATCH on 2.5.x, answering `405 unsupported operation`, so BPE now updates with a POST that echoes the policy's writable metadata back (see README.md's [Remote policies](README.md#remote-policies)). The remaining version risk is the mirror image: newer OpenBao releases document PATCH here, and if a future release were ever to *stop* accepting the POST form, the update path would need revisiting. POST is the older and more widely accepted of the two, which is why it is the one BPE uses. The editor's own limits are documented in README.md: a field whose value is not a plain literal is read-only rather than rewritten, the effective-access screen refuses to answer for a policy containing content BPE cannot fully represent, and a duplicated rule is appended at the end of the file rather than inserted after the original. `bpe validate`'s KV v2 and list/scan-prefix checks are same-file heuristics only — they have no access to a policy's real OpenBao mount configuration, so they can both miss real problems and flag paths that are actually fine; treat their output as guidance, not ground truth. `bpe test` has its own, separate scope limits — see README.md's [Evaluation limits](README.md#evaluation-limits) — and its expiration handling is a snapshot at the moment it runs, not live (README.md's [Expiration is a snapshot, not live](README.md#expiration-is-a-snapshot-not-live)). `bpe format`'s write-conflict detection narrows but does not close the check-then-rename race, and does not preserve ACLs or extended attributes — see README.md's [File writes](README.md#file-writes). Hard-link detection is Unix-only and silently skipped where unavailable.
