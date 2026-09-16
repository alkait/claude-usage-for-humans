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
  laptop A: Claude Code ── status line runs `cuh -s` ──┐
                                                       │  POST /sample every 5 min
  laptop B: Claude Code ── status line runs `cuh -s` ──┤  GET  /now   every redraw
                                                       ▼
                                             ┌──────────────────┐
                                             │    cuh serve     │  no Claude login
                                             │  history on disk │  verdict, pace
                                             │  web dashboard   │
                                             └──────────────────┘
```

- **Every computer you use Claude Code on runs `cuh`.** Claude Code's status
  line runs `cuh -s` on each redraw. When the server's numbers are more than
  5 minutes old, that run reads the local Claude Code login, fetches usage from
  Anthropic, and posts it to the server. Otherwise it just prints the verdict.
  So sampling happens exactly while you use Claude, on whichever machine you
  are using, with no daemon and no timer. The login is only read; Claude Code
  keeps it fresh because you use it.
- **The server holds no login.** It stores every sample in one JSONL file,
  stamps them with its own clock, keeps one per minute when two laptops post
  at once, measures your pace from the whole history, and serves the verdict
  on `/now` and the dashboard on `/`. Nothing in it ever expires.

Usage is account level, so every laptop reports the same truth, and gaps while
all laptops sleep lose no totals: Anthropic's numbers are cumulative per window.

## Set up

**The server**, on any always-on box with Docker (a Raspberry Pi works, the
image builds there):

```sh
git clone https://github.com/alkait/claude-usage-for-humans.git && cd claude-usage-for-humans
cp .env.example .env        # set a long random CUH_SECRET
docker compose up -d --build
```

Without Docker: `make build && ./cuh serve --data-dir ./data --secret S`.
History lives in `./data`. Back it up if you care about it.

**Each laptop**, with Claude Code logged in:

```sh
make install                                                   # ~/.local/bin/cuh
cuh --remote http://server:8787 --secret <the secret>          # the panel
```

And in `~/.claude/settings.json`, the same flags:

```json
{ "statusLine": { "type": "command", "command": "cuh -s --remote http://server:8787 --secret <the secret>" } }
```

There is no config file. The command carries its settings; `CUH_REMOTE` and
`CUH_SECRET` work too.

Open `http://server:8787/` for the dashboard. It asks for the secret once.
Keep the port on your LAN or tailnet; the secret is the only lock on it.

## When something is off, the status line says so

| Marker | Meaning |
|---|---|
| `⚠ server offline · last update 2h ago` | The server did not answer. The numbers shown are the last it gave. |
| `⚠ last update 40m ago` | The server answers but nobody has posted a sample for a while. Check this laptop's login: `cuh` shows the full error. |
| `⚠ sampling failed` | This machine just tried to sample and could not. Usually a login Claude Code has not refreshed yet, or Anthropic rate limiting. |

The dashboard shows the same as a banner. Cached numbers are always shown, never
silently.

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

Server: one line per sample in `data/history.jsonl` with timestamp, every
limit's percent and reset time, the weekly split by surface, and extra-usage
spend. About 250 bytes each, so a year of heavy use stays under 10 MB. Nothing
is deleted. After a restart the server has no current numbers until the next
laptop posts, which happens on the next status line redraw.

Client: one file, `~/.cache/cuh/cache.json`, with the server's last answer
(shown when the server is down) and the backoff after a failed fetch.

## Endpoints

| Endpoint | Returns |
|---|---|
| `GET /` | The dashboard. No auth; it asks for the secret itself. |
| `GET /now` | Current usage, plan, per-limit rates, verdict, headline, each limit's assessment. |
| `POST /sample` | Takes `{"usage": <Anthropic's response>, "plan": "...", "tier": "..."}`, stores it, answers like `/now`. |
| `GET /ping` | `{"ok":true,"secret_required":…}` once the secret is accepted. The dashboard uses it to test a secret. |

All but `/` require `Authorization: Bearer <secret>`.

## Rate limits

One request to Anthropic per 5 minutes across all your machines, since each
checks the server's clock before fetching. On HTTP 429 the client backs off
exponentially, honours `Retry-After`, and the server keeps serving the last
good numbers.

## Development

```sh
make build      # local binary
make test       # pace math, verdict thresholds, client sampling and backoff, server storage
make release    # all six platform binaries into dist/
```

Go 1.22 or newer. Dependencies are Lip Gloss for the terminal panel and
golang.org/x/term; the binary is static and needs no runtime.
