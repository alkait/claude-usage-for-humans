package main

import (
	"bufio"
	"encoding/json"
	"os"
	"time"
)

// Sample is one line of a history file.
type Sample struct {
	T time.Time              `json:"t"`
	L map[string]SamplePoint `json:"l"`
	S map[string]float64     `json:"s,omitempty"` // weekly share by surface: claude_code, chat, cowork, other
	X *SampleSpend           `json:"x,omitempty"` // extra usage, minor units
}

// SamplePoint is a limit's percent and reset time at sample time.
type SamplePoint struct {
	P float64    `json:"p"`
	R *time.Time `json:"r,omitempty"`
}

// SampleSpend records extra-usage spend at sample time.
type SampleSpend struct {
	Used  int64 `json:"used"`
	Limit int64 `json:"limit"`
}

// newSample turns a usage response into one history line.
func newSample(u *Usage, at time.Time) Sample {
	smp := Sample{T: at.UTC().Truncate(time.Second), L: map[string]SamplePoint{}}
	for _, l := range u.Limits {
		smp.L[l.Key()] = SamplePoint{P: l.Percent, R: l.ResetsAt}
	}
	if b := u.SevenDayBreakdown; b != nil && len(b.Rows) > 0 {
		smp.S = map[string]float64{}
		for _, r := range b.Rows {
			smp.S[r.Key] = r.Percent
		}
	}
	if sp := u.Spend; sp != nil && sp.Limit.AmountMinor > 0 {
		smp.X = &SampleSpend{Used: sp.Used.AmountMinor, Limit: sp.Limit.AmountMinor}
	}
	return smp
}

// readSamples parses one history file, skipping malformed lines.
func readSamples(path string) ([]Sample, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Sample
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var smp Sample
		if json.Unmarshal(sc.Bytes(), &smp) == nil && !smp.T.IsZero() {
			out = append(out, smp)
		}
	}
	return out, sc.Err()
}
