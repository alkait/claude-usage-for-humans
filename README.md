# Claude usage for humans

One verdict for your Claude subscription: **use more**, **on track**,
**slow down**, **running out**, or **wait**.

Claude's own usage screen tells you *how much* you have used. It does not tell
you whether that is a problem. 68% of the week gone with 2 days left is fine;
68% gone with 5 days left is not. Doing that arithmetic across a 5-hour window
nested in a 7-day window, per model, every time you wonder whether to send the
next prompt, is exactly the kind of thing nobody does. So people either hit the
wall mid-task, or they get cautious and leave a third of a paid plan unused.

`cuh` (Claude usage for humans) does the arithmetic and gives you the answer.

## How it is put together

```
   Anthropic usage endpoint
             ▲  one request every 5 minutes
             │
   ┌─────────┴──────────┐        /now (JSON)        ┌──────────────────────┐
   │  cuh serve │ ◄──────────────────────── │ web dashboard (built  │
   │  in a container     │                           │ into the server)      │
   │  keeps history      │ ◄──────────────────────── │ terminal client       │
   └────────────────────┘                           │ status line, popup    │
                                                    └──────────────────────┘
```

- **The server** (`cuh serve`) is the only thing that talks to
  Anthropic. It samples every 5 minutes, keeps every sample forever as monthly
  JSONL files, measures your pace from that history, computes the verdict, and
  serves it all on `/now`. It runs anywhere Docker runs.
- **The web dashboard** is embedded in the server at `/`.
- **The terminal client** is the same binary, run without arguments. It reads
  from the server, never from Anthropic directly once a server is configured.

## Run it locally

Prerequisites: Docker with Compose, and Claude Code logged in on this machine
(the server borrows its login).

```sh
cp .env.example .env          # set CUH_SECRET and CUH_CREDENTIALS
make up                       # build the image, start the container
open http://localhost:8787/   # the dashboard; enter the secret once
```

`make logs` follows the server, `make sample` prints what `/now` answers,
`make down` stops it. On Fedora and other SELinux systems the Compose file
already runs the container unconfined so the bind mounts work.

Point the terminal client at it and it stays in sync with the dashboard:

```sh
make install                                                    # ~/.local/bin/cuh
cuh config remote http://localhost:8787 --secret <the secret in .env>
cuh            # the panel
cuh -s         # one line, for status lines
cuh --watch    # live, q quits
```

For Claude Code's status line, in `~/.claude/settings.json`:

```json
{ "statusLine": { "type": "command", "command": "cuh -s" } }
```

Run `make up` as yourself, not with `sudo`: the container runs under your user
id so the data directory and the credentials file stay yours, and `$HOME`
changes under sudo. If your shell predates your membership in the `docker`
group, use `sg docker -c "make up"` or open a new terminal.

## Deploy to a server

The server needs three things: the image, a `.env`, and a Claude login of its
own.

**1. A login for the server.** Do not copy the credentials file from a machine
where Claude Code is in use: renewing the token rotates the refresh token and
would log that machine out. Instead, create a separate login on your laptop
into its own directory, and give that file to the server:

```sh
CLAUDE_CONFIG_DIR=~/claude-server-login claude     # then /login inside it
scp ~/claude-server-login/.credentials.json server:/srv/cuh/credentials.json
```

Anthropic treats it as another device. The server renews the token itself
from then on. Refresh tokens do expire eventually (about a month at the time
of writing); when that happens the dashboard says so and tells you to log in
again on the server. Repeat the two commands above to resume.

**2. Files on the server.**

```sh
git clone <this repo> /srv/cuh && cd /srv/cuh
cp .env.example .env
```

In `.env` set a long random `CUH_SECRET`, and
`CUH_CREDENTIALS=/srv/cuh/credentials.json`.

**3. Start it.**

```sh
docker compose up -d --build
docker compose logs -f     # expect "sample session N% weekly_all N% ..." every 5 minutes
```

