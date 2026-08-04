# cmd

Ask for a shell command in plain English. Uses your existing Claude subscription — no API key.

```
$ cmd "show the 10 biggest files in this folder"

> du -ah . | sort -rh | head -10

Execute? [y/N]: y
```

## Install

Needs Go 1.23+ and [Claude Code](https://claude.com/claude-code) signed in.

```bash
go build -o cmd . && mv cmd /usr/local/bin/
cmd --init
```

## Usage

```
cmd [flags] "what you want to do"
something | cmd [flags] "what to do with it"
```

```bash
cmd "find TODO comments in src, with line numbers"   # grep -rn "TODO" src
cmd "what's using port 3000"                         # lsof -i :3000
cmd "delete node_modules folders under here"         # find . -name node_modules ...
cat package.json | cmd "list the dependency names in package.json"   # jq -r '.dependencies | keys[]' package.json
```

| Flag | |
|---|---|
| `-t` | Let it reason first. Slower, better on hard requests. |
| `-m <name>` | Model for this run. |
| `-q` | Print the command only. No prompt, no execution. |
| `-y` | Skip confirmation (risky commands still ask). |
| `--config` | Show settings in force. |
| `--init` | Write a default config. |
| `--debug` | Show the underlying `claude` call. |

Exit codes: `0` ok, `1` error, `2` you aborted. Otherwise the exit code of the command that ran.

## Piping data in

Pipe a file and the command is built for its real structure — actual keys, actual column numbers.

```bash
cat todo.json | cmd "list all titles"                 # jq -r '.[].title'
cat todo.json | cmd "list all titles in todo.json"    # jq -r '.[].title' todo.json
cat users.json | cmd "emails of everyone who's active"
cat access.log | cmd "count requests per status code"
```

Name a file in your request and the command targets that file. Otherwise it reads stdin.

Big files are safe: at most 256 KB is ever read, no matter how large the file. Past that, `cmd` sends a structure summary (key paths, types, column positions) instead of raw data, so a 50 MB file costs the same as a small one.

## Scripting

The command goes to stdout, everything else to stderr:

```bash
cmd -q "show my git branches sorted by date" > branches.sh
eval "$(cmd -q 'list files changed in the last hour')"
```

With output redirected, `cmd` prints and never executes.

## Config

`~/.cmd-config.json`, all fields optional:

```json
{
  "model": "haiku",
  "max_pipe_chars": 4000,
  "sample_read_bytes": 262144,
  "enable_think": false,
  "effort": "medium",
  "timeout_seconds": 120,
  "show_thinking": false,
  "dangerous_patterns": []
}
```

- `effort` — `low`/`medium`/`high`/`xhigh`/`max`, used with `-t`. Without `-t` it's always `low`.
- `show_thinking` — print reasoning. Only applies with `-t`.
- `dangerous_patterns` — your own regexes to flag, e.g. `["\\bprod-db\\b"]`.

Run `cmd --config` to see what's actually in force. `--init` won't overwrite an existing file.

## Safety

You always see the command before it runs. On top of that, destructive ones (`rm -rf`, `mkfs`, `curl | sh`, `git push --force`, `DROP DATABASE`, `sudo`, …) list *why* they're flagged and need the full word `yes` — a bare `y` aborts. `<placeholders>` are called out too.

It's a speed bump, not a sandbox. Read the command.

## Development

```bash
go vet ./... && go test ./...
```

The logic that matters — stream parsing, sampling, sanitizing, risk checks — is pure functions, so tests run offline without the `claude` binary.
