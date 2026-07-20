from __future__ import annotations

from dataclasses import dataclass
import os
from pathlib import Path
import signal
import subprocess
import tempfile
from typing import Mapping, Sequence

from .errors import ReleaseError


_MAX_OUTPUT_BYTES = 16 * 1024 * 1024


@dataclass(frozen=True)
class ProcessResult:
    argv: tuple[str, ...]
    exit_code: int
    stdout: str
    stderr: str


def run(
    argv: Sequence[str],
    cwd: Path,
    env: Mapping[str, str],
    timeout: int,
) -> ProcessResult:
    arguments = tuple(argv)
    if not arguments or len(arguments) > 4096:
        raise ReleaseError("PROCESS_REQUEST_INVALID", "argv must contain between 1 and 4096 elements")
    if any(not isinstance(item, str) or not item or "\x00" in item for item in arguments):
        raise ReleaseError("PROCESS_REQUEST_INVALID", "argv contains an invalid element")
    directory = cwd.resolve()
    if not directory.is_dir():
        raise ReleaseError("PROCESS_REQUEST_INVALID", f"working directory is missing: {directory}")
    if timeout <= 0 or timeout > 24 * 60 * 60:
        raise ReleaseError("PROCESS_REQUEST_INVALID", "timeout is outside the reviewed range")
    environment: dict[str, str] = {}
    for key, value in env.items():
        if (
            not isinstance(key, str)
            or not isinstance(value, str)
            or not key
            or "=" in key
            or "\x00" in key
            or "\x00" in value
        ):
            raise ReleaseError("PROCESS_REQUEST_INVALID", "environment contains an invalid entry")
        environment[key] = value

    creation_flags = subprocess.CREATE_NEW_PROCESS_GROUP if os.name == "nt" else 0
    with tempfile.TemporaryFile() as stdout_file, tempfile.TemporaryFile() as stderr_file:
        try:
            process = subprocess.Popen(
                arguments,
                cwd=directory,
                env=environment,
                stdin=subprocess.DEVNULL,
                stdout=stdout_file,
                stderr=stderr_file,
                shell=False,
                close_fds=True,
                start_new_session=os.name != "nt",
                creationflags=creation_flags,
            )
        except OSError as failure:
            raise ReleaseError("PROCESS_START_FAILED", f"cannot start {arguments[0]}: {failure}") from failure
        try:
            exit_code = process.wait(timeout=timeout)
        except subprocess.TimeoutExpired as failure:
            _terminate_process_group(process)
            process.wait(timeout=10)
            raise ReleaseError("PROCESS_TIMEOUT", f"process exceeded {timeout} seconds: {arguments[0]}") from failure
        stdout = _read_output(stdout_file, "stdout")
        stderr = _read_output(stderr_file, "stderr")
    return ProcessResult(arguments, exit_code, stdout, stderr)


def run_checked(
    argv: Sequence[str],
    cwd: Path,
    env: Mapping[str, str],
    timeout: int,
    failure_code: str,
) -> ProcessResult:
    result = run(argv, cwd, env, timeout)
    if result.exit_code != 0:
        detail = result.stderr.strip() or result.stdout.strip() or f"exit {result.exit_code}"
        raise ReleaseError(failure_code, detail[-4096:])
    return result


def _terminate_process_group(process: subprocess.Popen[bytes]) -> None:
    try:
        if os.name == "nt":
            process.kill()
        else:
            os.killpg(process.pid, signal.SIGKILL)
    except (OSError, ProcessLookupError):
        process.kill()


def _read_output(stream: object, name: str) -> str:
    stream.seek(0)
    encoded = stream.read(_MAX_OUTPUT_BYTES + 1)
    if len(encoded) > _MAX_OUTPUT_BYTES:
        raise ReleaseError("PROCESS_OUTPUT_LIMIT", f"{name} exceeded {_MAX_OUTPUT_BYTES} bytes")
    return encoded.decode("utf-8", errors="replace")
