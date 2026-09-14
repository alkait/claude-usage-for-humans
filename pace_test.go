package main

import (
	"testing"
	"time"
)

func ts(base time.Time, minutesAgo int) time.Time {
	return base.Add(-time.Duration(minutesAgo) * time.Minute)
}

func sessionLimit(percent float64, resets time.Time) Limit {
	return Limit{Kind: "session", Group: "session", Percent: percent, ResetsAt: &resets}
}

func TestVerdictThresholdsFromAverageRate(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		percent float64
		resetIn time.Duration
		want    Verdict
	}{
		{10, 4 * time.Hour, VPush},  // 1h in, 10% used -> lands at 50%
		{16, 4 * time.Hour, VPush},  // lands at 80%: a fifth unused is worth acting on
		{18, 4 * time.Hour, VHold},  // lands at 90%
		{20, 4 * time.Hour, VHold},  // lands at 100%
		{22, 4 * time.Hour, VEase},  // lands at 110%
		{30, 4 * time.Hour, VStop},  // lands at 150%
		{100, 1 * time.Hour, VWait}, // exhausted
	}
	for _, c := range cases {
		a := assess(sessionLimit(c.percent, now.Add(c.resetIn)), historyRates(nil), now)
		if a.Verdict != c.want {
			t.Errorf("percent=%v resetIn=%v: got %s (projected %.0f), want %s", c.percent, c.resetIn, a.Verdict.Word(), a.Projected, c.want.Word())
		}
	}
}

func TestTooEarlyWithoutHistory(t *testing.T) {
	now := time.Now()
	a := assess(sessionLimit(2, now.Add(sessionWindow-5*time.Minute)), historyRates(nil), now)
	if a.Verdict != VNoPace || a.Note != "too early to tell" {
		t.Fatalf("got %s / %q", a.Verdict.Word(), a.Note)
	}
}

func TestRecentRateBeatsWindowAverage(t *testing.T) {
	now := time.Now()
	resets := now.Add(3 * time.Hour) // 2h into the session
	// idle for most of the window, then 15% in the last 30 minutes
	hist := []Sample{
		{T: ts(now, 30), L: map[string]SamplePoint{"session": {P: 10, R: &resets}}},
		{T: ts(now, 15), L: map[string]SamplePoint{"session": {P: 17, R: &resets}}},
	}
	a := assess(sessionLimit(25, resets), historyRates(hist), now)
	if a.RateSource != "recent" {
		t.Fatalf("rate source = %q", a.RateSource)
	}
	if a.Rate < 29 || a.Rate > 31 { // 15% over 0.5h
		t.Fatalf("rate = %.1f, want ~30", a.Rate)
	}
	// 25% + 30%/h * 3h = 115% -> ease off; the window average (12.5%/h) would have said push
	if a.Verdict != VEase || a.EmptyIn == 0 || a.Projected < 114 || a.Projected > 116 {
		t.Fatalf("verdict %s, projected %.0f, emptyIn %v", a.Verdict.Word(), a.Projected, a.EmptyIn)
	}
}

func TestSamplesFromOtherWindowsAreIgnored(t *testing.T) {
	now := time.Now()
	resets := now.Add(4 * time.Hour)
	old := now.Add(-6 * time.Hour)
	hist := []Sample{{T: ts(now, 20), L: map[string]SamplePoint{"session": {P: 90, R: &old}}}}
	a := assess(sessionLimit(10, resets), historyRates(hist), now)
	if a.RateSource != "average" {
		t.Fatalf("stale window leaked into recent rate: %q", a.RateSource)
	}
}

func TestWeeklyRecentRateNeedsHoursOfData(t *testing.T) {
	now := time.Now()
	resets := now.Add(4 * 24 * time.Hour)
	l := Limit{Kind: "weekly_all", Group: "weekly", Percent: 29, ResetsAt: &resets}
	short := []Sample{{T: ts(now, 90), L: map[string]SamplePoint{"weekly_all": {P: 28, R: &resets}}}}
	if a := assess(l, historyRates(short), now); a.RateSource != "average" {
		t.Fatalf("90 minutes must not project a week: source=%q projected=%.0f", a.RateSource, a.Projected)
	}
	long := []Sample{{T: ts(now, 6*60), L: map[string]SamplePoint{"weekly_all": {P: 26, R: &resets}}}}
	if a := assess(l, historyRates(long), now); a.RateSource != "recent" {
		t.Fatalf("6 hours of data should be enough: source=%q", a.RateSource)
	}
}

func TestBindingPicksMostSevere(t *testing.T) {
	as := []Assessment{{Key: "session", Verdict: VPush, Projected: 40}, {Key: "weekly_all", Verdict: VEase, Projected: 110}, {Key: "model:fable", Verdict: VPush, Projected: 60}}
	if b := binding(as); b == nil || b.Key != "weekly_all" {
		t.Fatalf("binding = %+v", b)
	}
	// a window with no pace yet never outranks one with a real verdict
	as = []Assessment{{Key: "session", Verdict: VNoPace}, {Key: "weekly_all", Verdict: VPush, Projected: 40}}
	if b := binding(as); b == nil || b.Key != "weekly_all" {
		t.Fatalf("binding = %+v", b)
	}
	// an exhausted window outranks everything
	as = []Assessment{{Key: "session", Verdict: VWait, Projected: 100}, {Key: "weekly_all", Verdict: VStop, Projected: 150}}
	if b := binding(as); b == nil || b.Key != "session" {
		t.Fatalf("binding = %+v", b)
	}
}

