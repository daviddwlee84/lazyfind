#!/usr/bin/env python3
"""Exercise lazyfind through a real PTY using disposable files and XDG state.

Usage: python3 scripts/pty_smoke.py /path/to/lazyfind [--artifacts /tmp/pty-log]
Requires Python 3, real fd/fdfind and rg. No Python packages are required.
The small terminal tracker is an ASCII assertion aid, not a visual glyph oracle.
"""

import argparse
import base64
import codecs
import errno
import fcntl
import json
import os
from pathlib import Path
import re
import select
import shutil
import signal
import struct
import subprocess
import sys
import tempfile
import termios
import time
import unicodedata


class Screen:
    """Track cursor-addressed text sufficiently for deterministic TUI assertions."""

    def __init__(self, columns=120, rows=32):
        self.pending = ""
        self.resize(columns, rows)

    def resize(self, columns, rows):
        self.columns, self.rows = columns, rows
        self.cells = [[" "] * columns for _ in range(rows)]
        self.x = self.y = 0
        self.saved = (0, 0)
        self.top, self.bottom = 0, rows - 1

    def index(self, reverse=False):
        if reverse:
            if self.y == self.top:
                self.cells.pop(self.bottom)
                self.cells.insert(self.top, [" "] * self.columns)
            else:
                self.y = max(0, self.y - 1)
        elif self.y == self.bottom:
            self.cells.pop(self.top)
            self.cells.insert(self.bottom, [" "] * self.columns)
        else:
            self.y = min(self.rows - 1, self.y + 1)

    def feed(self, value):
        self.pending += value
        pos = 0
        while pos < len(self.pending):
            ch = self.pending[pos]
            if ch == "\x1b":
                if pos + 1 >= len(self.pending):
                    break
                following = self.pending[pos + 1]
                if following == "[":
                    match = re.match(r"\x1b\[([0-?]*)([ -/]*)([@-~])", self.pending[pos:])
                    if not match:
                        break
                    self.csi(match[1], match[3])
                    pos += match.end()
                    continue
                if following in "]P_^":
                    match = re.search(r"\x07|\x1b\\", self.pending[pos + 2:])
                    if not match:
                        break
                    pos += 2 + match.end()
                    continue
                if following == "7":
                    self.saved = (self.x, self.y)
                elif following == "8":
                    self.x, self.y = self.saved
                elif following == "D":
                    self.index()
                elif following == "M":
                    self.index(reverse=True)
                pos += 2
                continue
            if ch == "\r":
                self.x = 0
            elif ch == "\n":
                self.index()
            elif ch == "\b":
                self.x = max(0, self.x - 1)
            elif ch == "\t":
                self.x = min(self.columns - 1, (self.x // 8 + 1) * 8)
            elif ord(ch) >= 32:
                width = 0 if unicodedata.combining(ch) else (2 if unicodedata.east_asian_width(ch) in "WF" else 1)
                if width and self.x + width > self.columns:
                    self.x = 0
                    self.y = min(self.rows - 1, self.y + 1)
                if width:
                    self.cells[self.y][self.x] = ch
                    if width == 2 and self.x + 1 < self.columns:
                        self.cells[self.y][self.x + 1] = ""
                    self.x += width
                elif self.x:
                    self.cells[self.y][self.x - 1] += ch
            pos += 1
        self.pending = self.pending[pos:]

    def csi(self, raw, command):
        if raw.startswith(("?", ">", "=")):
            if raw == "?1049" and command == "h":
                self.resize(self.columns, self.rows)
            return
        nums = [int(n) if n.isdigit() else 0 for n in raw.split(";")]
        n = nums[0] if nums and nums[0] else 1
        if command in "Hf":
            self.y = max(0, min(self.rows - 1, n - 1))
            self.x = max(0, min(self.columns - 1, (nums[1] if len(nums) > 1 and nums[1] else 1) - 1))
        elif command == "A":
            self.y = max(0, self.y - n)
        elif command == "B":
            self.y = min(self.rows - 1, self.y + n)
        elif command == "C":
            self.x = min(self.columns - 1, self.x + n)
        elif command == "D":
            self.x = max(0, self.x - n)
        elif command == "G":
            self.x = min(self.columns - 1, n - 1)
        elif command == "d":
            self.y = min(self.rows - 1, n - 1)
        elif command == "r":
            self.top = max(0, n - 1)
            self.bottom = min(self.rows - 1, (nums[1] if len(nums) > 1 and nums[1] else self.rows) - 1)
            self.x = self.y = 0
        elif command == "L":
            for _ in range(n):
                self.cells.pop(self.bottom)
                self.cells.insert(self.y, [" "] * self.columns)
        elif command == "M":
            for _ in range(n):
                self.cells.pop(self.y)
                self.cells.insert(self.bottom, [" "] * self.columns)
        elif command == "J":
            mode = nums[0]
            if mode in (2, 3):
                self.cells = [[" "] * self.columns for _ in range(self.rows)]
            elif mode == 0:
                self.cells[self.y][self.x:] = [" "] * (self.columns - self.x)
                for row in range(self.y + 1, self.rows):
                    self.cells[row] = [" "] * self.columns
        elif command == "K":
            mode = nums[0]
            left, right = (0, self.columns) if mode == 2 else ((0, self.x + 1) if mode == 1 else (self.x, self.columns))
            self.cells[self.y][left:right] = [" "] * (right - left)
        elif command == "X":
            end = min(self.columns, self.x + n)
            self.cells[self.y][self.x:end] = [" "] * (end - self.x)
        elif command == "P":
            row = self.cells[self.y]
            self.cells[self.y] = (row[:self.x] + row[self.x + n:] + [" "] * n)[:self.columns]
        elif command == "@":
            row = self.cells[self.y]
            self.cells[self.y] = (row[:self.x] + [" "] * n + row[self.x:])[:self.columns]
        elif command == "s":
            self.saved = (self.x, self.y)
        elif command == "u":
            self.x, self.y = self.saved

    @property
    def lines(self):
        return ["".join(row).rstrip() for row in self.cells]

    @property
    def text(self):
        return "\n".join(self.lines)


class Session:
    def __init__(self, argv, env, cwd):
        self.screen = Screen()
        self.raw = bytearray()
        self.decoder = codecs.getincrementaldecoder("utf-8")("replace")
        self.master, self.slave = os.openpty()
        self.report_read, report_write = os.pipe()
        self.initial = termios.tcgetattr(self.slave)
        self.resize(120, 32, initial=True)
        self.pid = os.fork()
        if self.pid == 0:
            try:
                os.close(self.master)
                os.close(self.report_read)
                os.setsid()
                fcntl.ioctl(self.slave, termios.TIOCSCTTY, 0)
                for fd in (0, 1, 2):
                    os.dup2(self.slave, fd)
                if self.slave > 2:
                    os.close(self.slave)
                os.chdir(cwd)
                # Keep the session leader alive until after mode inspection.
                # macOS revokes the PTY when its session leader exits, so a
                # parent tcgetattr() after exec/exit cannot verify restoration.
                result = subprocess.run(argv, env=env)
                os.write(report_write, json.dumps({"flags": termios.tcgetattr(0)[3]}).encode())
                os.close(report_write)
                os._exit(result.returncode if result.returncode >= 0 else 128 - result.returncode)
            finally:
                os._exit(127)
        os.close(report_write)
        self.exit_status = None
        self.final_modes = None

    def resize(self, columns, rows, initial=False):
        fcntl.ioctl(self.slave, termios.TIOCSWINSZ, struct.pack("HHHH", rows, columns, 0, 0))
        self.screen.resize(columns, rows)
        if not initial:
            os.kill(self.pid, signal.SIGWINCH)

    def poll(self):
        if self.exit_status is None:
            pid, status = os.waitpid(self.pid, os.WNOHANG)
            if pid:
                self.exit_status = os.waitstatus_to_exitcode(status)
                report = os.read(self.report_read, 4096)
                if report:
                    self.final_modes = json.loads(report)
        return self.exit_status

    def pump(self, duration=0.1):
        until = time.monotonic() + duration
        while time.monotonic() < until:
            ready, _, _ = select.select([self.master], [], [], max(0, until - time.monotonic()))
            if not ready:
                return
            try:
                data = os.read(self.master, 65536)
            except OSError as error:
                if error.errno == errno.EIO:
                    return
                raise
            if not data:
                return
            self.raw.extend(data)
            self.screen.feed(self.decoder.decode(data))

    def send(self, value):
        os.write(self.master, value.encode() if isinstance(value, str) else value)
        self.pump(0.15)

    def expect(self, description, predicate, timeout=10):
        until = time.monotonic() + timeout
        while time.monotonic() < until:
            if predicate(self.screen):
                return
            if self.poll() is not None:
                raise AssertionError(f"Exited {self.exit_status} waiting for {description}\n{self.screen.text}")
            self.pump(0.05)
        raise AssertionError(f"Timed out waiting for {description}\n{self.screen.text}")

    def text(self, needle, timeout=10):
        self.expect(needle, lambda screen: needle in screen.text, timeout)

    def click(self, x, y):
        # Wire coordinates are one-based. Include separate press and release.
        self.send(f"\x1b[<0;{x};{y}M\x1b[<0;{x};{y}m")

    def click_text(self, text):
        for y, line in enumerate(self.screen.lines):
            if text in line:
                self.click(line.index(text) + max(1, len(text) // 2), y + 1)
                return
        raise AssertionError(f"No clickable text {text!r}\n{self.screen.text}")

    def close(self):
        if self.poll() is None:
            os.killpg(self.pid, signal.SIGTERM)
            until = time.monotonic() + 2
            while self.poll() is None and time.monotonic() < until:
                self.pump(0.05)
            if self.poll() is None:
                os.killpg(self.pid, signal.SIGKILL)
                os.waitpid(self.pid, 0)
        os.close(self.master)
        os.close(self.slave)
        os.close(self.report_read)


def exercise(binary, artifacts):
    fd, rg = shutil.which("fd") or shutil.which("fdfind"), shutil.which("rg")
    if not fd or not rg:
        raise RuntimeError("PTY verification requires installed fd/fdfind and rg")
    events = []
    with tempfile.TemporaryDirectory(prefix="lazyfind-pty-") as temp:
        base = Path(temp)
        fixture = base / "fixture"
        fixture.mkdir()
        for name, text in {
            "needle-alpha.txt": "needle alpha\n" * 10,
            "needle-beta.txt": "needle\n",
            "content-only.txt": "needle content only\n" * 4,
            "搜尋-needle.txt": "needle unicode\n" * 3,
        }.items():
            (fixture / name).write_text(text)
        env = os.environ.copy()
        env.update({"TERM": "xterm-256color", "NO_COLOR": "1", "HOME": str(base / "home"),
                    "XDG_CONFIG_HOME": str(base / "config"), "XDG_STATE_HOME": str(base / "state"),
                    "XDG_CACHE_HOME": str(base / "cache"), "XDG_DATA_HOME": str(base / "data"),
                    "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull,
                    "BAT_CONFIG_PATH": os.devnull})
        for key in ("HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"):
            Path(env[key]).mkdir()
        marker = base / "child.json"
        child = base / "editor.py"
        child.write_text(
            "import json,sys,termios\n"
            "flags=termios.tcgetattr(0)[3]\n"
            f"open({str(marker)!r},'w').write(json.dumps({{'path':sys.argv[1], 'echo':bool(flags&termios.ECHO), 'canonical':bool(flags&termios.ICANON)}}))\n"
            "print('LAZYFIND_TEST_CHILD_READY',flush=True)\n"
            "input()\n"
            "sys.exit(7 if '--fail' in sys.argv else 0)\n"
        )
        config_dir = Path(env["XDG_CONFIG_HOME"]) / "lazyfind"
        config_dir.mkdir()
        quote = json.dumps
        (config_dir / "config.toml").write_text(
            "[tools]\n" + f"fd = {quote(fd)}\nrg = {quote(rg)}\n"
            "[ui]\ncolor = 'never'\nmouse = true\n"
            "[[actions]]\nid='editor'\nlabel='Fixture editor'\nmode='suspend'\ncwd='{dir}'\n"
            f"argv={quote([sys.executable, str(child), '{path}'])}\n"
            "[[actions]]\nid='fixture-failure'\nlabel='Fixture failure'\nmode='suspend'\ncwd='{dir}'\n"
            f"argv={quote([sys.executable, str(child), '{path}', '--fail'])}\n"
            "[[rules]]\nkinds=['file']\nactions=['fixture-failure']\ndefault='editor'\n"
        )
        session = Session([binary, str(fixture)], env, str(fixture))
        try:
            session.text("lazyfind")
            session.send("needle")
            session.text("complete · 4 results")
            session.text("needle-alpha.txt")
            session.send("\r")
            session.text("Enter Fixture editor")
            events.append("real fd + rg search and Enter accepts query")

            session.send("\x1b[H")
            session.expect("Home selects first result", lambda screen: any("> content-only.txt" in line for line in screen.lines))
            session.send("\x1b[B")
            session.expect("arrow selects alpha", lambda screen: any("> needle-alpha.txt" in line for line in screen.lines))
            session.send("j")
            session.expect("j selects beta", lambda screen: any("> needle-beta.txt" in line for line in screen.lines))
            session.send("k")
            session.expect("k selects alpha", lambda screen: any("> needle-alpha.txt" in line for line in screen.lines))
            events.append("arrow and Vim navigation")

            session.send("/\x01\x0bjkhq?/:")
            session.text("jkhq?/:")
            session.text("0 results")
            if session.poll() is not None:
                raise AssertionError("printable query text triggered quit")
            session.send("\x01\x0bneedle\r")
            session.text("complete · 4 results")
            events.append("printable shortcuts stay text while input is focused")

            session.send("?")
            session.text("Help ·")
            session.click(12, 2)
            session.text("Help ·")
            session.send("\x1b")
            session.text("Sources")
            session.send("S")
            session.text("Sources — Space toggles")
            session.send(" \r")
            session.text("complete · 4 results")
            session.send("S \r")
            session.text("complete · 4 results")
            events.append("help/modal mouse isolation and source toggles")

            session.send("s")
            session.text("Sort · Enter chooses")
            session.send("\x1b[B" * 4 + "\r")
            session.expect("size sort", lambda screen: "needle-beta.txt" in screen.lines[6])
            session.click(5, 7)
            session.expect("mouse selects beta", lambda screen: any("> needle-beta.txt" in line for line in screen.lines))
            session.send("\x1b[<65;5;8M")
            session.expect("wheel moves selection", lambda screen: any("> needle-alpha.txt" in line for line in screen.lines))
            events.append("column sorting, SGR mouse press/release and wheel")

            for columns, rows in ((80, 24), (40, 12), (120, 32)):
                session.resize(columns, rows)
                session.text("lazyfind")
                session.pump(0.15)
            session.text("needle-alpha.txt")
            events.append("80×24, narrow 40×12 and wide resize")

            session.text("Enter Fixture editor")
            session.send("\r")
            session.text("LAZYFIND_TEST_CHILD_READY")
            session.send("return\r")
            session.text("Action finished")
            handoff = json.loads(marker.read_text())
            if not handoff["echo"] or not handoff["canonical"]:
                raise AssertionError(f"child inherited raw terminal state: {handoff}")
            if Path(handoff["path"]).parent.resolve() != fixture.resolve():
                raise AssertionError("child received the wrong selected path")
            events.append("external editor sees restored ECHO/ICANON and returns to same query")

            session.send(":")
            session.text("Actions — Enter executes")
            session.send("Fixture failure")
            session.text("Fixture failure")
            session.send("\r")
            session.text("LAZYFIND_TEST_CHILD_READY")
            session.send("return\r")
            session.text("Action failed")
            events.append("failed external action restores usable dashboard")

            session.send("q")
            deadline = time.monotonic() + 10
            while session.poll() is None and time.monotonic() < deadline:
                session.pump(0.05)
            if session.poll() != 0:
                raise AssertionError(f"quit status: {session.exit_status}")
            session.pump(0.1)
            mask = termios.ECHO | termios.ICANON
            if not session.final_modes or session.final_modes["flags"] & mask != session.initial[3] & mask:
                raise AssertionError("quit did not restore echo/canonical input")
            if b"\x1b[?1049l" not in session.raw or b"\x1b[?1006l" not in session.raw:
                raise AssertionError("alternate screen or mouse was not disabled")
            events.append("quit restores terminal modes, alternate screen and mouse reporting")

            runs = json.loads(subprocess.check_output([binary, "history", "list", "--json"], env=env, cwd=fixture))
            if not runs or not any(run["query"]["raw"] == "needle" for run in runs):
                raise AssertionError("accepted search was not saved")
            if any("jkhq" in run["query"]["raw"] for run in runs):
                raise AssertionError("unaccepted keystrokes polluted history")
            events.append("accepted searches persist; intermediate typing does not")
            events.extend(exercise_patch(binary, base, env, config_dir, artifacts))
            if artifacts:
                artifacts.mkdir(parents=True, exist_ok=True)
                (artifacts / "transcript.ansi").write_bytes(session.raw)
                (artifacts / "verification.json").write_text(json.dumps({"passed": events, "history_runs": len(runs)}, indent=2) + "\n")
        except Exception:
            if artifacts:
                artifacts.mkdir(parents=True, exist_ok=True)
                (artifacts / "failure.ansi").write_bytes(session.raw)
                (artifacts / "failure-screen.txt").write_text(session.screen.text)
            raise
        finally:
            session.close()
    return events


def exercise_patch(binary, base, inherited_env, config_dir, artifacts):
    """Check patch interactions against real tools, tracing expensive calls."""
    events = []
    fixture = base / "patch-fixture"
    fixture.mkdir()
    (fixture / "needle-guide.md").write_text("needle first\nplain middle\nneedle third\n")
    (fixture / "needle-plan.md").write_text("needle plan\n")
    (fixture / "notes.txt").write_text("unrelated notes\n")
    for directory in ("archive", "downloads"):
        (fixture / directory).mkdir()
        (fixture / directory / "payload.bin").write_bytes(b"x" * 8192)
    trace = base / "tool-trace.jsonl"
    delay_du = base / "delay-du"
    tools_dir = base / "tools"
    tools_dir.mkdir()
    real_tools = {"fd": shutil.which("fd") or shutil.which("fdfind"), "rg": shutil.which("rg"), "du": shutil.which("du")}
    for tool, real in real_tools.items():
        if not real:
            raise RuntimeError(f"PTY patch verification requires installed {tool}")
        wrapper = tools_dir / tool
        wrapper.write_text(
            f"#!{sys.executable}\n"
            "import json,os,sys,time\n"
            f"with open({str(trace)!r},'a') as out:\n"
            f" out.write(json.dumps({{'tool':{tool!r},'argv':sys.argv[1:],'cwd':os.getcwd()}})+'\\n')\n"
            f"if {tool!r}=='du' and os.path.exists({str(delay_du)!r}): time.sleep(5)\n"
            f"os.execv({real!r},[{real!r}]+sys.argv[1:])\n"
        )
        wrapper.chmod(0o700)
    env = inherited_env.copy()
    env.pop("NO_COLOR", None)
    env["PATH"] = str(tools_dir) + os.pathsep + env.get("PATH", "")
    (config_dir / "config.toml").write_text(
        "[tools]\n" + f"fd={json.dumps(str(tools_dir / 'fd'))}\nrg={json.dumps(str(tools_dir / 'rg'))}\n"
        "[ui]\ncolor='always'\nmouse=true\ninitial_focus='results'\n"
        "highlight_matches=true\npreview_line_numbers=true\n"
        "[search]\nauto_search_empty=false\n"
    )

    def calls(tool=None):
        records = [json.loads(line) for line in trace.read_text().splitlines()] if trace.exists() else []
        # Capability checks do not search the fixture and are not expensive
        # traversal/matching calls.
        return [record for record in records if (tool is None or record["tool"] == tool)
                and "--version" not in record["argv"]]

    def history_runs():
        return json.loads(subprocess.check_output([binary, "history", "list", "--json"], env=env, cwd=fixture))

    session = Session([binary, str(fixture)], env, str(fixture))
    try:
        session.text("empty-query automatic search disabled")
        session.pump(0.5)
        if calls("fd") or calls("rg"):
            raise AssertionError(f"idle startup searched: {calls()}")
        session.send("j")
        if "j" in session.screen.lines[1]:
            raise AssertionError("results startup interpreted navigation as search text")
        events.append("results startup and disabled empty automatic search perform no backend work")

        session.send("i\x00")
        session.text("Conditions ·")
        session.text("ext:")
        session.send("\x1b[B\r")  # type → ext qualifier
        session.text("ext:")
        session.send("md\r")
        session.expect("completion inserts ext:md", lambda screen: "ext:md" in screen.lines[1])
        session.send("\r")
        session.text("complete · 2 results")
        events.append("Ctrl+Space qualifier and value completion insert an executable ext:md query")

        session.send("/\x01\x0bneedle ext:md")
        session.send("\x1b")  # close the qualifier value popup
        session.send("\r")
        session.text("complete · 2 results")
        session.text("needle first")
        session.expect("native preview line numbers", lambda screen: re.search(r"\b1\s*│ needle first", screen.text))
        # Lip Gloss uses ANSI yellow (3) for the exact match region, with a
        # reset immediately after the keyword. This rejects whole-row color.
        yellow = re.compile(rb"\x1b\[(?:[0-9;]*;)?(?:43|48;5;3)(?:;[0-9;]*)?mneedle\x1b\[[0-9;]*m")
        if not yellow.search(session.raw):
            raise AssertionError("no exact yellow ANSI highlight around needle")
        session.send("L")
        session.text("needle first")
        if re.search(r"\b1\s*│ needle first", session.screen.text):
            raise AssertionError("preview line numbers remained after L")
        session.send("L")
        session.expect("line numbers restored", lambda screen: re.search(r"\b1\s*│ needle first", screen.text))
        events.append("yellow highlights wrap the exact match and L toggles native preview line numbers")

        session.pump(0.3)
        searches = (len(calls("fd")), len(calls("rg")))
        session.send("\x06guide")
        session.text("needle-guide.md")
        session.expect("current-results filter leaves one row", lambda screen: "1/2" in screen.text or "1 / 2" in screen.text)
        session.pump(0.4)
        if searches != (len(calls("fd")), len(calls("rg"))):
            raise AssertionError("Ctrl+F reran a search backend")
        session.send("\x01\x0b\x1b")
        session.text("needle-plan.md")
        events.append("Ctrl+F filters loaded results without restarting fd or rg")

        session.send(":")
        session.text("Actions —")
        if "Search" not in session.screen.lines[1]:
            raise AssertionError("Actions popup erased background search context")
        session.send("Copy")
        session.send("\r")
        session.text("Copy")
        session.text("Absolute path")
        session.text("Relative path")
        session.send("\x1b")
        session.text("Actions —")
        session.send("\x1b")
        session.send("y\x1b[B\x1b[B\x1b[B\r")
        session.text("Copied needle-guide.md:1")
        if base64.b64encode(b"needle-guide.md:1") not in session.raw:
            raise AssertionError("copy did not send the selected root-relative native location to the terminal clipboard")
        session.send("?")
        session.text("Help ·")
        session.text("FLOW")
        session.click(12, 2)
        session.text("Help ·")
        session.send("mtime")
        session.text("mtime:")
        session.resize(40, 12)
        session.text("Help ·")
        session.click(1, 1)
        session.text("Help ·")
        session.resize(120, 32)
        session.text("mtime:")
        session.send("\x1b")
        session.text("needle-guide.md")
        events.append("centered Actions/Copy and searchable Help preserve background and isolate mouse input")

        # Both accepted ext queries exist; deleting one must return to the same
        # filtered history list, leaving its neighboring ext query visible.
        session.send("H")
        session.text("History")
        session.send("ext:md")
        session.text("needle ext:md")
        before = history_runs()
        target = next(run for run in before if run["query"]["raw"] == "needle ext:md")
        session.send("\x04")
        session.text("Delete")
        session.send("\r")  # default confirmation choice must be Cancel
        session.text("History")
        if not any(run["id"] == target["id"] for run in history_runs()):
            raise AssertionError("default confirmation deleted history")
        session.click_text("[Delete]")
        session.text("Delete")
        session.send("\x1b[B\r")
        session.text("History entry deleted")
        session.text("History")
        session.text("ext:md")
        if any(run["id"] == target["id"] for run in history_runs()):
            raise AssertionError("confirmed history deletion retained selected run")
        session.send("\x1b")
        events.append("history deletion defaults to Cancel, then deletes only the selected run and returns to filtered history")

        session.send("/\x01\x0btype:dir")
        session.send("\x1b")
        session.send("\r")
        session.text("complete · 2 results")
        session.text("archive")
        session.send("u")
        session.text("recursive; may be slow")
        session.text("Calculate visible directories (2)")
        session.send("\r")
        session.text("Disk usage 1/1")
        session.pump(0.3)
        if len(calls("du")) != 1:
            raise AssertionError(f"selected directory usage calls: {calls('du')}")
        session.send("u\r")
        session.text("Disk usage 1/1")
        session.pump(0.3)
        if len(calls("du")) != 1:
            raise AssertionError("session cache reran du")
        session.send("u\x1b[B\r")
        session.text("Disk usage 2/2")
        session.pump(0.3)
        if len(calls("du")) != 2:
            raise AssertionError("visible directory batch did not reuse completed session measurement")
        session.send("/\x01\x0bnotes\r")
        session.text("complete · 1 results")
        session.send("/\x01\x0btype:dir")
        session.send("\x1b")
        session.send("\r")
        session.text("complete · 2 results")
        session.send("u\r")
        session.text("Disk usage 1/1")
        session.pump(0.3)
        if len(calls("du")) != 2:
            raise AssertionError("changing query discarded the directory session cache")
        delay_du.touch()
        session.send("u\x1b[B\x1b[B\r")
        session.expect("forced directory measurement starts", lambda screen: len(calls("du")) == 3)
        session.send("\x1b")
        session.text("Directory usage canceled")
        delay_du.unlink()
        session.send("u\r")
        session.text("Disk usage 1/1")
        session.pump(0.3)
        if len(calls("du")) != 4:
            raise AssertionError("cancelled forced measurement incorrectly returned an old cached value")
        events.append("on-demand du measures selected/visible directories, reuses session cache, forces refresh and cancels")

        session.send("q")
        deadline = time.monotonic() + 10
        while session.poll() is None and time.monotonic() < deadline:
            session.pump(0.05)
        if session.poll() != 0:
            raise AssertionError(f"patch-session quit status: {session.exit_status}")
        if any(run["id"] == target["id"] for run in history_runs()):
            raise AssertionError("deleted history was resurrected by final save")
        events.append("deleted history remains absent after later queries and quit")
        if artifacts:
            artifacts.mkdir(parents=True, exist_ok=True)
            (artifacts / "patch-transcript.ansi").write_bytes(session.raw)
            (artifacts / "tool-trace.jsonl").write_bytes(trace.read_bytes())
    except Exception:
        if artifacts:
            artifacts.mkdir(parents=True, exist_ok=True)
            (artifacts / "patch-failure.ansi").write_bytes(session.raw)
            (artifacts / "patch-failure-screen.txt").write_text(session.screen.text)
            if trace.exists():
                (artifacts / "tool-trace.jsonl").write_bytes(trace.read_bytes())
        raise
    finally:
        session.close()
    return events


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=lambda value: str(Path(value).resolve()))
    parser.add_argument("--artifacts", type=Path)
    args = parser.parse_args()
    print(json.dumps({"passed": exercise(args.binary, args.artifacts)}, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
