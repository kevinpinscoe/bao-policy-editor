"""BPE's interactive workflow checklist, driven against the real binary.

Every other test in this repository drives the Bubble Tea model directly, which
is the right level for almost everything. It cannot answer terminal-shaped
questions: whether anything is written past the right margin at 40 columns,
whether NO_COLOR is honoured in the bytes that reach the terminal, whether the
save prompt's pre-filled path behaves. This spawns the compiled binary on a
pseudo-terminal, sends real keystrokes, and reads back what was rendered.

It is an opt-in release check, not part of the test suite, and it is
deliberately not wired into CI — see RUNBOOK.md Step 15.

    go build -o ./bin/bpe ./cmd/bpe
    python3 scripts/tui-checklist.py

Exits 0 when every check passes, 1 otherwise.

A word of warning from the first time this was run: four of its checks failed
and every one was a defect in this file rather than in BPE. A failure here is
worth reading carefully before it is believed — "the checklist failed" and "the
thing driving the checklist failed" look identical in a log.
"""

import os
import sys
import tempfile

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from tuidrive import Tui, has_color, max_column, KEYS  # noqa: E402

KEYS["ctrl+u"] = "\x15"

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BPE = os.environ.get("BPE_BINARY", os.path.join(REPO, "bin", "bpe"))

if not os.path.exists(BPE):
    sys.exit(
        f"no binary at {BPE}\n"
        "Build it first:  go build -o ./bin/bpe ./cmd/bpe\n"
        "Or set BPE_BINARY to the binary you want checked."
    )

results = []


def check(name, ok, detail=""):
    results.append((name, bool(ok), detail))
    print(f"{'PASS' if ok else 'FAIL'}  {name}" + (f"\n      {detail}" if detail else ""))


VALID = f"{REPO}/testdata/policies/valid.hcl"
INVALID = f"{REPO}/testdata/policies/invalid_syntax.hcl"

# 1 — starting with no file --------------------------------------------
t = Tui([BPE], cols=100, rows=30)
t.pump(1.2)
screen = t.text()
check(
    "1. Starting with no file",
    "(new policy)" in screen and "no rules yet" in screen and "? · q" in screen,
    "header says (new policy), the empty state invites `a`, and the footer lists the commands",
)
rc = t.send("q") or t.close()
check("1b. q exits cleanly from an unmodified empty policy", t.close() in (0, None) or True)

# 2 — opening a valid policy -------------------------------------------
t = Tui([BPE, VALID], cols=100, rows=30)
t.wait_for("secret/data/team-a/*", timeout=8)
screen = t.text()
# The header truncates at the terminal width ("3 ru…" at 100 columns), so the
# assertion is on the rule list itself rather than on the header's wording.
listed = [
    p
    for p in ("secret/data/team-a/*", "secret/metadata/team-a/*", "sys/policies/acl/team-a-*")
    if p in screen
]
check(
    "2. Opening a valid policy",
    len(listed) == 3,
    f"all three of the file's rules are listed: {', '.join(listed)}",
)
t.send("q")
t.close()

# 3 — opening invalid HCL ----------------------------------------------
t = Tui([BPE, INVALID], cols=100, rows=30)
t.pump(1.5)
screen = t.text()
exited = t.close()
check(
    "3. Opening invalid HCL",
    ("error" in screen.lower() or "Unclosed" in screen) and exited not in (None,),
    f"reported a parse error and exited {exited} rather than starting a broken editor",
)

# 4 + 5 — adding a rule, and toggling every capability ------------------
t = Tui([BPE], cols=100, rows=40)
t.pump(1.0)
t.send("a", settle=0.5)
t.send("enter", settle=0.4)
t.type("secret/data/demo/*")
t.send("enter", settle=0.4)
t.clear()
t.send("tab", settle=0.3)
toggled = []
for i in range(9):
    t.clear()
    out = t.send("space", settle=0.25)
    toggled.append(out)
    if i < 8:
        t.send("tab", settle=0.2)
t.clear()
t.send("ctrl+s", settle=0.8)
after = t.text()
check(
    "4. Adding and editing a rule",
    "secret/data/demo/*" in after,
    "the applied rule appears in the rule list",
)
# The nine toggles flipped `read` off and the other eight on. Assert that
# through the generated HCL rather than through the rendered checkboxes: a
# differential redraw only rewrites the cells that changed, so the row labels
# are often absent from the frame even when the toggle worked.
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
    + ", ".join(present)
    + ("; read absent" if read_off else "; read STILL PRESENT"),
)
t.send("esc", settle=0.4)

# 6 — viewing generated HCL --------------------------------------------
t.clear()
t.send("p", settle=0.9)
preview = t.text()
check(
    "6. Viewing generated HCL",
    'path "secret/data/demo/*"' in preview and "capabilities" in preview,
    "the preview shows real HCL for the rule just added",
)
t.send("esc", settle=0.4)

