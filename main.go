package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"golang.org/x/term"
)

var version = "0.1.0"

const (
	defaultMaxAge  = 3 * time.Minute // serve cached numbers younger than this without a network call
	manualThrottle = 60 * time.Second
	baseBackoff    = 5 * time.Minute
	maxBackoff     = time.Hour
	watchInterval  = 3 * time.Minute
	watchRedraw    = time.Second
)

type options struct {
	short, watch, jsonOut, reset, offline, noColor, noBG, paths, showVersion bool
	maxAge, interval                                                         time.Duration
	remote, secret                                                           string
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "serve":
			runServe(os.Args[2:])
			return
		case "config":
			runConfig(os.Args[2:])
			return
		case "auth":
			runAuth(os.Args[2:])
			return
		}
	}
	cfg := loadConfig()
	var o options
	flag.StringVar(&o.remote, "remote", cfg.Remote, "read from a claude-usage server instead of Anthropic")
	flag.StringVar(&o.secret, "secret", cfg.Secret, "shared secret for --remote")
	flag.BoolVar(&o.short, "s", false, "one line, for status lines and prompts")
	flag.BoolVar(&o.short, "short", false, "one line, for status lines and prompts")
	flag.BoolVar(&o.watch, "watch", false, "live view that refreshes itself (q quits, r refreshes)")
	flag.BoolVar(&o.jsonOut, "json", false, "machine-readable output")
	flag.BoolVar(&o.reset, "reset", false, "delete cached state and history")
	flag.BoolVar(&o.offline, "offline", false, "never touch the network; use cached numbers")
	flag.BoolVar(&o.noColor, "no-color", false, "plain text")
	flag.BoolVar(&o.noBG, "no-bg", false, "do not paint the panel background")
	flag.BoolVar(&o.paths, "paths", false, "print where state and history live")
	flag.BoolVar(&o.showVersion, "version", false, "print version")
	flag.DurationVar(&o.maxAge, "max-age", defaultMaxAge, "reuse cached numbers younger than this")
	flag.DurationVar(&o.interval, "interval", watchInterval, "refresh interval in --watch mode")
	flag.Usage = usage
	flag.Parse()

	if o.showVersion {
		fmt.Println("claude-usage", version)
		return
	}
	store, err := openStore()
	if err != nil {
		fatal(err)
	}
	if o.paths {
		fmt.Println(store.statePath())
		fmt.Println(store.historyPath())
		fmt.Println(configPath())
		return
	}
	if o.reset {
		if err := store.Reset(); err != nil {
			fatal(err)
		}
		fmt.Println("cleared", store.Dir)
		return
	}
	if o.interval < time.Minute {
		o.interval = time.Minute
	}
	if o.watch {
		runWatch(store, o)
		return
	}
	runOnce(store, o)
}

func usage() {
	fmt.Fprintf(os.Stderr, `claude-usage %s - one verdict for your Claude subscription: use more, on track, slow down, or running out.

usage: claude-usage [flags]
       claude-usage serve  [--listen :8787 --data-dir ./data --interval 5m --secret S --credentials FILE]
       claude-usage config show | remote URL [--secret S] | clear
       claude-usage auth refresh [--force]

`, version)
	flag.PrintDefaults()
}

// runAuth implements `claude-usage auth refresh`.
func runAuth(args []string) {
	if len(args) == 0 || args[0] != "refresh" {
		fatal(errors.New("usage: claude-usage auth refresh [--force] [--credentials FILE]"))
	}
	fs := flag.NewFlagSet("auth refresh", flag.ExitOnError)
	force := fs.Bool("force", false, "refresh even if the token is not close to expiry")
	path := fs.String("credentials", credentialsPath(), "credentials file")
	fs.Parse(args[1:])
	c, did, err := refreshIfNeeded(*path, *force, time.Now())
	if err != nil {
		fatal(err)
	}
	exp := time.UnixMilli(c.ExpiresAt)
	if did {
		fmt.Printf("token refreshed, now valid until %s (%s)\n", exp.Local().Format("Mon 15:04"), fmtDur(time.Until(exp)))
	} else {
		fmt.Printf("token still valid until %s (%s), no refresh needed\n", exp.Local().Format("Mon 15:04"), fmtDur(time.Until(exp)))
	}
}

// noteFetchError records a failed fetch and decides when the next attempt may happen.
func noteFetchError(st *State, err error, now time.Time) {
	var fe *FetchError
	st.LastError, st.LastErrorAt = err.Error(), now
	if errors.As(err, &fe) && fe.Status == 429 {
		st.Backoff++
		wait := baseBackoff << (st.Backoff - 1)
		if wait > maxBackoff || st.Backoff > 8 {
			wait = maxBackoff
		}
		if fe.RetryAfter > wait {
			wait = fe.RetryAfter
		}
		st.NextAllowedAt = now.Add(wait)
		st.LastError = fmt.Sprintf("%s; next try in %s", fe.Msg, fmtDur(wait))
		return
	}
	st.NextAllowedAt = now.Add(2 * time.Minute)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "claude-usage:", err)
	os.Exit(1)
}

