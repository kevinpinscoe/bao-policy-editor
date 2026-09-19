#!/usr/bin/env python3
"""BPE's interactive workflow checklist, driven against the real binary.

Every other test in this repository drives the Bubble Tea model directly, which
is the right level for almost everything. It cannot answer terminal-shaped
questions: whether anything is written past the right margin at 40 columns,
whether NO_COLOR is honoured in the bytes that reach the terminal, whether a
prompt that arrives pre-filled behaves when somebody types into it. This spawns
the compiled binary on a pseudo-terminal, sends real keystrokes, and reads back
what was rendered.

It is an opt-in release check, not part of the test suite, and deliberately not
wired into CI — see RUNBOOK.md Step 15.

    python3 scripts/tui-checklist.py

That is the whole invocation. **It builds ./cmd/bpe from the current checkout**
into a temporary directory and checks that binary, so the result always
describes the code you have rather than whatever ./bin/bpe happens to hold.
Everything it creates — the binary, the policy files it writes — is removed
afterwards, on a failure and on an interrupt as well as on a clean run.

Set BPE_BINARY to check some other artifact instead; the run says loudly that
it did, because then the result is about that file and not about this checkout.

Exits 0 when every check passes, 1 otherwise.

A word of warning from the first time this ran: four of its checks failed and
every one was a defect in this file rather than in BPE. A failure here is worth
reading carefully before it is believed — "the checklist failed" and "the thing
driving the checklist failed" look identical in a log.
"""

import contextlib
import os
import signal
import subprocess
import sys
import tempfile

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from tuidrive import KEYS, Tui, has_color, max_column  # noqa: E402

KEYS["ctrl+u"] = "\x15"

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
VALID = os.path.join(REPO, "testdata", "policies", "valid.hcl")
INVALID = os.path.join(REPO, "testdata", "policies", "invalid_syntax.hcl")

results = []
live = []  # every Tui spawned, so an interrupt does not leave one behind


def check(name, ok, detail=""):
    results.append((name, bool(ok), detail))
    print(f"{'PASS' if ok else 'FAIL'}  {name}" + (f"\n      {detail}" if detail else ""))


def spawn(*args, **kwargs):
    t = Tui(*args, **kwargs)
    live.append(t)
    return t


def reap_all():
    for t in live:
        with contextlib.suppress(Exception):
            t.kill()


def on_signal(signum, _frame):
    reap_all()
    sys.exit(128 + signum)


def build(into):
    """Compile cmd/bpe from this checkout. The binary under test is never a leftover."""
    binary = os.path.join(into, "bpe")
    print(f"building {os.path.relpath(REPO)}/cmd/bpe from the current checkout ...")
    proc = subprocess.run(
        ["go", "build", "-o", binary, "./cmd/bpe"],
        cwd=REPO,
        capture_output=True,
        text=True,
    )
    if proc.returncode != 0:
        sys.exit("go build failed:\n" + proc.stdout + proc.stderr)
    revision = subprocess.run(
        ["git", "rev-parse", "--short", "HEAD"], cwd=REPO, capture_output=True, text=True
    )
    # --untracked-files=normal, not =no. An untracked .go file inside a package
    # directory is compiled into the binary just like a tracked one, so hiding
    # untracked files would let this print "built from <revision>" for a build
    # that is not that revision — the exact claim this line exists to make.
    dirty = subprocess.run(
        ["git", "status", "--porcelain", "--untracked-files=normal"],
        cwd=REPO,
        capture_output=True,
        text=True,
    )
    head = revision.stdout.strip() or "unknown"
    if dirty.stdout.strip():
        changed = len(dirty.stdout.strip().splitlines())
        head += f" (working tree is dirty: {changed} modified or untracked file(s))"
    print(f"checking a binary built from {head}\n")
    return binary


