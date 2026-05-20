# logs-macos

Interactive TUI viewer for JSON log streams. Reads from stdin, syntax-highlights JSON, fades old log lines by age, and lets you search interactively.

![Go](https://img.shields.io/badge/go-1.24-blue)

## Features

- **JSON syntax highlighting** — keys, strings, numbers, booleans, nulls each in a distinct colour
- **Relative timestamps** — `ts`, `time`, `timestamp`, `t`, `@timestamp` fields replaced with a live relative age (`5s`, `2m30s`, `1h15m`, …)
- **Age-based fading** — recent logs (< 1 min) are brighter; older logs fade toward a muted sepia tone over 5 minutes
- **Line mode** (default) — each JSON object on one compact line; wide lines scroll horizontally
- **Multi-line mode** — pretty-printed indented JSON
- **Live search** — `/`-style interactive search with highlighted matches and `n`/`N` navigation
- **Separator** — press `-` to insert a visible horizontal rule between log bursts
- **Opaque black background** — works correctly with transparent-background terminals (Ghostty, etc.)

## Install

```bash
go install github.com/jig/logs-macos/cmd/lm@latest
```

This installs a binary named `lm` into `$(go env GOPATH)/bin`.

Or build locally:

```bash
git clone https://github.com/jig/logs-macos
cd logs-macos
go build -o lm ./cmd/lm
```

## Usage

Pipe any JSON log stream into it. Include `2>&1` to capture stderr as well:

```bash
./my-server 2>&1 | lm
```

```bash
kubectl logs -f my-pod 2>&1 | lm
```

```bash
journalctl -f -o json 2>&1 | lm
```

Non-JSON lines are passed through as plain text without crashing.

### Custom title

By default the status bar shows the auto-detected source command on the left.
For long pipelines you can override it with `--title`:

```bash
./my-server --some --very --long --flags 2>&1 | lm --title "my-server"
```

## Key bindings

| Key | Action |
|-----|--------|
| `l` | Toggle line mode / multi-line pretty-print |
| `←` `→` | Scroll horizontally (line mode) |
| `Home` `End` | Scroll to start / end of line (line mode), or top / bottom (multi-line) |
| `/` | Enter search |
| `n` `N` | Next / previous match |
| `Esc` | Clear search |
| `g` `G` | Scroll to top / bottom |
| `-` | Insert a separator bar |
| `q` | Quit `lm` (leaves the producing command running) |
| `Ctrl+C` | Quit and forward `SIGINT` to the rest of the pipeline |

## Supported timestamp fields

The following JSON keys are recognised as timestamps and displayed as relative age:

`ts` · `time` · `timestamp` · `t` · `@timestamp`

Accepted formats: RFC3339 / ISO 8601 strings, and Unix timestamps (integer or fractional seconds).
