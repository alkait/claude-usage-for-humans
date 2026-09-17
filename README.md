# cuh · Claude usage for humans

**Get the most out of your Claude subscription, based on how you actually use it.**

Claude shows you a percentage. cuh reads your pace and tells you what it means,
right in Claude Code's status line:

- **Never hit the wall mid-task.** See a limit coming hours before it lands, and
  slow down while there is still time to finish.
- **Never leave frontier quota unused.** Know when you are on course to end the
  week with a third of the top model untouched, and reach for it instead.

```sh
$ cuh -s
🟢 USE MORE Fable · 38% may go unused  │  S 3% W 29% Fable 52%
```

## How it works

- **Each computer** you use Claude Code on runs `cuh -s` from the status line.
  When the server's numbers are over 5 minutes old, that run reads the local
  Claude Code login, fetches usage from Anthropic, and posts it to the server.
- **The server** holds no login. It stores every sample in one `history.jsonl`,
  measures your pace, and serves the verdict and the dashboard.

Nothing expires. You keep Claude Code logged in anyway; the server never needs to be.

## Server

Docker, on any always-on box (a Raspberry Pi works):

```sh
cp .env.example .env            # set CUH_SECRET to something long and random
make up                         # docker compose up -d --build
make logs                       # one "sample ..." line per post
make down
```

Without Docker:

```sh
make build
./cuh serve --listen :8787 --data-dir ./data --secret <secret>
```

Dashboard: `http://<server>:8787/`. It asks for the secret once.

## Each computer with Claude Code

Install the client, then put the hook in `~/.claude/settings.json`:

```sh
make install                    # ~/.local/bin/cuh
```

```json
{ "statusLine": { "type": "command", "command": "cuh -s --remote http://<server>:8787 --secret <secret>" } }
```

Do this on **every** computer you use Claude Code from. Sampling only happens
where Claude Code is open, so a machine without the hook leaves gaps in the
history. Usage is account-level, so all machines report the same numbers and
totals are never lost, only detail.

Without `-s` the same command prints the full panel. `--json` prints data.

## When something is off

The status line says so. `⚠ server offline · last update 2h ago` means the
server is down and you see cached numbers. `⚠ last update 40m ago` means the
server is up but nothing has posted for a while. `⚠ sampling failed` means this
machine could not fetch; run `cuh` without `-s` for the reason.

## Development

```sh
make test       # pace math, verdicts, client sampling and backoff, server storage
make release    # binaries for linux, macOS, windows into dist/
```
