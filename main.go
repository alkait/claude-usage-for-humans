package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"
)

var version = "0.2.0"

const (
	sampleEvery = 5 * time.Minute  // a client samples when the server's numbers are older than this
	staleAfter  = 15 * time.Minute // and flags them once they are older than this
	baseBackoff = 5 * time.Minute
	maxBackoff  = time.Hour
)

type options struct {
	jsonOut, noColor, showVersion bool
	remote, secret                string
}

// State is what the renderers show: the server's numbers plus how we got them.
type State struct {
	FetchedAt time.Time
	Plan      string
	Tier      string
	Usage     *Usage
	Raw       json.RawMessage
	Error     string // current problem, if any; the numbers shown may be cached
	Offline   bool   // the server did not answer; numbers come from the local cache
	Via       string // server host
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "serve":
			runServe(os.Args[2:])
			return
		}
	}
	var o options
	flag.StringVar(&o.remote, "remote", os.Getenv("CUH_REMOTE"), "the cuh server, e.g. http://host:8787")
	flag.StringVar(&o.secret, "secret", os.Getenv("CUH_SECRET"), "its shared secret")
	flag.BoolVar(&o.jsonOut, "json", false, "machine-readable output")
	flag.BoolVar(&o.noColor, "no-color", false, "plain text")
	flag.BoolVar(&o.showVersion, "version", false, "print version")
	flag.Usage = usage
	flag.Parse()

	if o.showVersion {
		fmt.Println("cuh", version)
		return
	}
	if o.remote == "" {
		fatal(errors.New("no server given; pass --remote http://host:8787 --secret S"))
	}
	runOnce(o)
}

func usage() {
	fmt.Fprintf(os.Stderr, `cuh %s - one verdict for your Claude subscription: use more, on track, slow down, or running out.

usage: cuh --remote URL --secret S [--json]
       cuh serve [--listen :8787 --data-dir ./data --secret S]

flags can also come from CUH_REMOTE and CUH_SECRET.

`, version)
	flag.PrintDefaults()
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "cuh:", err)
	os.Exit(1)
}

// sample fetches usage with this machine's Claude Code login and hands it to
// the server. The login is only read; Claude Code keeps it fresh.
func sample(remote, secret string) (*NowResponse, error) {
	creds, err := loadCreds()
	if err != nil {
		return nil, err
	}
	_, raw, err := fetchUsage(creds.AccessToken)
	if err != nil {
		return nil, err
	}
	return postSample(remote, secret, raw, creds)
}

// noteError records a failed sample and decides when the next attempt may happen.
func noteError(c *cache, err error, now time.Time) {
	c.LastError = err.Error()
	c.NextTry = now.Add(time.Minute)
	var fe *FetchError
	if errors.As(err, &fe) && fe.Status == 429 {
		c.Backoff++
		wait := baseBackoff << (c.Backoff - 1)
		if wait > maxBackoff || c.Backoff > 8 {
			wait = maxBackoff
		}
		if fe.RetryAfter > wait {
			wait = fe.RetryAfter
		}
		c.NextTry = now.Add(wait)
		c.LastError = fmt.Sprintf("%s; next try in %s", fe.Msg, fmtDur(wait))
	}
}

// acquire asks the server for the current view, samples first if the server's
// numbers are due, and falls back to the cached answer when the server is down.
func acquire(remote, secret string, now time.Time) (*State, Rates) {
	c := loadCache()
	if c.Remote != remote {
		c = &cache{Remote: remote}
	}
	via := hostOf(remote)
	res, err := fetchNow(remote, secret)
	if err == nil {
		if now.Sub(res.FetchedAt) >= sampleEvery && !now.Before(c.NextTry) {
			if fresh, serr := sample(remote, secret); serr == nil {
				res = fresh
				c.Backoff, c.NextTry, c.LastError = 0, time.Time{}, ""
			} else {
				noteError(c, serr, now)
			}
		}
		c.ReceivedAt, c.Response = now, *res
		saveCache(c)
		st := stateOf(res, via)
		st.Error = c.LastError
		return st, fixedRates(res.Rates)
	}
	problem := "server unreachable: " + shortErr(err)
	if c.Response.Usage == nil {
		return &State{Error: problem, Offline: true, Via: via}, fixedRates(nil)
	}
	st := stateOf(&c.Response, via)
	st.Error = fmt.Sprintf("%s; showing numbers from %s", problem, fmtAgo(now.Sub(c.ReceivedAt)))
	st.Offline = true
	return st, fixedRates(c.Response.Rates)
}

func stateOf(res *NowResponse, via string) *State {
	return &State{FetchedAt: res.FetchedAt, Plan: res.Plan, Tier: res.Tier, Usage: res.Usage, Raw: res.Raw, Via: via}
}

func buildView(st *State, rates Rates, now time.Time) View {
	v := View{State: st, Now: now, Error: st.Error}
	if st.Usage != nil {
		v.As = assessAll(st.Usage, rates, now)
		v.Binding = binding(v.As)
		v.Headline = headline(v.Binding, v.As)
	}
	return v
}

// runOnce prints the status line, or the JSON view. Cached numbers are shown
// with a marker; with no numbers at all the exit code says so.
func runOnce(o options) {
	now := time.Now()
	st, rates := acquire(o.remote, o.secret, now)
	if st.Usage == nil && o.jsonOut {
		fatal(errors.New(st.Error))
	}
	v := buildView(st, rates, now)
	setupColors(o.noColor)
	if o.jsonOut {
		printJSON(v)
	} else {
		fmt.Println(renderLine(v))
	}
	if st.Usage == nil {
		os.Exit(1)
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
	}{Headline: v.Headline, Plan: Creds{SubscriptionType: v.State.Plan, RateLimitTier: v.State.Tier}.PlanLabel(), FetchedAt: v.State.FetchedAt, Error: v.Error, Source: "server " + v.State.Via, Limits: limitsJSON(v.As), Spend: v.State.Usage.Spend, Raw: v.State.Raw}
	out.Verdict = VUnknown.Slug()
	if v.Binding != nil {
		out.Verdict = v.Binding.Verdict.Slug()
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(out)
}
