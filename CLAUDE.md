# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
# Run the unified monitor + web server (default monitors "python2")
go run main.go -processes <name1>,<name2>

# Install dependencies
go mod download

# Build
go build ./...

# Run the Python 2 test load generator (requires Python 2)
python2 tester.py
```

## Architecture

A single Go program (`main.go`) that monitors multiple Linux processes and serves a live Chart.js dashboard.

- **`main.go`** — Starts a per-process collector goroutine (polls `/proc/<pid>/stat` and `/proc/<pid>/statm` every second) and an HTTP server on port 8090. Accepts a `-processes` flag (comma-separated, default `"python2"`).
  - `GET /` — inline HTML page with Chart.js charts (CPU and RSS, one line per process)
  - `GET /metrics` — JSON: `{ "<name>": [{t, cpu, vsize, rss}, ...], ... }` (last 300 samples)

- **`tester.py`** — Python 2 script that generates CPU and memory load, intended as a target process for the monitor.

The tool only works on Linux as it reads directly from the `/proc` filesystem.
