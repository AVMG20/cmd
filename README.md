# cmd

Ask for a shell command in plain English.

```
$ cmd "show the 10 biggest files in this folder"

> du -ah . | sort -rh | head -10

Execute? [y] run  [c] copy  [n] cancel
```

One keypress decides. No Enter, no typing `y` then Return.

Or run `cmd` with nothing and type the request there, where `@` completes file
paths and `/` opens a command palette:

```
$ cmd
cmd 0.3.0
openrouter · google/gemini-3.7-flash
@ for files · / for commands · ctrl-d to leave

› strip the email column from @users.csv
                               ❯ users.csv
                                 src/models/users.go

> awk -F, -v OFS=, '{$3=""; print}' users.csv > users.tmp && mv users.tmp users.csv

Execute? [y] run  [c] copy  [e] edit  [n] cancel
```

It is not a shell and not an agent: it answers one request and exits on run,
copy or cancel. `cmd "..."` is still there and still faster when the request is
already in your head.

## Install

Needs Go 1.23+.

```bash
go build -o cmd . && mv cmd /usr/local/bin/
cmd --configure
```

`--configure` asks which backend to use and sets up whatever that choice needs.
Nothing else has to be edited by hand.

## Backends

| | Auth | Speed |
|---|---|---|
| **OpenRouter** | an API key | fastest — one HTTPS request |
| **Claude Code CLI** | your Claude subscription | slower — starts a Node process per run |
| **Antigravity CLI** (`agy`) | your Google account | slower — starts a Node process per run |

The CLI backends cost nothing extra but pay several hundred milliseconds of
process start-up before the model is even reached. For a twenty-token shell
command that start-up is most of the wait, which is why OpenRouter is the
quickest option even on the same class of model.

Switch any time with `cmd --configure`, or for one run with `-p`:

```bash
cmd -p claude "rewrite this rsync to preserve hardlinks"
```

### A note on cheap models

OpenRouter defaults to `google/gemini-3.7-flash`. Several fast models — that
one included — ship with reasoning **on** at medium effort, which is exactly the
slowness worth avoiding here. `cmd` asks for low effort and excludes the
reasoning from the response, so a plain run stays fast. `-t` turns it back up.

