# Operating loop24-gateway

This document covers the developer-laptop lifecycle for loop24-gateway:
starting and stopping the gateway in the background, where PID and log
files live, env-var overrides for the wrapper scripts, and how the
`status` subcommand determines whether the gateway is healthy.

The Go binary is a single foreground process. Two wrapper scripts own
process supervision on developer laptops — the binary itself has no
`start`/`stop` subcommands. See
[`scripts/loop24`](../scripts/loop24) (POSIX) and
[`scripts/loop24.ps1`](../scripts/loop24.ps1) (PowerShell).

## Quick Start (macOS / Linux)

```bash
make build               # compile bin/loop24-gateway

./scripts/loop24 start   # launch in background
./scripts/loop24 status  # check PID + /health
./scripts/loop24 stop    # send SIGTERM, wait for exit
```

Makefile shortcuts delegate to the same script:

```bash
make start
make status
make stop
```

## Quick Start (Windows)

```powershell
make build

.\scripts\loop24.ps1 start
.\scripts\loop24.ps1 status
.\scripts\loop24.ps1 stop
```

If PowerShell blocks execution due to execution policy, run via:
`powershell -ExecutionPolicy Bypass -File .\scripts\loop24.ps1 start`

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `start` | Launch gateway in the background; write PID file; append stdout (and stderr on Windows) to log files |
| `stop` | Send SIGTERM (macOS/Linux) or `Kill()` (Windows); wait up to 10 s for clean exit; remove PID file |
| `status` | Check PID file exists, verify process is alive, then GET `/health` and print JSON response |
| `restart` | `stop` then `start` — race-free because `stop` waits for process exit before returning |
| `logs [-f]` | Tail the log file; pass `-f` to follow (macOS/Linux); Windows tails both stdout and stderr files simultaneously |
| `run` | Run gateway in the foreground — equivalent to invoking the binary directly |

## File Locations

| File | macOS / Linux default | Windows default |
|------|-----------------------|-----------------|
| Binary | `./bin/loop24-gateway` | `.\bin\loop24-gateway.exe` |
| PID file | `/tmp/loop24-gateway.pid` | `%TEMP%\loop24-gateway.pid` |
| Log file (stdout) | `/tmp/loop24-gateway.log` | `%TEMP%\loop24-gateway.log` |
| Log file (stderr) | merged into stdout | `%TEMP%\loop24-gateway-err.log` |

On macOS/Linux, stdout and stderr are both redirected to the single log
file via `nohup ... >> $LOOP24_LOG 2>&1`. On Windows, `Start-Process`
cannot redirect both streams to the same file, so stdout and stderr go
to separate files. The `logs` subcommand tails both files simultaneously
using background jobs.

## Environment Variable Overrides

These variables control the wrapper scripts. Set them in your shell
before calling the script.

| Variable | Default | Description |
|----------|---------|-------------|
| `LOOP24_BIN` | `./bin/loop24-gateway` (macOS/Linux) / `.\bin\loop24-gateway.exe` (Windows) | Path to the gateway binary |
| `LOOP24_PID` | `/tmp/loop24-gateway.pid` (macOS/Linux) / `%TEMP%\loop24-gateway.pid` (Windows) | PID file location |
| `LOOP24_LOG` | `/tmp/loop24-gateway.log` (macOS/Linux) / `%TEMP%\loop24-gateway.log` (Windows) | Log file location (stdout) |
| `LOOP24_LOGERR` | `%TEMP%\loop24-gateway-err.log` | Stderr log file location — Windows only; not applicable on macOS/Linux where stderr is merged into stdout |
| `LOOP24_ADDR` | `http://localhost:11434` | Gateway address used by the `status` subcommand for the `/health` probe |

Example — redirect logs to a project-specific directory:

```bash
export LOOP24_LOG=~/Projects/loop24/gateway.log
export LOOP24_PID=~/Projects/loop24/gateway.pid
./scripts/loop24 start
```

## Gateway Environment Variables

These are set in your shell before calling the wrapper; they pass
through to the gateway binary unchanged.

| Variable | Default | Description |
|----------|---------|-------------|
| `HTTP_ADDR` | `:11434` | Bind address for the HTTP server (e.g., `:8080` or `127.0.0.1:11434`) |
| `KIRO_CMD` | `kiro-cli` | kiro-cli binary name or full path. If unset, the gateway starts without ACP worker processes. |
| `KIRO_ARGS` | `acp` | Arguments passed to kiro-cli (space-separated) |
| `KIRO_CWD` | _(empty)_ | Default working directory for kiro-cli subprocesses |
| `DEBUG` | `false` | Enable debug-level JSON logging. Accepts `1`, `true`, `0`, or `false`. |
| `PING_INTERVAL` | `60000` | ACP ping interval. Default: 60 s. Integer values are treated as milliseconds (e.g., `60000` = 60 s); Go duration strings are also accepted (e.g., `"90s"`, `"2m"`). |

Example — run with a custom binary path and debug logging:

```bash
export KIRO_CMD=~/.local/bin/kiro-cli
export DEBUG=true
./scripts/loop24 start
```

## How `status` Works

The `status` subcommand combines two checks:

1. **PID file check.** If no PID file exists at `$LOOP24_PID`, the
   gateway is stopped. If the file exists but the process is gone
   (stale PID), `status` reports `stopped (stale PID)` and exits
   non-zero.

2. **Process liveness check.** On macOS/Linux, `kill -0 $pid` probes
   whether the process is alive without sending a signal. On Windows,
   `Get-Process -Id $pid` is used.

3. **Health probe.** If the process is alive, `status` sends
   `GET $LOOP24_ADDR/health` and prints the JSON response. The
   response includes gateway version, uptime seconds, and pool/session/
   embedding stats.

Exit codes: 0 if the gateway is running and health check succeeded;
non-zero if stopped or the PID file is stale.

## Logs

Log format is JSON (`log/slog` with `slog.NewJSONHandler`). Each line
is a single JSON object with keys `time`, `level`, `msg`, and
request-scoped keys (`request_id`, `method`, `path`, `status`,
`duration`).

Viewing logs:

```bash
./scripts/loop24 logs        # last 50 lines (macOS/Linux)
./scripts/loop24 logs -f     # follow (macOS/Linux)
.\scripts\loop24.ps1 logs    # tail both stdout + stderr (Windows)
```

On macOS/Linux, stdout and stderr are merged into a single file, so
`logs` shows all output. On Windows, `logs` tails both
`%TEMP%\loop24-gateway.log` and `%TEMP%\loop24-gateway-err.log`
simultaneously.

> **Note:** Log files are not rotated. For extended development
> sessions, truncate manually: `> /tmp/loop24-gateway.log` (macOS/Linux)
> or `Clear-Content $env:TEMP\loop24-gateway.log` (Windows).
