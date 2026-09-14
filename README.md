# claude-usage

One verdict for your Claude subscription: **use more**, **on track**, **slow down**, or **running out**.

Claude's own usage screen tells you *how much* you have used. It does not tell you
whether that is a problem. 68% of the week gone with 2 days left is fine. 68% gone
with 5 days left is not. Doing that arithmetic across a 5-hour window nested in a
7-day window, per model, every time you wonder whether to send the next prompt, is
exactly the kind of thing nobody does. So people either hit the wall mid-task, or
they get cautious and leave a third of a paid plan unused every week.

`claude-usage` does the arithmetic and gives you the answer.

```
╭──────────────────────────────────────────────────────────────────────────────╮
│                                                                              │
│  ✱ Claude usage  ·  MAX 20X                                                  │
│  ──────────────────────────────────────────────────────────────────────────  │
│                                                                              │
│  ╭──────────────────────────────────────────────────────────────────────╮    │
│  │ 🟢  USE MORE                                                         │    │
│  │ Based on your usage, 33% of your Fable quota can go unused before    │    │
│  │ it resets in 3d 8h.                                                  │    │
│  ╰──────────────────────────────────────────────────────────────────────╯    │
│                                                                              │
│                                                                              │
│               Session                Weekly                Fable             │
│           time       quota      time       quota      time       quota       │
│  100%      ╌╌          ╌╌        ╌╌          ╌╌        ╌╌          ╌╌        │
│            ╌╌          ╌╌        ╌╌          ╌╌        ╌╌          ╌╌        │
│            ╌╌          ╌╌        ╌╌          ╌╌        ╌╌          ╌╌        │
│            ╌╌          ╌╌        ╌╌          ╌╌        ╌╌          ╍╍        │
│            ╌╌          ╌╌        ╌╌          ╌╌        ╌╌          ╍╍        │
│   50%      ╌╌          ╌╌        ▬▬          ╌╌        ▬▬          ╍╍        │
│            ╌╌          ╍╍        ▬▬          ╍╍        ▬▬          ▬▬        │
│            ▬▬          ╍╍        ▬▬          ╍╍        ▬▬          ▬▬        │
│            ▬▬          ╍╍        ▬▬          ▬▬        ▬▬          ▬▬        │
│    0%      ▬▬          ▬▬        ▬▬          ▬▬        ▬▬          ▬▬        │
│          1h39m        10%      day 4        20%      day 4        36%        │
│          of 5h        used      of 7        used      of 7        used       │
│            on course: 41%        on course: 37%        on course: 67%        │
│           resets at 23:00       resets Fri 02:00      resets Fri 02:00       │
│                                                                              │
│  ──────────────────────────────────────────────────────────────────────────  │
│  EXTRA USAGE                                          $0.00 of $20.00 · off  │
│  ──────────────────────────────────────────────────────────────────────────  │
│  ◷ updated just now · via pi.local:8787                                      │
│                                                                              │
╰──────────────────────────────────────────────────────────────────────────────╯
```

Each limit is a pair of vertical meters. **time** is how far through the
window you are. **quota** is how much of the limit you have used, in the
verdict colour, with a faint dashed extension (`╍╍`) up to where you land at
the reset if the pace holds. Quota lower than time means you have room; higher
means you are spending faster than time is passing. The caption says it in
words: "on course: 67%", or "runs out 2h 05m" when that is the case.

## The verdict

The verdict is driven by whichever window is tightest for *your* plan. On Max 20x
that is usually the weekly cap, on Pro it is usually the 5-hour session.

| Verdict | Meaning | What to do |
|---|---|---|
| 🟢 **USE MORE** | You will land under 85% at the reset. | Use the big model, run the refactor, parallelise. Unused quota is money gone. |
| 🔵 **ON TRACK** | You will land between 85% and 105%. | Keep going as you are. |
| 🟡 **SLOW DOWN** | You will run out shortly before the reset. | Smaller tasks, a cheaper model, `/clear` between tasks. |
| 🔴 **RUNNING OUT** | You will run out well before the reset. | Batch the rest and save it for what matters. |
| ⏳ **WAIT** | You have hit the limit. | Nothing to do until the reset. |
| ⚪ **NO PACE YET** | The window just started. | Check back in a few minutes. |

The rate behind the projection is your **recent** burn, not the window average,
so three idle days followed by a heavy hour do not read as "on track". Once the
tool has seen enough of your history it also tells you when the current burn is
unusual for you: "Burning 2.1× your usual rate."

## Install

Download the binary for your platform from the releases page, or build it:

```sh
make build     # needs Go 1.22+
make release   # every platform into dist/
make install   # copy to ~/.local/bin
```

No runtime, no Python, no Node. The only build-time dependencies are Lip Gloss and golang.org/x/term, compiled into the binary. It reads the login Claude Code already has, so
there is nothing to configure. On macOS the token is read from the Keychain; on
Linux and Windows from `~/.claude/.credentials.json`.