// refresh fetches if the cache is stale and the rate-limit backoff allows it.
// It always returns a usable state; problems are recorded in st.LastError.
func refresh(store *Store, st *State, maxAge time.Duration, offline, manual bool, now time.Time) (fetched bool, note string) {
	if offline {
		return false, ""
	}
	age := now.Sub(st.FetchedAt)
	if st.Usage != nil && age < maxAge && !manual {
		return false, ""
	}
	if manual && st.Usage != nil && age < manualThrottle {
		return false, fmt.Sprintf("refresh throttled, try again in %ds", int((manualThrottle - age).Seconds()))
	}
	if now.Before(st.NextAllowedAt) {
		if manual {
			return false, "holding off the API, next try in " + fmtDur(st.NextAllowedAt.Sub(now))
		}
		return false, ""
	}
	unlock, err := store.TryLock(now)
	if err != nil {
		return false, ""
	}
	defer unlock()

	creds, err := loadCreds()
	if err != nil {
		st.LastError, st.LastErrorAt = err.Error(), now
		st.NextAllowedAt = now.Add(time.Minute)
		store.SaveState(st)
		return false, ""
	}
	u, raw, err := fetchUsage(creds.AccessToken)
	if err != nil {
		noteFetchError(st, err, now)
		store.SaveState(st)
		return false, ""
	}
	st.Usage, st.Raw, st.FetchedAt = u, raw, now
	st.Plan, st.Tier = creds.SubscriptionType, creds.RateLimitTier
	st.Backoff, st.NextAllowedAt, st.LastError = 0, time.Time{}, ""
	store.AppendSample(u, now)
	store.CompactIfNeeded(st, now)
	store.SaveState(st)
	return true, ""
}

// acquire returns the state and rates to render. With a server configured the
// server is the only source: if it does not answer, the last answer it gave is
// shown as stale, never a direct fetch, so the view stays consistent.
func acquire(store *Store, o options, now time.Time, manual bool) (*State, Rates, string) {
	if o.remote != "" {
		res, err := fetchNow(o.remote, o.secret)
		if err == nil && res.Usage != nil {
			st := &State{FetchedAt: res.FetchedAt, Plan: res.Plan, Tier: res.Tier, Usage: res.Usage, Raw: res.Raw, LastError: res.Error, Via: hostOf(o.remote)}
			store.SaveRemote(remoteCache{Remote: o.remote, ReceivedAt: now, Response: *res})
			return st, fixedRates(res.Rates), ""
		}
		problem := "server did not answer"
		if err != nil {
			problem = "server unreachable: " + shortErr(err)
		} else if res.Error != "" {
			problem = "server has no data: " + res.Error
		}
		if c := store.LoadRemote(); c != nil && c.Remote == o.remote && c.Response.Usage != nil {
			r := c.Response
			st := &State{FetchedAt: r.FetchedAt, Plan: r.Plan, Tier: r.Tier, Usage: r.Usage, Raw: r.Raw, Via: hostOf(o.remote)}
			st.LastError = fmt.Sprintf("%s; showing numbers from %s", problem, fmtAgo(now.Sub(c.ReceivedAt)))
			return st, fixedRates(r.Rates), ""
		}
		return &State{LastError: problem, Via: hostOf(o.remote)}, fixedRates(nil), ""
	}
	st := store.LoadState()
	_, note := refresh(store, st, o.maxAge, o.offline, manual, now)
	return st, historyRates(store.LoadHistory()), note
}

func buildView(st *State, rates Rates, now time.Time, width int, note string) View {
	v := View{State: st, Now: now, Width: width, Note: note, Via: st.Via}
	v.Plan = Creds{SubscriptionType: st.Plan, RateLimitTier: st.Tier}.PlanLabel()
	if st.LastError != "" {
		v.Error = st.LastError
	}
	if st.Usage != nil {
		v.As = assessAll(st.Usage, rates, now)
		v.Binding = binding(v.As)
		v.Headline = headline(v.Binding, v.As)
	}
	return v
}

func termSize() (width, height int) {
	if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return w, h
	}
	return 80, 0
}

func runOnce(store *Store, o options) {
	now := time.Now()
	st, rates, note := acquire(store, o, now, false)
	if st.Usage == nil && (o.jsonOut || o.short || st.Via == "") {
		msg := st.LastError
		if msg == "" {
			msg = "no usage data yet"
		}
		fatal(errors.New(msg))
	}
	w, h := termSize()
	v := buildView(st, rates, now, w, note)
	v.PaintBG, v.Height = !o.noBG, h
	if st.Usage == nil { // server configured but never reached: show the panel with the problem
		fmt.Println(renderPanel(v))
		os.Exit(1)
	}
	setupColors(o.noColor)
	switch {
	case o.jsonOut:
		printJSON(v)
	case o.short:
		fmt.Println(renderShort(v))
	default:
		fmt.Println(renderPanel(v))
	}
}

