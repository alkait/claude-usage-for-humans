package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	sessionWindow = 5 * time.Hour
	weeklyWindow  = 7 * 24 * time.Hour
	minRecentSpan = 10 * time.Minute // shortest span we trust for a recent-rate estimate
	minTypicalN   = 12               // intervals needed before "vs usual" is shown
)

// Verdict is the single word the tool exists to produce.
type Verdict int

const (
	VUnknown Verdict = iota // no window to judge
	VNoPace                 // window just started, nothing to measure yet
	VPush                   // USE MORE
	VHold                   // ON TRACK
	VEase                   // SLOW DOWN
	VStop                   // RUNNING OUT
	VWait                   // already at 100%
)

func (v Verdict) Word() string {
	switch v {
	case VPush:
		return "USE MORE"
	case VHold:
		return "ON TRACK"
	case VEase:
		return "SLOW DOWN"
	case VStop:
		return "RUNNING OUT"
	case VWait:
		return "WAIT"
	case VNoPace:
		return "NO PACE YET"
	}
	return "NO DATA"
}

func (v Verdict) Glyph() string {
	switch v {
	case VPush:
		return "🟢"
	case VHold:
		return "🔵"
	case VEase:
		return "🟡"
	case VStop:
		return "🔴"
	case VWait:
		return "⏳"
	case VNoPace:
		return "⚪"
	}
	return "❓"
}

func (v Verdict) Slug() string {
	switch v {
	case VPush:
		return "use_more"
	case VHold:
		return "on_track"
	case VEase:
		return "slow_down"
	case VStop:
		return "running_out"
	case VWait:
		return "wait"
	case VNoPace:
		return "no_pace_yet"
	}
	return "unknown"
}

// Assessment is one limit turned into something decision-shaped.
type Assessment struct {
	Key        string
	Name       string
	Group      string
	Percent    float64
	IsActive   bool
	ResetsAt   *time.Time
	Window     time.Duration
	ResetIn    time.Duration
	Elapsed    float64       // fraction of the window that has passed; -1 if unknown
	Rate       float64       // percent per hour used for the projection
	RateSource string        // "recent", "average", or ""
	Projected  float64       // percent at reset if the rate holds; -1 if unknown
	EmptyIn    time.Duration // time until 100% at the current rate; 0 if it will not happen before reset
	Verdict    Verdict
	Note       string
	VsUsual    float64 // current rate divided by your typical rate; 0 if unknown
}

