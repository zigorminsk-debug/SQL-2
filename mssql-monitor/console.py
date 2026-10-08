"""UTF-8 output when the CLI is captured by Actions, a pipe, or a log file."""
import sys


def configure_console(streams=None):
    for stream in (sys.stdout, sys.stderr) if streams is None else streams:
        # Windowed PyInstaller executables have no stdout/stderr. Leave real
        # terminals to Python's platform-specific console encoding handling.
        if stream is not None and hasattr(stream, "reconfigure") and not stream.isatty():
            stream.reconfigure(encoding="utf-8", errors="backslashreplace")
