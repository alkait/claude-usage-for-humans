package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

const fakeUsage = `{"limits":[{"kind":"session","group":"session","percent":12,"resets_at":"2099-01-01T00:00:00Z"}],"spend":{"used":{"amount_minor":0,"currency":"USD","exponent":2},"limit":{"amount_minor":2000,"currency":"USD","exponent":2}},"seven_day_breakdown":{"rows":[{"key":"claude_code","display_name":"Claude Code","percent":99},{"key":"chat","display_name":"Chats","percent":0}]}}`

func fakeCreds(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"x","subscriptionType":"max","rateLimitTier":"default_claude_max_20x"}}`), 0o600)
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
}

func TestRefreshBacksOffOn429AndServesCache(t *testing.T) {
	fakeCreds(t)
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n == 1 {
			w.Write([]byte(fakeUsage))
			return
		}
		w.Header().Set("Retry-After", "900")
		w.WriteHeader(429)
	}))
	defer srv.Close()
	usageURL = srv.URL
	store := &Store{Dir: t.TempDir()}
	st := store.LoadState()
	now := time.Now()

	if ok, _ := refresh(store, st, 0, false, false, now); !ok || st.Usage == nil {
		t.Fatal("first refresh should fetch")
	}
	if ok, _ := refresh(store, st, 0, false, false, now); ok || st.Backoff != 1 {
		t.Fatalf("second refresh should hit 429 and back off: fetched=%v backoff=%d", ok, st.Backoff)
	}
	if st.Usage == nil || st.Usage.Limits[0].Percent != 12 {
		t.Fatal("cached numbers must survive a 429")
	}
	wait := st.NextAllowedAt.Sub(now)
	if wait < 14*time.Minute || wait > 16*time.Minute {
		t.Fatalf("Retry-After should win over the 5m base backoff, got %v", wait)
	}
	for i := 0; i < 5; i++ {
		refresh(store, st, 0, false, false, now.Add(time.Minute))
	}
	if atomic.LoadInt32(&hits) != 2 {
		t.Fatalf("no request may go out during backoff, server saw %d", hits)
	}
	if _, note := refresh(store, st, 0, false, true, now.Add(2*time.Minute)); note == "" {
		t.Fatal("manual refresh during backoff should explain itself")
	}
}

func TestRefreshHonoursMaxAge(t *testing.T) {
	fakeCreds(t)
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write([]byte(fakeUsage))
	}))
	defer srv.Close()
	usageURL = srv.URL
	store := &Store{Dir: t.TempDir()}
	st := store.LoadState()
	now := time.Now()
	refresh(store, st, 3*time.Minute, false, false, now)
	for i := 0; i < 20; i++ {
		refresh(store, st, 3*time.Minute, false, false, now.Add(time.Duration(i)*5*time.Second))
	}
	if hits != 1 {
		t.Fatalf("status-line style polling must be served from cache, server saw %d", hits)
	}
	refresh(store, st, 3*time.Minute, false, false, now.Add(4*time.Minute))
	if hits != 2 {
		t.Fatalf("stale cache should refetch once, server saw %d", hits)
	}
	if got := len(store.LoadHistory()); got != 2 {
		t.Fatalf("history samples = %d, want 2", got)
	}
}