## Use

```sh
claude-usage             # the summary above
claude-usage -s          # one line, for status lines and prompts
claude-usage --watch     # live view, q quits, r refreshes
claude-usage --json      # everything, machine-readable
claude-usage serve       # the sampling server, see below
```

Put the one-liner in Claude Code's status line so the verdict is in front of you
at the exact moment you decide whether to send another prompt. In
`~/.claude/settings.json`:

```json
{ "statusLine": { "type": "command", "command": "claude-usage -s" } }
```

It also works in tmux, a shell prompt, or anything else that runs a command.

As a small always-on window with a matching dark background (Ghostty):

```sh
ghostty --class=claude-usage --window-width=100 --window-height=46 --font-size=14 \
  --background=1e1e2e --foreground=cdd6f4 -e claude-usage --watch
```

The panel paints its own background; `--no-bg` turns that off if you prefer your
terminal's own. Built on [Lip Gloss](https://github.com/charmbracelet/lipgloss).

## The server: history that follows you

Anthropic's usage endpoint reports only the present. To know your *pace*, and
whether today is unusual for you, something has to sample it over time. That is
the server: the same binary in `serve` mode, running on any always-on machine
in a container. Every 5 minutes it fetches, appends a sample to history, and
answers a tiny `/now` request with the current numbers plus the rates it has
measured. Laptops point at it and never talk to Anthropic themselves:

```sh
claude-usage config remote http://your-host:8787 --secret your-secret
```

The server is the only source once configured. If it does not answer, the
client shows the last numbers it received from it, marked stale with the
reason, rather than switching to a different view.

### Run it locally

```sh
cp .env.example .env     # set CLAUDE_USAGE_SECRET
make up                  # build the image and start the container
make logs                # watch it sample
make sample              # what /now answers
make down
```

`make serve` runs the same server directly, without a container, for quick
iteration. The server needs a Claude login: it reads Claude Code's credentials
file (mounted into the container) and refreshes the token itself before it
expires, writing the new token back in place. Refreshing rotates the refresh
token, so a credentials file must not be shared between two machines that
both refresh it.

### The web client

The server serves its own page at `/`: the verdict as the headline, one card
per limit with two concentric rings (time elapsed outside, quota used inside),
the projection in words under each ring, and a system/light/dark theme switch.
It refreshes every 30 seconds. Verdict colours are a status palette
validated for contrast and colour-vision separation on the dark surface. Open
`http://your-host:8787/` and enter the shared secret once; the browser
remembers it. All the logic (pace, verdict, headline) runs on the server, so
the page and the terminal client can never disagree.

### Endpoints

| Endpoint | Returns |
|---|---|
| `GET /` | The web client. No auth; it asks for the secret itself. |
| `GET /now` | Current usage, plan, per-limit rates, and the computed view: verdict, headline, and each limit's assessment. What every client uses. A few KB. |
| `GET /history?from=&to=` | Samples in a range (RFC3339 or `YYYY-MM-DD`), default the last 7 days. |
| `GET /history/YYYY-MM.jsonl` | One month of raw samples. |
| `GET /healthz` | `ok`, no auth. |

All but `/healthz` require `Authorization: Bearer <secret>` when a secret is set.

### What is stored

One line per sample in `history-YYYY-MM.jsonl`: timestamp, every limit's
percent and reset time, the weekly split by surface (Claude Code, chats,
Cowork), and extra-usage spend. About 250 bytes each, under 200 KB a month.
Nothing is ever deleted; ten years fits in a quarter of a gigabyte. The
server keeps the last 90 days in memory for rate computation and reads
older months from disk only when asked.

## Rate limits and data (direct mode)

Without a server, the client talks to Anthropic itself and is built to stay
well under the endpoint's rate limit:

- Numbers younger than 3 minutes are served from cache without a network call
  (`--max-age` changes this). A status line polling every few seconds costs at
  most 20 requests an hour.
- A lock prevents two instances from fetching at the same moment.
- On HTTP 429 it backs off exponentially (5 min, 10 min, ... up to 1 hour),
  honours `Retry-After`, and keeps showing the last good numbers. Nothing is
  retried until the wait is over, even across separate invocations.

The server follows the same backoff rules.

Client files (`claude-usage --paths` prints them):

| File | Purpose |
|---|---|
| `state.json` | Last good response, when it was fetched, backoff state. Rewritten atomically. |
| `history.jsonl` | Local fallback history. Capped at 30 days and about 1 MB. |
| `config.json` | Server address and secret, if any. |
| `remote.json` | Last answer from the server, shown as stale if the server stops answering. |

`claude-usage --reset` deletes the first two. The token is used only in request
headers and is never written anywhere except back into Claude Code's own
credentials file after a refresh.

## Same numbers as Anthropic

The tool reads the same endpoint the `/usage` command in Claude Code and the
claude.ai usage page use. It does not estimate from local logs, so the percentages
never drift from what the cap actually enforces.