def run(bpe, work):
    """Every check. `work` is a scratch directory that is deleted afterwards."""

    # 1 — starting with no file ------------------------------------------
    t = spawn([bpe], cols=100, rows=30)
    t.pump(1.2)
    screen = t.text()
    check(
        "1. Starting with no file",
        "(new policy)" in screen and "no rules yet" in screen and "? · q" in screen,
        "header says (new policy), the empty state invites `a`, and the footer lists the commands",
    )
    # "Exited 0" on its own is too weak: a binary that does nothing exits 0 too.
    # The editor has to have been *running* first, so that the clean exit is
    # attributable to the keystroke rather than to never having started.
    alive_before_quit = t.wait_exit(0.3) is None
    t.send("q")
    code = t.close()
    check(
        "1b. q exits cleanly from an unmodified empty policy",
        alive_before_quit and code == 0,
        f"the editor was running, then exited {code!r} on q; a process this driver had to kill fails",
    )

    # 2 — opening a valid policy -----------------------------------------
    t = spawn([bpe, VALID], cols=100, rows=30)
    t.wait_for("secret/data/team-a/*", timeout=8)
    screen = t.text()
    # The header truncates at the terminal width ("3 ru…" at 100 columns), so
    # the assertion is on the rule list rather than the header's wording.
    listed = [
        p
        for p in ("secret/data/team-a/*", "secret/metadata/team-a/*", "sys/policies/acl/team-a-*")
        if p in screen
    ]
    check(
        "2. Opening a valid policy",
        len(listed) == 3,
        f"all three of the file's rules are listed: {', '.join(listed) or 'none found'}",
    )
    t.send("q")
    t.close()

    # 3 — opening invalid HCL --------------------------------------------
    #
    # This does not exit. BPE opens the file read-only, says why, and refuses
    # to edit it — which is the documented behaviour and a much better one than
    # exiting. An earlier version of this check asserted an exit *and* accepted
    # the driver's own SIGKILL as evidence of it, so a hung process would have
    # passed. Now the screen is asserted, an editing attempt is proved to be
    # refused, and the exit code is required to be a clean 0 from `q`.
    t = spawn([bpe, INVALID], cols=100, rows=30)
    t.wait_for("syntax errors", timeout=8)
    screen = t.text()
    explained = "syntax errors" in screen and "read-only" in screen
    still_running = t.wait_exit(0.5) is None
    t.clear()
    t.send("a", settle=0.6)  # add a rule — must be refused on a read-only document
    refused = "Add a rule" not in t.text()
    t.send("esc", settle=0.3)
    t.send("q", settle=0.5)
    code = t.close()
    check(
        "3. Opening invalid HCL",
        explained and still_running and refused and code == 0,
        "opened read-only naming the syntax error, stayed up rather than dying, refused an "
        f"edit, and exited {code!r} on q",
    )

    # 4 + 5 — adding a rule, and toggling every capability -----------------
    t = spawn([bpe], cols=100, rows=40)
    t.pump(1.0)
    t.send("a", settle=0.5)
    t.send("enter", settle=0.4)
    t.type("secret/data/demo/*")
    t.send("enter", settle=0.4)
    t.send("tab", settle=0.3)
    for i in range(9):
        t.send("space", settle=0.25)
        if i < 8:
            t.send("tab", settle=0.2)
    t.clear()
    t.send("ctrl+s", settle=0.8)
    check(
        "4. Adding and editing a rule",
        "secret/data/demo/*" in t.text(),
        "the applied rule appears in the rule list",
    )

    # The nine toggles flipped `read` off and the other eight on. Assert that
    # through the generated HCL rather than through the rendered checkboxes: a
    # differential redraw only rewrites the cells that changed, so a row's
    # label is often absent from the frame even when its toggle worked.
    t.clear()
    t.send("p", settle=0.9)
    caps_preview = t.text()
    expected_on = ["create", "update", "patch", "delete", "list", "scan", "sudo", "deny"]
    present = [c for c in expected_on if f'"{c}"' in caps_preview]
    read_off = '"read"' not in caps_preview
    check(
        "5. Toggling each of the nine capabilities",
        len(present) == 8 and read_off,
        "all eight toggled on reached the HCL and the one toggled off left it: "
        + (", ".join(present) or "none")
        + ("; read absent" if read_off else "; read STILL PRESENT"),
    )

    # 6 — viewing generated HCL ------------------------------------------
    check(
        "6. Viewing generated HCL",
        'path "secret/data/demo/*"' in caps_preview and "capabilities" in caps_preview,
        "the preview shows real HCL for the rule just added",
    )
    t.send("esc", settle=0.4)
    t.send("q", settle=0.4)
    t.send("y", settle=0.3)
    t.close()

    # 7 — simulating allowed and denied requests --------------------------
    # A fresh editor with one read-only rule. The rule built above has `deny`
    # toggled on, which correctly denies everything it matches — a fine thing
    # for BPE to do and a useless basis for an "allowed" check.
    t = spawn([bpe], cols=100, rows=40)
    t.pump(1.0)
    t.send("a", settle=0.5)
    t.send("enter", settle=0.4)
    t.type("secret/data/demo/*")
    t.send("enter", settle=0.4)
    t.send("ctrl+s", settle=0.8)  # applies with the default `read` capability
    t.clear()
    t.send("t", settle=0.8)
    t.type("secret/data/demo/thing")
    t.send("enter", settle=0.9)
    allowed = t.text()
    check(
        "7a. Simulating a request the policy allows",
        "ALLOWED" in allowed and "DENIED" not in allowed,
        "the effective-access screen answered ALLOWED, and only ALLOWED",
    )
    t.clear()
    t.send("esc", settle=0.4)
    t.send("t", settle=0.7)
    t.send("ctrl+u", settle=0.3)  # the path field keeps what the last check used
    t.type("kv/other/thing")
    t.send("enter", settle=0.9)
    denied = t.text()
    check(
        "7b. Simulating a request the policy denies",
        "DENIED" in denied and "ALLOWED" not in denied,
        "default-deny answered DENIED, and only DENIED, for an uncovered path",
    )
    t.send("esc", settle=0.4)

    # 8 — quitting with unsaved changes -----------------------------------
    t.clear()
    t.send("q", settle=0.8)
    quitting = t.text()
    quit_guarded = "unsaved" in quitting.lower() or "discard" in quitting.lower()
    still_up = t.wait_exit(0.5) is None
    check(
        "8. Attempting to quit with unsaved changes",
        quit_guarded and still_up,
        "a confirmation stands between the keystroke and losing the work, and the editor is still running",
    )
    t.send("esc", settle=0.3)
    t.send("q", settle=0.3)
    t.send("y", settle=0.3)
    t.close()

    # 9 — saving and reopening a policy -----------------------------------
    target = os.path.join(work, "saved.hcl")
    t = spawn([bpe], cols=100, rows=40)
    t.pump(1.0)
    t.send("a", settle=0.5)
    t.send("enter", settle=0.4)
    t.type("secret/data/saved/*")
    t.send("enter", settle=0.4)
    t.send("ctrl+s", settle=0.7)
    t.send("s", settle=0.7)
    t.send("ctrl+s", settle=0.7)
    t.clear()
    t.send("ctrl+u", settle=0.4)  # the prompt arrives pre-filled with a suggested path
    t.type(target)
    t.send("enter", settle=1.2)
    t.send("q", settle=0.4)
    save_exit = t.close()
    written = open(target).read() if os.path.exists(target) else ""
    check(
        "9a. Saving a policy that had no file",
        "secret/data/saved/*" in written and save_exit == 0,
        f"the rule reached {os.path.basename(target)} and the editor exited {save_exit!r}",
    )
    t = spawn([bpe, target], cols=100, rows=30)
    t.wait_for("secret/data/saved/*", timeout=8)
    reopened = t.text()
    t.send("q")
    reopen_exit = t.close()
    check(
        "9b. Reopening the saved policy",
        "secret/data/saved/*" in reopened and reopen_exit == 0,
        f"the file reopens showing what was saved, and exits {reopen_exit!r}",
    )

    # 10 — detecting an externally modified file ---------------------------
    conflict = os.path.join(work, "conflict.hcl")
    with open(conflict, "w") as fh:
        fh.write('path "secret/data/one/*" {\n  capabilities = ["read"]\n}\n')
    t = spawn([bpe, conflict], cols=100, rows=40)
    t.wait_for("secret/data/one/*", timeout=8)
    t.clear()
    t.send("d", settle=0.6)  # duplicate a rule, so the document is modified
    duplicated = "2 rules" in t.text() or "duplicated" in t.text().lower()
    with open(conflict, "a") as fh:
        fh.write("\n# changed underneath the editor\n")
    t.clear()
    t.send("s", settle=0.7)
    t.send("ctrl+s", settle=1.0)
    conflicted = t.text().lower()
    check(
        "10. Detecting an externally modified file",
        "changed" in conflicted or "conflict" in conflicted,
        "the write was refused and the change on disk reported",
    )
    on_disk = open(conflict).read()
    # The in-memory duplicate has to have actually happened, or "the file still
    # has one rule" is true for the boring reason that nothing ever ran.
    check(
        "10b. The refused write did not touch the file",
        duplicated
        and "changed underneath the editor" in on_disk
        and "secret/data/one/*" in on_disk
        and on_disk.count("path ") == 1,
        "the editor really did duplicate the rule in memory, and the file on disk still has the "
        "external edit, the original rule, and no second path block",
    )
    t.send("esc", settle=0.3)
    t.kill()

    # 11 — running in a narrow terminal ------------------------------------
    for cols in (40, 60, 80):
        t = spawn([bpe, VALID], cols=cols, rows=24)
        t.wait_for("rule", timeout=8)
        t.pump(0.6)
        widest = max_column(t.raw)
        legible = "rule" in t.text().lower()
        t.send("q")
        exit_code = t.close()
        check(
            f"11. Running in a {cols}-column terminal",
            0 < widest <= cols and legible and exit_code == 0,
            f"rightmost column written was {widest}, terminal is {cols}; content rendered; exit {exit_code!r}",
        )

    # 12 — running with colour disabled ------------------------------------
    t = spawn([bpe, VALID], cols=100, rows=30)
    t.wait_for("secret/data/team-a/*", timeout=8)
    colour_on = has_color(t.raw)
    t.send("q")
    t.close()

    t = spawn([bpe, VALID], cols=100, rows=30, env={"NO_COLOR": "1"})
    t.wait_for("secret/data/team-a/*", timeout=8)
    colour_off = has_color(t.raw)
    readable = "secret/data/team-a/*" in t.text()
    t.send("q")
    no_colour_exit = t.close()
    check(
        "12a. Colour is used when it is available",
        colour_on,
        "the control case emits colour-setting sequences, so 12b is meaningful",
    )
    check(
        "12b. NO_COLOR=1 emits no colour-setting sequences, and stays readable",
        (not colour_off) and readable and no_colour_exit == 0,
        f"no SGR colour parameters in the stream; the rule list still renders; exit {no_colour_exit!r}",
    )


def main():
    signal.signal(signal.SIGINT, on_signal)
    signal.signal(signal.SIGTERM, on_signal)

    override = os.environ.get("BPE_BINARY")
    try:
        with tempfile.TemporaryDirectory(prefix="bpe-checklist-") as work:
            if override:
                print("=" * 62)
                print(f"BPE_BINARY is set: checking {override}")
                print("This run does NOT describe the current checkout.")
                print("=" * 62 + "\n")
                if not os.path.exists(override):
                    sys.exit(f"BPE_BINARY points at {override}, which does not exist")
                bpe = override
            else:
                bpe = build(work)
            run(bpe, work)
    finally:
        reap_all()

    print("\n" + "=" * 62)
    failed = [r for r in results if not r[1]]
    print(f"{len(results) - len(failed)}/{len(results)} checks passed")
    for name, _, _ in failed:
        print(f"  FAILED: {name}")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