Any slug from [openrouter.ai/models](https://openrouter.ai/models) works;
`--configure` suggests a few and takes a custom one.

## Usage

```
cmd [flags] "what you want to do"
something | cmd [flags] "what to do with it"
```

```bash
cmd "find TODO comments in src, with line numbers"   # grep -rn "TODO" src
cmd "what's using port 3000"                         # lsof -i :3000
cmd "delete node_modules folders under here"         # find . -name node_modules ...
```

| Flag | |
|---|---|
| `-c` | Copy to the clipboard instead of running. |
| `-f <path>` | Include a file as context. Repeatable. |
| `-t` | Let it reason first. Slower, better on hard requests. |
| `-m <name>` | Model for this run. |
| `-p <name>` | Backend for this run: `claude`, `antigravity`, `openrouter`. |
| `-q` | Print the command only. No prompt, no execution. |
| `-y` | Skip confirmation (risky commands still ask). |
| `--configure` | Interactive setup. |
| `--config` | Show settings in force. |
| `--debug` | Show what is being sent. |

Exit codes: `0` ok, `1` error, `2` you aborted. Otherwise the exit code of the
command that ran.

## The harness

Run `cmd` with no request.

| | |
|---|---|
| `@name` | Complete a file path from the current directory. Matches anywhere in the path, so `@users` finds `src/models/users.go`. |
| `/` | Command palette, with descriptions. |
| `tab` / `enter` | Accept the highlighted completion. |
| `up` / `down` | Move through completions; with none open, previous requests, kept in `~/.cmd-history`. |
| `e` at the prompt | Edit the generated command before running it. |
| `ctrl-w` / `ctrl-u` | Delete the last word / the line. |
| `ctrl-c` | While the model is working, give up on that request. At the prompt, leave. |
| `ctrl-d` | Leave. |

`-c`, `-f`, `-y` and `--debug` carry into the harness: `cmd -c` opens it with
copy mode on, `cmd -f data.json` sends that file with every request.

| Command | |
|---|---|
| `/config` | The setup wizard, without leaving the session. Backend and model are chosen here. |
| `/think` | Toggle reasoning. |
| `/copy` | Toggle copying instead of running. |
| `/help` | Everything above. |
| `/exit` | Leave. |

`@` is the shortest way to name a file, and it sidesteps shell quoting
entirely — `what's using port 3000` needs no escaping when the shell never
sees it.

## Naming a file

Mention a file and `cmd` reads it, so the command is built for its real
structure — actual keys, actual column numbers — and **targets it by name**:

```bash
cmd "list all titles in todo.json"          # jq -r '.[].title' todo.json
cmd "strip the email column from users.csv" # awk -F, '{...}' users.csv > ...
cmd "count requests per status code in access.log"
```

That name is what makes file *mutation* work. A piped command can only read
stdin, and the pipe has already been consumed to build the prompt, so it cannot
even be re-run against your data. A named file has none of those problems.

A redirect works too — on Linux the path is recovered through `/proc/self/fd/0`:

```bash
cmd "drop rows with an empty id" < users.csv
```

Only words that look like paths are considered, and only if they resolve to a
real file, so ordinary prose reads nothing. `-f` names a file the request does
not mention. Set `"auto_read_files": false` to switch the behaviour off
entirely.

Piping still works and is unchanged:

```bash
cat access.log | cmd "count requests per status code"
```

Big files are safe: at most 256 KB is ever read, no matter how large the file.
Past that, `cmd` sends a structure summary (key paths, types, column positions)
instead of raw data, so a 50 MB file costs the same as a small one.

## Copying instead of running

Press `c` at the prompt, or pass `-c`, and the command goes to the clipboard
rather than the shell — for when it needs to run on a different box, or in a
terminal that does not have `cmd` installed.

`pbcopy`, `wl-copy`, `xclip`, `xsel` and `clip.exe` are used when present. When
none is, `cmd` falls back to an OSC 52 escape sequence, which asks the terminal
emulator itself to set the clipboard — the only thing that works over SSH, which
is usually where you are when you want the command somewhere else.

## Scripting

The command goes to stdout, everything else to stderr:

```bash
cmd -q "show my git branches sorted by date" > branches.sh
eval "$(cmd -q 'list files changed in the last hour')"
```

With output redirected, `cmd` prints and never executes.

## Config

`~/.cmd-config.json`. `cmd --configure` writes it; these are the fields it sets,
plus the ones only worth changing by hand:

```json
{
  "provider": "openrouter",
  "openrouter_model": "google/gemini-3.7-flash",
  "model": "haiku",
  "agy_path": "agy",
  "agy_model": "",
  "auto_read_files": true,
  "max_auto_files": 3,
  "effort": "medium",
  "timeout_seconds": 120,
  "show_thinking": false,
  "dangerous_patterns": []
}
```

- `provider` — `openrouter`, `claude` or `antigravity`.
- `openrouter_model` / `model` / `agy_model` — one per backend, so switching
  backends does not lose the model you picked for the other. An empty
  `agy_model` lets `agy` choose; run `agy models` to see the options.
- `effort` — `low`/`medium`/`high`/`xhigh`/`max`, used with `-t`. Without `-t`
  it is always `low`.
- `auto_read_files` — whether a path in the request may be opened.
- `dangerous_patterns` — your own regexes to flag, e.g. `["\\bprod-db\\b"]`.

The API key is read from `OPENROUTER_API_KEY` if set, otherwise from the config
file, which is written `0600`. Run `cmd --config` to see what is in force.

## Safety

You always see the command before it runs. Destructive ones (`rm -rf`, `mkfs`,
`curl | sh`, `git push --force`, `DROP DATABASE`, `sudo`, …) list *why* they are
flagged and need the full word `yes` typed out — the single-keypress prompt is
deliberately not used there, so no stray keystroke can run one.
`<placeholders>` are called out too.

It is a speed bump, not a sandbox. Read the command.

## Development

```bash
go vet ./... && go test ./...
```

No dependencies. The logic that matters — stream parsing, sampling, path
detection, sanitizing, risk checks — is pure functions, so tests run offline
without any backend installed.