func TestFmtDur(t *testing.T) {
	for d, want := range map[time.Duration]string{45 * time.Second: "1m", 125 * time.Minute: "2h 05m", 52 * time.Hour: "2d 4h"} {
		if got := fmtDur(d); got != want {
			t.Errorf("fmtDur(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestHeadlineWording(t *testing.T) {
	b := &Assessment{Key: "model:fable", Name: "Fable", Group: "weekly", Percent: 29, Projected: 69, Verdict: VPush, ResetIn: 97 * time.Hour}
	if got := headline(b, nil); got != "Based on your usage, 31% of your Fable quota can go unused before it resets in 4d 1h." {
		t.Fatalf("push: %q", got)
	}
	b = &Assessment{Key: "weekly_all", Name: "Weekly", Group: "weekly", Percent: 60, Projected: 98, Verdict: VHold, ResetIn: 52 * time.Hour}
	if got := headline(b, nil); got != "You are at the right pace to use your weekly quota without hitting the limit. It resets in 2d 4h." {
		t.Fatalf("on track: %q", got)
	}
	b = &Assessment{Key: "session", Name: "Session", Group: "session", Percent: 34, Projected: 160, Verdict: VEase, ResetIn: 253 * time.Minute, EmptyIn: 125 * time.Minute}
	if got := headline(b, nil); got != "You are moving a little too fast and might hit your session limit in 2h 05m, before it resets in 4h 13m." {
		t.Fatalf("slow down: %q", got)
	}
	b = &Assessment{Key: "session", Name: "Session", Group: "session", Percent: 40, Projected: 180, Verdict: VStop, ResetIn: 253 * time.Minute, EmptyIn: 70 * time.Minute}
	if got := headline(b, nil); got != "You will hit your session limit in 1h 10m and be locked out for 3h 03m until it resets." {
		t.Fatalf("running out: %q", got)
	}
	b = &Assessment{Key: "weekly_all", Name: "Weekly", Group: "weekly", Percent: 100, Projected: 100, Verdict: VWait, ResetIn: 2 * time.Hour}
	if got := headline(b, nil); got != "You have hit your weekly limit. It resets in 2h 00m." {
		t.Fatalf("wait: %q", got)
	}
	b = &Assessment{Key: "session", Name: "Session", Group: "session", Percent: 2, Projected: -1, Verdict: VNoPace, Note: "too early to tell"}
	if got := headline(b, nil); got != "The session just started. Not enough usage yet to judge the pace." {
		t.Fatalf("no pace: %q", got)
	}
	if got := headline(nil, nil); got != "Could not read your usage." {
		t.Fatalf("no data: %q", got)
	}
	b = &Assessment{Key: "session", Name: "Session", Group: "session", Percent: 34, Projected: 110, Verdict: VEase, Rate: 20, VsUsual: 2.1, ResetIn: 253 * time.Minute, EmptyIn: 125 * time.Minute}
	if got := headline(b, nil); got != "You are moving a little too fast and might hit your session limit in 2h 05m, before it resets in 4h 13m. That is 2.1× your usual pace." {
		t.Fatalf("add-on: %q", got)
	}
}

func TestShortReason(t *testing.T) {
	cases := map[string]*Assessment{
		"Fable · 31% may go unused":                    {Key: "model:fable", Name: "Fable", Group: "weekly", Verdict: VPush, Projected: 69},
		"weekly · resets in 2d 4h":                     {Key: "weekly_all", Group: "weekly", Verdict: VHold, ResetIn: 52 * time.Hour},
		"session · limit in 2h 05m, resets in 4h 13m":  {Key: "session", Group: "session", Verdict: VEase, EmptyIn: 125 * time.Minute, ResetIn: 253 * time.Minute},
		"session · limit in 1h 10m, locked out 3h 03m": {Key: "session", Group: "session", Verdict: VStop, EmptyIn: 70 * time.Minute, ResetIn: 253 * time.Minute},
		"session · resets in 2h 05m":                   {Key: "session", Group: "session", Verdict: VWait, ResetIn: 125 * time.Minute},
		"no data":                                      nil,
	}
	for want, a := range cases {
		if got := shortReason(a); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

type usualOnly float64

func (u usualOnly) Recent(string, time.Time, time.Duration, time.Time, float64) (float64, bool) {
	return 0, false
}
func (u usualOnly) Typical(string) (float64, bool) { return float64(u), true }

func TestFreshWindowAssumesUsualPace(t *testing.T) {
	now := time.Now()
	l := sessionLimit(0, now.Add(sessionWindow-7*time.Minute)) // 7 minutes in, nothing used
	a := assess(l, usualOnly(12), now)                         // 12%/h is this person's usual burn
	if a.RateSource != "usual" || a.Verdict != VPush || a.Projected < 55 || a.Projected > 62 {
		t.Fatalf("source=%q verdict=%s projected=%.0f", a.RateSource, a.Verdict.Word(), a.Projected)
	}
	if got := headline(&a, nil); got != "At your usual pace, 41% of your session quota can go unused before it resets in 4h 53m." {
		t.Fatalf("headline: %q", got)
	}
	// without any history the fresh window still says so
	if a := assess(l, historyRates(nil), now); a.Verdict != VNoPace {
		t.Fatalf("no history should mean no pace yet, got %s", a.Verdict.Word())
	}
}
