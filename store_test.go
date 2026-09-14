package main

import (
	"os"
	"testing"
	"time"
)

func TestThinKeepsRecentAndSparseOld(t *testing.T) {
	now := time.Now()
	var hist []Sample
	for m := 0; m < 4*24*60; m += 3 { // 4 days, every 3 minutes
		hist = append(hist, Sample{T: now.Add(-time.Duration(m) * time.Minute)})
	}
	// oldest first
	for i, j := 0, len(hist)-1; i < j; i, j = i+1, j-1 {
		hist[i], hist[j] = hist[j], hist[i]
	}
	out := thin(hist, now)
	recent, old := 0, 0
	for _, s := range out {
		if s.T.After(now.Add(-thinAfter)) {
			recent++
		} else {
			old++
		}
	}
	if recent != 48*20 {
		t.Fatalf("recent kept = %d, want %d", recent, 48*20)
	}
	if old > 2*24*4+2 {
		t.Fatalf("old kept = %d, expected about one per 15 minutes", old)
	}
}

func TestCompactDropsSamplesOlderThan30Days(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	now := time.Now()
	u := &Usage{Limits: []Limit{{Kind: "session", Group: "session", Percent: 1}}}
	if err := s.AppendSample(u, now.Add(-40*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendSample(u, now); err != nil {
		t.Fatal(err)
	}
	st := &State{}
	s.CompactIfNeeded(st, now)
	if got := len(s.LoadHistory()); got != 1 {
		t.Fatalf("kept %d samples, want 1", got)
	}
	if st.LastCompact.IsZero() {
		t.Fatal("LastCompact not recorded")
	}
}

func TestLockIsExclusiveAndStaleLocksClear(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	now := time.Now()
	unlock, err := s.TryLock(now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.TryLock(now); err == nil {
		t.Fatal("second lock should fail while first is held")
	}
	unlock()
	if _, err := s.TryLock(now); err != nil {
		t.Fatal("lock should be free after unlock")
	}
	// a lock left behind by a crashed process is ignored after lockStale
	os.Chtimes(s.lockPath(), now.Add(-time.Minute), now.Add(-time.Minute))
	if _, err := s.TryLock(now); err != nil {
		t.Fatal("stale lock should be cleared")
	}
}
