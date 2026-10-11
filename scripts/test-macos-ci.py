#!/usr/bin/env python3
"""Run the real macOS suite in a bounded, disposable Lima home."""
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tempfile


class Interrupted(Exception):
    def __init__(self, signum):
        self.signum = signum


def command(args, env, cwd, log, timeout):
    print("Running: " + " ".join(args), flush=True)
    with log.open("wb") as output:
        process = subprocess.Popen(args, env=env, cwd=cwd, stdout=output,
                                   stderr=subprocess.STDOUT, start_new_session=True)
        try:
            return process.wait(timeout=timeout)
        except (subprocess.TimeoutExpired, Interrupted) as error:
            os.killpg(process.pid, signal.SIGTERM)
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait(timeout=10)
            if isinstance(error, Interrupted):
                raise
            output.write(b"\nmacOS CI command exceeded its deadline\n")
            return 124
        finally:
            output.flush()
            # Bound console output; the complete command log is an artifact.
            with log.open("rb") as saved:
                saved.seek(max(0, log.stat().st_size - 16 * 1024))
                print(saved.read().decode(errors="replace"), flush=True)


def run_suite(project, artifacts, timeout=2100, temporary_parent="/private/tmp", shared_home=None):
    artifacts.mkdir(parents=True, exist_ok=True, mode=0o700)
    run = Path(tempfile.mkdtemp(prefix="ck-e2e-", dir=temporary_parent))
    try:
        # Engine installation stages payload in machine state and transfers it
        # through the writable home share. Keep it out of daily machine state.
        state = Path(tempfile.mkdtemp(prefix=".casklet-e2e-state-",
                                     dir=shared_home or Path.home()))
    except OSError:
        shutil.rmtree(run)
        raise
    env = os.environ.copy()
    # Never adopt a caller's daily Lima home or machine state. Keep the ordinary
    # home share, which the live-bind and terminal tests intentionally exercise.
    env["LIMA_HOME"] = str(run / "lima")
    env["CASKLET_MACHINE_STATE_DIR"] = str(state)
    env["LIMA_SSH_PORT_FORWARDER"] = "false"
    env["CASKLET_MACOS_RESIZE_GROW"] = "1"
    (run / "lima").mkdir(mode=0o700)
    (artifacts / "run-directory.txt").write_text(str(run) + "\n")
    (artifacts / "state-directory.txt").write_text(str(state) + "\n")
    result = 1
    cleanup_ok = False
    try:
        result = command(["make", "test-macos"], env, project,
                         artifacts / "suite.log", timeout)
    except Interrupted as interrupt:
        result = 128 + interrupt.signum
    except (OSError, subprocess.SubprocessError) as error:
        (artifacts / "suite-error.log").write_text(str(error) + "\n")
    finally:
        # A second catchable signal must not interrupt bounded cleanup.
        old = {sig: signal.signal(sig, signal.SIG_IGN)
               for sig in (signal.SIGHUP, signal.SIGINT, signal.SIGTERM)}
        try:
            command(["limactl", "--tty=false", "list", "--json"], env, project,
                    artifacts / "instances.log", 30)
            instance = run / "lima" / "casklet-runtime"
            if instance.exists():
                command(["limactl", "--tty=false", "shell", "casklet-runtime",
                         "sudo", "-n", "journalctl", "--no-pager", "-n", "1000"],
                        env, project, artifacts / "guest-journal.log", 45)
                # Copy logs only, never disks, SSH keys, or credential files.
                logs = artifacts / "lima-logs"
                logs.mkdir(exist_ok=True)
                for source in sorted(instance.glob("*.log"))[:20]:
                    if source.is_symlink() or not source.is_file():
                        continue
                    with source.open("rb") as stream:
                        stream.seek(max(0, source.stat().st_size - 4 * 1024 * 1024))
                        (logs / source.name).write_bytes(stream.read())
                command(["limactl", "--tty=false", "stop", "--force", "casklet-runtime"],
                        env, project, artifacts / "stop.log", 60)
                cleanup_ok = command(
                    ["limactl", "--tty=false", "delete", "--force", "casklet-runtime"],
                    env, project, artifacts / "delete.log", 60) == 0 and not instance.exists()
            else:
                cleanup_ok = True
        except (OSError, subprocess.SubprocessError) as error:
            (artifacts / "cleanup-error.log").write_text(str(error) + "\n")
        finally:
            for sig, handler in old.items():
                signal.signal(sig, handler)
        if cleanup_ok:
            shutil.rmtree(run)
            shutil.rmtree(state)
        else:
            print(f"VM cleanup failed; preserve {run} and {state} for operator recovery", flush=True)
            result = result or 1
    return result


def main():
    if sys.platform != "darwin" or os.geteuid() == 0:
        sys.exit("macOS CI requires a regular user on a dedicated Mac")
    if sys.version_info < (3, 9):
        sys.exit("Python 3.9 or newer is required")
    for executable in ("make", "go", "limactl"):
        if not shutil.which(executable):
            sys.exit(f"Required command is missing: {executable}")
    timeout = int(os.environ.get("CASKLET_MACOS_CI_TIMEOUT_SECONDS", "2100"))
    if not 60 <= timeout <= 3600:
        sys.exit("CASKLET_MACOS_CI_TIMEOUT_SECONDS must be between 60 and 3600")
    artifacts = Path(os.environ.get("CASKLET_MACOS_CI_ARTIFACTS", "bin/macos-e2e-artifacts")).resolve()
    project = Path(__file__).resolve().parent.parent
    def interrupted(signum, _frame):
        raise Interrupted(signum)
    for sig in (signal.SIGHUP, signal.SIGINT, signal.SIGTERM):
        signal.signal(sig, interrupted)
    return run_suite(project, artifacts, timeout)


if __name__ == "__main__":
    sys.exit(main())