func windowFor(group string) time.Duration {
	switch group {
	case "session":
		return sessionWindow
	case "weekly":
		return weeklyWindow
	}
	return 0
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// Rates supplies the two measured rates a verdict depends on. Locally they come
// from the history file; with a server they arrive precomputed in /now.
type Rates interface {
	Recent(key string, resetsAt time.Time, horizon time.Duration, now time.Time, cur float64) (float64, bool)
	Typical(key string) (float64, bool)
}

// historyRates computes rates from samples.
type historyRates []Sample

func (h historyRates) Recent(key string, resetsAt time.Time, horizon time.Duration, now time.Time, cur float64) (float64, bool) {
	return recentRate([]Sample(h), key, resetsAt, horizon, now, cur)
}

func (h historyRates) Typical(key string) (float64, bool) { return typicalRate([]Sample(h), key) }

// RateInfo is one limit's rates as served by /now.
type RateInfo struct {
	Recent    float64 `json:"recent"`
	RecentOK  bool    `json:"recent_ok"`
	Typical   float64 `json:"typical"`
	TypicalOK bool    `json:"typical_ok"`
}

// fixedRates are rates a server already computed.
type fixedRates map[string]RateInfo

func (f fixedRates) Recent(key string, _ time.Time, _ time.Duration, _ time.Time, _ float64) (float64, bool) {
	r := f[key]
	return r.Recent, r.RecentOK
}

func (f fixedRates) Typical(key string) (float64, bool) {
	r := f[key]
	return r.Typical, r.TypicalOK
}

func assessAll(u *Usage, r Rates, now time.Time) []Assessment {
	if r == nil {
		r = historyRates(nil)
	}
	var out []Assessment
	for _, l := range u.Limits {
		out = append(out, assess(l, r, now))
	}
	return out
}

func assess(l Limit, r Rates, now time.Time) Assessment {
	if r == nil {
		r = historyRates(nil)
	}
	a := Assessment{
		Key: l.Key(), Name: l.Name(), Group: l.Group, Percent: l.Percent, IsActive: l.IsActive,
		ResetsAt: l.ResetsAt, Window: windowFor(l.Group), Elapsed: -1, Projected: -1,
	}
	if a.ResetsAt == nil || a.Window == 0 {
		a.Verdict = VUnknown
		a.Note = "no window"
		if l.Percent >= 100 {
			a.Verdict = VWait
		}
		return a
	}
	a.ResetIn = a.ResetsAt.Sub(now)
	if a.ResetIn < 0 {
		a.ResetIn = 0
	}
	if a.ResetIn > a.Window {
		a.ResetIn = a.Window
	}
	elapsed := a.Window - a.ResetIn
	a.Elapsed = float64(elapsed) / float64(a.Window)

	horizon := a.Window / 8 // 37 min for a session, 21 h for the week
	if rr, ok := r.Recent(a.Key, *a.ResetsAt, horizon, now, a.Percent); ok {
		a.Rate, a.RateSource = rr, "recent"
	} else if elapsed >= a.Window/20 {
		a.Rate, a.RateSource = a.Percent/elapsed.Hours(), "average"
	} else if typ, ok := r.Typical(a.Key); ok && typ > 0 {
		// a fresh window: assume your usual pace until it has evidence of its own
		a.Rate, a.RateSource = typ, "usual"
	}

	if a.Percent >= 100 {
		a.Verdict = VWait
		a.Projected = a.Percent
		a.Note = "exhausted"
		return a
	}
	if a.RateSource == "" {
		a.Verdict = VNoPace
		a.Note = "too early to tell"
		return a
	}
	a.Projected = a.Percent + a.Rate*a.ResetIn.Hours()
	if a.Rate > 0 {
		empty := time.Duration((100 - a.Percent) / a.Rate * float64(time.Hour))
		if empty < a.ResetIn {
			a.EmptyIn = empty
		}
	}
	switch {
	case a.Projected >= 125:
		a.Verdict = VStop
	case a.Projected >= 105:
		a.Verdict = VEase
	case a.Projected >= 85:
		a.Verdict = VHold
	default:
		a.Verdict = VPush
	}
	if a.RateSource == "recent" {
		if typ, ok := r.Typical(a.Key); ok && typ > 0 {
			a.VsUsual = a.Rate / typ
		}
	}
	return a
}

// recentRate measures percent per hour over the last `horizon` inside the current window.
func recentRate(hist []Sample, key string, resetsAt time.Time, horizon time.Duration, now time.Time, cur float64) (float64, bool) {
	type pt struct {
		t time.Time
		p float64
	}
	var pts []pt
	since := now.Add(-horizon)
	for _, s := range hist {
		sp, ok := s.L[key]
		if !ok || sp.R == nil || absDur(sp.R.Sub(resetsAt)) > 5*time.Second || s.T.Before(since) || s.T.After(now) {
			continue
		}
		pts = append(pts, pt{s.T, sp.P})
	}
	pts = append(pts, pt{now, cur})
	if len(pts) < 2 {
		return 0, false
	}
	first, last := pts[0], pts[len(pts)-1]
	span := last.t.Sub(first.t)
	// A short span extrapolated over a long window is noise: one 1% step over 90
	// minutes would project the whole week. Demand a quarter of the horizon.
	need := horizon / 4
	if need < minRecentSpan {
		need = minRecentSpan
	}
	if span < need {
		return 0, false
	}
	dp := last.p - first.p
	if dp < 0 {
		dp = 0
	}
	return dp / span.Hours(), true
}

// typicalRate is the median active burn rate seen in history for this limit.
func typicalRate(hist []Sample, key string) (float64, bool) {
	var rates []float64
	for i := 1; i < len(hist); i++ {
		pa, oka := hist[i-1].L[key]
		pb, okb := hist[i].L[key]
		if !oka || !okb || pa.R == nil || pb.R == nil || absDur(pa.R.Sub(*pb.R)) > 5*time.Second {
			continue
		}
		dt := hist[i].T.Sub(hist[i-1].T)
		if dt < 2*time.Minute || dt > 90*time.Minute {
			continue
		}
		if dp := pb.P - pa.P; dp > 0 {
			rates = append(rates, dp/dt.Hours())
		}
	}
	if len(rates) < minTypicalN {
		return 0, false
	}
	sort.Float64s(rates)
	return rates[len(rates)/2], true
}

// binding picks the limit that should drive the verdict.
func binding(as []Assessment) *Assessment {
	var best *Assessment
	for i := range as {
		a := &as[i]
		if a.Verdict == VUnknown {
			continue
		}
		if best == nil || a.Verdict > best.Verdict || (a.Verdict == best.Verdict && a.Projected > best.Projected) {
			best = a
		}
	}
	return best
}

// headline is the one sentence under the verdict. It states the consequence
// of the current pace, not the projection number.
func headline(b *Assessment, as []Assessment) string {
	if b == nil {
		return "Could not read your usage."
	}
	quota, limit, window := "your session quota", "your session limit", "session"
	switch {
	case strings.HasPrefix(b.Key, "model:"):
		quota, limit, window = "your "+b.Name+" quota", "your "+b.Name+" weekly limit", "week"
	case b.Group == "weekly":
		quota, limit, window = "your weekly quota", "your weekly limit", "week"
	}
	var s string
	switch {
	case b.Verdict == VWait:
		s = fmt.Sprintf("You have hit %s. It resets in %s.", limit, fmtDur(b.ResetIn))
	case b.Verdict == VNoPace:
		s = fmt.Sprintf("The %s just started. Not enough usage yet to judge the pace.", window)
	case b.RateSource == "usual" && b.Verdict == VPush:
		s = fmt.Sprintf("At your usual pace, %d%% of %s can go unused before it resets in %s.", int(100-b.Projected+0.5), quota, fmtDur(b.ResetIn))
	case b.RateSource == "usual" && b.Verdict == VHold:
		s = fmt.Sprintf("At your usual pace you use %s without hitting the limit. It resets in %s.", quota, fmtDur(b.ResetIn))
	case b.RateSource == "usual" && b.Verdict == VEase:
		s = fmt.Sprintf("At your usual pace you might hit %s in %s, before it resets in %s.", limit, fmtDur(b.EmptyIn), fmtDur(b.ResetIn))
	case b.RateSource == "usual" && b.Verdict == VStop:
		s = fmt.Sprintf("At your usual pace you hit %s in %s and are locked out for %s until it resets.", limit, fmtDur(b.EmptyIn), fmtDur(b.ResetIn-b.EmptyIn))
	case b.Verdict == VStop:
		s = fmt.Sprintf("You will hit %s in %s and be locked out for %s until it resets.", limit, fmtDur(b.EmptyIn), fmtDur(b.ResetIn-b.EmptyIn))
	case b.Verdict == VEase:
		s = fmt.Sprintf("You are moving a little too fast and might hit %s in %s, before it resets in %s.", limit, fmtDur(b.EmptyIn), fmtDur(b.ResetIn))
	case b.Verdict == VHold:
		s = fmt.Sprintf("You are at the right pace to use %s without hitting the limit. It resets in %s.", quota, fmtDur(b.ResetIn))
	default:
		s = fmt.Sprintf("Based on your usage, %d%% of %s can go unused before it resets in %s.", int(100-b.Projected+0.5), quota, fmtDur(b.ResetIn))
	}
	if b.VsUsual >= 1.5 {
		s += fmt.Sprintf(" That is %.1f× your usual pace.", b.VsUsual)
	} else if b.VsUsual > 0 && b.VsUsual <= 0.5 && b.Rate > 0 {
		s += " That is slower than usual for you."
	}
	return s
}

// shortReason is the compact form of the headline for one-line output:
// which limit the verdict is about and the one number that justifies it.
func shortReason(b *Assessment) string {
	if b == nil {
		return "no data"
	}
	which := "session"
	switch {
	case strings.HasPrefix(b.Key, "model:"):
		which = b.Name
	case b.Group == "weekly":
		which = "weekly"
	}
	switch b.Verdict {
	case VWait:
		return fmt.Sprintf("%s · resets in %s", which, fmtDur(b.ResetIn))
	case VNoPace:
		return which + " · just started"
	case VStop:
		return fmt.Sprintf("%s · limit in %s, locked out %s", which, fmtDur(b.EmptyIn), fmtDur(b.ResetIn-b.EmptyIn))
	case VEase:
		return fmt.Sprintf("%s · limit in %s, resets in %s", which, fmtDur(b.EmptyIn), fmtDur(b.ResetIn))
	case VHold:
		return fmt.Sprintf("%s · resets in %s", which, fmtDur(b.ResetIn))
	case VPush:
		return fmt.Sprintf("%s · %d%% may go unused", which, int(100-b.Projected+0.5))
	}
	return which
}

// fmtDur renders durations the way a person would say them.
func fmtDur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Minute)
	days := int(d.Hours()) / 24
	h := int(d.Hours()) % 24
	m := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, h)
	case h > 0:
		return fmt.Sprintf("%dh %02dm", h, m)
	default:
		return fmt.Sprintf("%dm", m)
	}
}

// fmtAgo renders "just now", "2m ago", "1h 05m ago".
func fmtAgo(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	return fmtDur(d) + " ago"
}