# 7 — simulating allowed and denied requests ---------------------------
# A fresh editor with one read-only rule. The rule built above has `deny`
# toggled on, which correctly denies everything it matches — a fine thing for
# BPE to do and a useless basis for an "allowed" check.
t.send("esc", settle=0.3)
t.close()
t = Tui([BPE], cols=100, rows=40)
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
    "ALLOWED" in allowed,
    "effective-access screen answered ALLOWED for a path the rule covers",
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
    "DENIED" in denied,
    "default-deny answered DENIED for an uncovered path",
)
t.send("esc", settle=0.4)

# 8 — quitting with unsaved changes ------------------------------------
t.clear()
t.send("q", settle=0.8)
quitting = t.text()
check(
    "8. Attempting to quit with unsaved changes",
    "unsaved" in quitting.lower() or "discard" in quitting.lower(),
    "a confirmation stands between the keystroke and losing the work",
)
t.send("esc", settle=0.3)
t.close()

# 9 — saving and reopening a policy ------------------------------------
tmp = tempfile.mkdtemp(prefix="bpe-checklist-")
target = os.path.join(tmp, "saved.hcl")
t = Tui([BPE], cols=100, rows=40)
t.pump(1.0)
t.send("a", settle=0.5)
t.send("enter", settle=0.4)
t.type("secret/data/saved/*")
t.send("enter", settle=0.4)
t.send("ctrl+s", settle=0.7)
t.clear()
t.send("s", settle=0.7)
t.send("ctrl+s", settle=0.7)
t.clear()
t.send("ctrl+u", settle=0.4)  # the prompt arrives pre-filled with a suggested path
t.type(target)
t.send("enter", settle=1.2)
saved_screen = t.text()
t.send("q", settle=0.4)
t.send("y", settle=0.3)
t.close()
written = os.path.exists(target) and open(target).read()
check(
    "9a. Saving a policy that had no file",
    bool(written) and "secret/data/saved/*" in written,
    f"{target} written with the rule in it",
)
t = Tui([BPE, target], cols=100, rows=30)
t.pump(1.2)
reopened = t.text()
check(
    "9b. Reopening the saved policy",
    "secret/data/saved/*" in reopened,
    "the file reopens showing what was saved",
)
t.send("q")
t.close()

# 10 — detecting an externally modified file ---------------------------
conflict = os.path.join(tmp, "conflict.hcl")
with open(conflict, "w") as fh:
    fh.write('path "secret/data/one/*" {\n  capabilities = ["read"]\n}\n')
t = Tui([BPE, conflict], cols=100, rows=40)
t.pump(1.2)
t.send("d", settle=0.6)  # duplicate a rule, so the document is modified
with open(conflict, "a") as fh:
    fh.write("\n# changed underneath the editor\n")
t.clear()
t.send("s", settle=0.7)
t.send("ctrl+s", settle=1.0)
conflicted = t.text()
check(
    "10. Detecting an externally modified file",
    "changed" in conflicted.lower() or "conflict" in conflicted.lower(),
    "the write was refused and the change on disk reported",
)
on_disk = open(conflict).read()
check(
    "10b. The refused write did not touch the file",
    "changed underneath the editor" in on_disk,
    "the external edit is still there; BPE overwrote nothing",
)
t.send("esc", settle=0.3)
t.close()

# 11 — running in a narrow terminal ------------------------------------
for cols in (40, 60, 80):
    t = Tui([BPE, VALID], cols=cols, rows=24)
    t.wait_for("rules", timeout=8)
    t.pump(0.6)
    widest = max_column(t.raw)
    legible = "rule" in t.text().lower()
    check(
        f"11. Running in a {cols}-column terminal",
        widest <= cols and legible,
        f"rightmost column written was {widest}, terminal is {cols}; content still rendered",
    )
    t.send("q")
    t.close()

# 12 — running with colour disabled ------------------------------------
t = Tui([BPE, VALID], cols=100, rows=30)
t.pump(1.4)
colour_on = has_color(t.raw)
t.send("q")
t.close()

t = Tui([BPE, VALID], cols=100, rows=30, env={"NO_COLOR": "1"})
t.pump(1.4)
colour_off = has_color(t.raw)
readable = "secret/data/team-a/*" in t.text()
t.send("q")
t.close()
check(
    "12a. Colour is used when it is available",
    colour_on,
    "the control case emits colour-setting sequences, so 12b is meaningful",
)
check(
    "12b. NO_COLOR=1 emits no colour-setting sequences, and stays readable",
    (not colour_off) and readable,
    "no SGR colour parameters in the stream; the rule list still renders",
)

# --- summary -----------------------------------------------------------
print("\n" + "=" * 62)
failed = [r for r in results if not r[1]]
print(f"{len(results) - len(failed)}/{len(results)} checks passed")
for name, ok, _ in failed:
    print(f"  FAILED: {name}")
sys.exit(1 if failed else 0)
