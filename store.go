package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	historyKeep    = 30 * 24 * time.Hour // drop samples older than this
	historyMaxSize = 1 << 20             // thin the file when it passes 1 MB
	thinAfter      = 48 * time.Hour      // samples older than this are thinned to one per thinStep
	thinStep       = 15 * time.Minute
	lockStale      = 30 * time.Second
)

// State is the small JSON document that survives between invocations.
type State struct {
	FetchedAt     time.Time       `json:"fetched_at"`
	Plan          string          `json:"plan"`
	Tier          string          `json:"tier"`
	Usage         *Usage          `json:"usage,omitempty"`
	Raw           json.RawMessage `json:"raw,omitempty"`
	NextAllowedAt time.Time       `json:"next_allowed_at"`
	Backoff       int             `json:"backoff"`
	LastError     string          `json:"last_error,omitempty"`
	LastErrorAt   time.Time       `json:"last_error_at"`
	LastCompact   time.Time       `json:"last_compact"`
	Via           string          `json:"-"` // server host when the numbers came from one
}

// Sample is one line of history.jsonl.
type Sample struct {
	T time.Time              `json:"t"`
	L map[string]SamplePoint `json:"l"`
	S map[string]float64     `json:"s,omitempty"` // weekly share by surface: claude_code, chat, cowork, other
	X *SampleSpend           `json:"x,omitempty"` // extra usage, minor units
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

// SamplePoint is a limit's percent and reset time at sample time.
type SamplePoint struct {
	P float64    `json:"p"`
	R *time.Time `json:"r,omitempty"`
}

// Store knows where the files live.
type Store struct {
	Dir string
}

func openStore() (*Store, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return nil, err
		}
		base = filepath.Join(home, ".cache")
	}
	dir := filepath.Join(base, "claude-usage")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{Dir: dir}, nil
}

func (s *Store) statePath() string   { return filepath.Join(s.Dir, "state.json") }
func (s *Store) historyPath() string { return filepath.Join(s.Dir, "history.jsonl") }
func (s *Store) lockPath() string    { return filepath.Join(s.Dir, "fetch.lock") }
func (s *Store) remotePath() string  { return filepath.Join(s.Dir, "remote.json") }

// remoteCache is the last answer a server gave, kept so an outage shows stale
// numbers from the same source rather than a different view.
type remoteCache struct {
	Remote     string      `json:"remote"`
	ReceivedAt time.Time   `json:"received_at"`
	Response   NowResponse `json:"response"`
}

func (s *Store) SaveRemote(c remoteCache) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return atomicWrite(s.remotePath(), raw)
}

func (s *Store) LoadRemote() *remoteCache {
	raw, err := os.ReadFile(s.remotePath())
	if err != nil {
		return nil
	}
	var c remoteCache
	if json.Unmarshal(raw, &c) != nil {
		return nil
	}
	return &c
}

// LoadState returns the persisted state, or an empty one.
func (s *Store) LoadState() *State {
	st := &State{}
	raw, err := os.ReadFile(s.statePath())
	if err != nil {
		return st
	}
	if json.Unmarshal(raw, st) != nil {
		return &State{}
	}
	return st
}

// SaveState writes atomically: temp file, then rename.
func (s *Store) SaveState(st *State) error {
	raw, err := json.MarshalIndent(st, "", " ")
	if err != nil {
		return err
	}
	return atomicWrite(s.statePath(), raw)
}

func atomicWrite(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}

// AppendSample records one fetch. Appends are small enough to be atomic.
func (s *Store) AppendSample(u *Usage, at time.Time) error {
	line, err := json.Marshal(newSample(u, at))
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.historyPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// LoadHistory parses history.jsonl, skipping malformed lines, oldest first.
func (s *Store) LoadHistory() []Sample {
	f, err := os.Open(s.historyPath())
	if err != nil {
		return nil
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
	sort.SliceStable(out, func(i, j int) bool { return out[i].T.Before(out[j].T) })
	return out
}

// CompactIfNeeded enforces the time cap and the size cap on history.jsonl.
func (s *Store) CompactIfNeeded(st *State, now time.Time) {
	info, err := os.Stat(s.historyPath())
	if err != nil {
		return
	}
	if info.Size() < historyMaxSize && now.Sub(st.LastCompact) < 24*time.Hour {
		return
	}
	hist := s.LoadHistory()
	var kept []Sample
	cutoff := now.Add(-historyKeep)
	for _, smp := range hist {
		if smp.T.After(cutoff) {
			kept = append(kept, smp)
		}
	}
	if info.Size() >= historyMaxSize {
		kept = thin(kept, now)
	}
	var buf []byte
	for _, smp := range kept {
		line, err := json.Marshal(smp)
		if err == nil {
			buf = append(buf, line...)
			buf = append(buf, '\n')
		}
	}
	if atomicWrite(s.historyPath(), buf) == nil {
		st.LastCompact = now
	}
}

// thin keeps every sample from the last 48 hours and one per 15 minutes before that.
func thin(hist []Sample, now time.Time) []Sample {
	edge := now.Add(-thinAfter)
	var out []Sample
	var lastKept time.Time
	for _, smp := range hist {
		if smp.T.After(edge) || lastKept.IsZero() || smp.T.Sub(lastKept) >= thinStep {
			out = append(out, smp)
			lastKept = smp.T
		}
	}
	return out
}

// TryLock prevents two processes from fetching at the same moment.
// Returns an unlock function, or an error if another fetch is in flight.
func (s *Store) TryLock(now time.Time) (func(), error) {
	path := s.lockPath()
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			return func() { os.Remove(path) }, nil
		}
		info, serr := os.Stat(path)
		if serr != nil || now.Sub(info.ModTime()) > lockStale {
			os.Remove(path) // stale lock from a crashed run
			continue
		}
		break
	}
	return nil, errors.New("another claude-usage process is fetching")
}

// Reset deletes everything we own.
func (s *Store) Reset() error {
	var firstErr error
	for _, p := range []string{s.statePath(), s.historyPath(), s.lockPath(), s.remotePath()} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