The image is built on the server from the Dockerfile, so any architecture
Docker supports works, including a Raspberry Pi. To update: `git pull` and
`docker compose up -d --build`. History lives in `./data` and survives
rebuilds; back it up if you care about it.

**4. Point your devices at it.** On each laptop:

```sh
cuh config remote http://server:8787 --secret <the secret>
```

and open `http://server:8787/` in a browser. The dashboard asks for the secret
once and remembers it. The gear in its header opens settings, where you can
change the secret the browser sends and test it against the server. Keep the port on your LAN or behind a VPN or reverse
proxy with TLS; the secret is the only lock on it.

## The verdict

The verdict is driven by whichever window is tightest for *your* plan. On Max
20x that is usually the weekly cap; on Pro it is usually the 5-hour session.

| Verdict | Trigger | Meaning |
|---|---|---|
| 🟢 **USE MORE** | landing under 85% at the reset | Quota will go unused. Reach for the big model, run the refactor. |
| 🔵 **ON TRACK** | landing 85% to 105% | Right pace. |
| 🟡 **SLOW DOWN** | landing 105% to 125% | You will brush the limit before the reset. |
| 🔴 **RUNNING OUT** | landing over 125% | You will hit the limit with hours to go. |
| ⏳ **WAIT** | already at 100% | Nothing to do until the reset. |
| ⚪ **NO PACE YET** | window just started, no history | Check back shortly. |

"Landing" is where you end up at the reset if the current pace holds. The pace
is your **recent** burn (the last eighth of the window), so idle days followed
by a heavy hour do not read as "on track". A window that has just started
assumes your **usual** pace, the median burn across everything the server has
recorded for that limit, until it has evidence of its own. Once there is
enough history, unusual burn is called out: "2.1× your usual pace".

## What is stored

One line per sample in `data/history-YYYY-MM.jsonl`: timestamp, every limit's
percent and reset time, the weekly split by surface (Claude Code, chats,
Cowork), and extra-usage spend. About 250 bytes each, under 200 KB a month.
Nothing is deleted. The server keeps 90 days in memory for pace computation
and reads older months from disk only when asked.

Client-side files (`cuh --paths` prints them): `config.json` with the
server address and secret, `remote.json` with the last server answer (shown as
stale if the server stops answering), and a small local cache used only when
no server is configured. `cuh --reset` clears them.

## Endpoints

| Endpoint | Returns |
|---|---|
| `GET /` | The dashboard. No auth; it asks for the secret itself. |
| `GET /now` | Current usage, plan, per-limit rates, and the computed view: verdict, headline, each limit's assessment, and whether recording is active. What every client uses. |
| `GET /history?from=&to=` | Samples in a range (RFC3339 or `YYYY-MM-DD`), default the last 7 days. |
| `GET /history/YYYY-MM.jsonl` | One month of raw samples. |
| `GET /ping` | `{"ok":true,"secret_required":…}` once the secret is accepted, 401 otherwise. What the dashboard's settings use to test a secret. |
| `GET /healthz` | `ok`, no auth. |

All but `/` and `/healthz` require `Authorization: Bearer <secret>`.

## Rate limits

Anthropic's usage endpoint rate-limits aggressive polling. The server makes
one request per 5 minutes, total, for all your devices. On HTTP 429 it backs
off exponentially, honours `Retry-After`, and keeps serving the last good
numbers. A client without a server follows the same rules with a 3-minute
cache and a lock against concurrent fetches.

## Development

```sh
make build      # local binary
make test       # 23 tests: pace math, verdict thresholds, backoff, token
                # refresh, history storage, server endpoints
make release    # all six platform binaries into dist/
make serve      # run the server on the host without a container
```

Go 1.22 or newer. Build-time dependencies are Lip Gloss for the terminal
panel and golang.org/x/term; the binary is static and needs no runtime.