// limitJSON is one assessed limit as exposed by --json and by the server's /now.
type limitJSON struct {
	Key        string     `json:"key"`
	Name       string     `json:"name"`
	Group      string     `json:"group"`
	Percent    float64    `json:"percent"`
	ResetsAt   *time.Time `json:"resets_at"`
	ResetIn    int        `json:"resets_in_seconds"`
	Elapsed    float64    `json:"elapsed_fraction"`
	Rate       float64    `json:"rate_percent_per_hour"`
	RateSource string     `json:"rate_source"`
	Projected  float64    `json:"projected_at_reset"`
	EmptyIn    int        `json:"empty_in_seconds"`
	Verdict    string     `json:"verdict"`
	VsUsual    float64    `json:"vs_usual"`
	Note       string     `json:"note,omitempty"`
}

func limitsJSON(as []Assessment) []limitJSON {
	var out []limitJSON
	for _, a := range as {
		out = append(out, limitJSON{
			Key: a.Key, Name: a.Name, Group: a.Group, Percent: a.Percent, ResetsAt: a.ResetsAt,
			ResetIn: int(a.ResetIn.Seconds()), Elapsed: a.Elapsed, Rate: a.Rate, RateSource: a.RateSource,
			Projected: a.Projected, EmptyIn: int(a.EmptyIn.Seconds()), Verdict: a.Verdict.Slug(), VsUsual: a.VsUsual, Note: a.Note,
		})
	}
	return out
}

func printJSON(v View) {
	out := struct {
		Verdict   string          `json:"verdict"`
		Headline  string          `json:"headline"`
		Plan      string          `json:"plan"`
		FetchedAt time.Time       `json:"fetched_at"`
		Error     string          `json:"error,omitempty"`
		Source    string          `json:"source"`
		Limits    []limitJSON     `json:"limits"`
		Spend     *Spend          `json:"spend,omitempty"`
		Raw       json.RawMessage `json:"raw,omitempty"`
	}{Headline: v.Headline, Plan: v.Plan, FetchedAt: v.State.FetchedAt, Error: v.Error, Source: "direct", Limits: limitsJSON(v.As), Spend: v.State.Usage.Spend, Raw: v.State.Raw}
	if v.Via != "" {
		out.Source = "server " + v.Via
	}
	out.Verdict = VUnknown.Slug()
	if v.Binding != nil {
		out.Verdict = v.Binding.Verdict.Slug()
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(out)
}

// runWatch keeps the summary on screen and refreshes it on a slow, polite cadence.
func runWatch(store *Store, o options) {
	setupColors(o.noColor)
	fd := int(os.Stdin.Fd())
	raw := term.IsTerminal(fd)
	var old *term.State
	if raw {
		enableVT()
		s, err := term.MakeRaw(fd)
		if err == nil {
			old = s
		}
	}
	fmt.Print("\x1b[?1049h\x1b[?25l")
	restore := func() {
		fmt.Print("\x1b[?25h\x1b[?1049l")
		if old != nil {
			term.Restore(fd, old)
		}
	}
	defer restore()

	keys := make(chan byte, 8)
	go func() {
		buf := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				return
			}
			if n == 1 {
				keys <- buf[0]
			}
		}
	}()

	var st *State
	var rates Rates
	note := ""
	noteUntil := time.Time{}
	poll := 30 * time.Second // how often to re-read the source; caching keeps this cheap
	var lastPoll time.Time
	pull := func(now time.Time, manual bool) {
		wo := o
		wo.maxAge = o.interval
		var n string
		st, rates, n = acquire(store, wo, now, manual)
		if n != "" {
			note, noteUntil = n, now.Add(6*time.Second)
		}
		lastPoll = now
	}
	draw := func(now time.Time) {
		if now.After(noteUntil) {
			note = ""
		}
		w, h := termSize()
		v := buildView(st, rates, now, w, note)
		v.Watch, v.PaintBG, v.Height = true, !o.noBG, h
		fmt.Print("\x1b[H\x1b[2J" + crlf(renderPanel(v)))
	}
	pull(time.Now(), false)
	draw(time.Now())
	tick := time.NewTicker(watchRedraw)
	defer tick.Stop()
	for {
		select {
		case k := <-keys:
			switch k {
			case 'q', 'Q', 27, 3:
				return
			case 'r', 'R':
				now := time.Now()
				pull(now, true)
				draw(now)
			}
		case now := <-tick.C:
			if now.Sub(lastPoll) >= poll {
				pull(now, false)
			}
			draw(now)
		}
	}
}

// crlf makes multi-line output render correctly while the terminal is in raw mode.
func crlf(s string) string {
	var b []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			b = append(b, '\r')
		}
		b = append(b, s[i])
	}
	return string(b)
}
