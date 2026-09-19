"""Drive the real bpe binary through a pty, the way a person would.

This is not the model-level test harness in internal/tui. It spawns the
compiled binary on a pseudo-terminal of a given size, sends real keystrokes,
and reads back what was actually rendered — which is the only way to check
terminal-shaped behaviour (narrow widths, NO_COLOR, the footer) against the
thing a user runs.
"""

import errno
import fcntl
import os
import pty
import re
import select
import signal
import struct
import termios
import time

ANSI = re.compile(r"\x1b\[[0-9;?]*[a-zA-Z]|\x1b[()][B0]|\x1b[=>]|\x1b\][^\x07]*\x07")

KEYS = {
    "enter": "\r",
    "esc": "\x1b",
    "tab": "\t",
    "up": "\x1b[A",
    "down": "\x1b[B",
    "right": "\x1b[C",
    "left": "\x1b[D",
    "ctrl+s": "\x13",
    "ctrl+c": "\x03",
    "space": " ",
    "backspace": "\x7f",
}


class Tui:
    def __init__(self, argv, cols=100, rows=30, env=None, cwd=None):
        self.argv = argv
        self.buf = ""
        self.raw = b""
        self._exit = None
        self._closed = False
        env = {**os.environ, **(env or {})}
        env.setdefault("TERM", "xterm-256color")
        self.pid, self.fd = pty.fork()
        if self.pid == 0:
            if cwd:
                os.chdir(cwd)
            os.execvpe(argv[0], argv, env)
            os._exit(127)
        fcntl.ioctl(self.fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))

    # --- reading -------------------------------------------------------

    def pump(self, seconds=0.35):
        """Read whatever has arrived for a while."""
        end = time.time() + seconds
        while time.time() < end:
            r, _, _ = select.select([self.fd], [], [], 0.05)
            if not r:
                continue
            try:
                chunk = os.read(self.fd, 65536)
            except OSError as exc:
                if exc.errno in (errno.EIO, errno.EBADF):
                    break
                raise
            if not chunk:
                break
            self.raw += chunk
            self.buf += chunk.decode("utf-8", "replace")
        return self.text()

    def wait_for(self, needle, timeout=6.0):
        end = time.time() + timeout
        while time.time() < end:
            if needle in self.text():
                return True
            self.pump(0.15)
        return False

    def text(self):
        return ANSI.sub("", self.buf)

    def tail(self, n=2500):
        return self.text()[-n:]

    # --- writing -------------------------------------------------------

    def send(self, *keys, settle=0.35):
        for key in keys:
            data = KEYS.get(key, key)
            os.write(self.fd, data.encode())
            time.sleep(0.12)
        return self.pump(settle)

    def type(self, text, settle=0.35):
        for ch in text:
            os.write(self.fd, ch.encode())
            time.sleep(0.02)
        return self.pump(settle)

    # --- finishing -----------------------------------------------------

    def wait_exit(self, timeout=3.0):
        """The child's exit code if it ends within `timeout`, else None.

        None means "still running" and nothing has been killed, which is what
        lets a caller tell a process that exited on its own from one this
        driver had to put down. Those are very different results, and an
        assertion that cannot tell them apart will call a hung program a
        passing error path.
        """
        if self._exit is not None:
            return self._exit
        end = time.time() + timeout
        while time.time() < end:
            self.pump(0.1)
            pid, status = os.waitpid(self.pid, os.WNOHANG)
            if pid:
                self._exit = os.waitstatus_to_exitcode(status)
                return self._exit
        return None

    def close(self, timeout=3.0):
        """Reap the child, killing it if it overstays. Idempotent.

        Returns its exit code, or the string "killed" if it had to be killed —
        never a plausible-looking integer for a process that never finished.
        """
        if self._closed:
            return self._exit if self._exit is not None else "killed"
        code = self.wait_exit(timeout)
        if code is None:
            self.kill()
            code = "killed"
        self._closed = True
        try:
            os.close(self.fd)
        except OSError:
            pass
        return code

    def kill(self):
        """Put the child down without waiting. Safe to call on a dead process."""
        try:
            os.kill(self.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        try:
            os.waitpid(self.pid, 0)
        except (ChildProcessError, OSError):
            pass
        self._closed = True
        try:
            os.close(self.fd)
        except OSError:
            pass

    def widest_line(self):
        """The longest rendered line, ANSI stripped — for narrow-terminal checks."""
        return max((len(line.rstrip()) for line in self.text().splitlines()), default=0)


def has_color(raw: bytes) -> bool:
    """True if the stream contains an SGR sequence that actually sets a colour."""
    for match in re.finditer(rb"\x1b\[([0-9;]*)m", raw):
        params = [p for p in match.group(1).split(b";") if p != b""]
        for p in params:
            n = int(p)
            if 30 <= n <= 37 or 40 <= n <= 47 or 90 <= n <= 97 or 100 <= n <= 107 or n in (38, 48):
                return True
    return False


def _clear(self):
    """Forget everything rendered so far, so the next assertion is about the next screen."""
    self.buf = ""
    self.raw = b""


Tui.clear = _clear


CSI = re.compile(rb"\x1b\[([0-9;?]*)([a-zA-Z])")


def max_column(raw: bytes) -> int:
    """Highest column any emitted text reached, tracking real cursor motion.

    Measuring "lines" in a pty stream does not work: a TUI redraws by moving
    the cursor, so consecutive frames run together and a naive split reports
    lines that were never on screen at once. This walks the stream instead,
    following CUP/CHA/CUF/CUB and carriage returns, so the number it returns
    is the rightmost column actually written to. A value greater than the
    terminal width means something wrapped.
    """
    col = 0
    widest = 0
    i = 0
    n = len(raw)
    while i < n:
        byte = raw[i:i + 1]
        if byte == b"\x1b":
            m = CSI.match(raw, i)
            if m:
                params = [int(p) for p in m.group(1).split(b";") if p.isdigit()]
                final = m.group(2)
                if final in (b"H", b"f"):
                    col = (params[1] - 1) if len(params) > 1 else 0
                elif final == b"G":
                    col = (params[0] - 1) if params else 0
                elif final == b"C":
                    col += params[0] if params else 1
                elif final == b"D":
                    col = max(0, col - (params[0] if params else 1))
                i = m.end()
                continue
            i += 2  # some other escape; skip the introducer and its selector
            continue
        if byte == b"\r":
            col = 0
        elif byte == b"\n":
            col = 0
        elif byte == b"\x08":
            col = max(0, col - 1)
        elif byte >= b" ":
            # Step over a UTF-8 continuation so a multibyte glyph counts once.
            ch = raw[i:i + 1]
            length = 1
            if ch >= b"\xf0":
                length = 4
            elif ch >= b"\xe0":
                length = 3
            elif ch >= b"\xc0":
                length = 2
            col += 1
            widest = max(widest, col)
            i += length
            continue
        i += 1
    return widest
