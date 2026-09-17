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
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

// startServer runs a cuh server in-process and returns its URL.
func startServer(t *testing.T) (*Server, string) {
	t.Helper()
	s := &Server{opt: serveOptions{dataDir: t.TempDir(), secret: "s3cret"}}
	if err := s.load(time.Now()); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.handler())
	t.Cleanup(srv.Close)
	return s, srv.URL
}

func TestClientSamplesOnlyWhenServerIsStale(t *testing.T) {
	fakeCreds(t)
	var hits int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write([]byte(fakeUsage))
	}))
	defer up.Close()
	usageURL = up.URL
	s, url := startServer(t)
	now := time.Now()

	st, _ := acquire(url, "s3cret", now)
	if st.Usage == nil || st.Usage.Limits[0].Percent != 12 || hits != 1 {
		t.Fatalf("empty server: client must sample once and show the result, hits=%d", hits)
	}
	if s.total != 1 {
		t.Fatalf("server should hold one sample, has %d", s.total)
	}
	for i := 0; i < 20; i++ { // status-line polling inside the sampling interval
		acquire(url, "s3cret", now.Add(time.Duration(i)*5*time.Second))
	}
	if hits != 1 {
		t.Fatalf("no sampling while the server's numbers are fresh, hits=%d", hits)
	}
	acquire(url, "s3cret", now.Add(sampleEvery+time.Second))
	if hits != 2 {
		t.Fatalf("stale numbers must trigger one sample, hits=%d", hits)
	}
	if st.Error != "" || st.Offline {
		t.Fatalf("healthy run must carry no error: %q offline=%v", st.Error, st.Offline)
	}
}

func TestClientBacksOffOn429(t *testing.T) {
	fakeCreds(t)
	var hits int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			w.Write([]byte(fakeUsage))
			return
		}
		w.Header().Set("Retry-After", "900")
		w.WriteHeader(429)
	}))
	defer up.Close()
	usageURL = up.URL
	_, url := startServer(t)
	now := time.Now()
	acquire(url, "s3cret", now)
	later := now.Add(sampleEvery + time.Second)
	st, _ := acquire(url, "s3cret", later)
	if hits != 2 || st.Error == "" || st.Usage == nil {
		t.Fatalf("429 must be reported while the server's numbers stay: hits=%d err=%q", hits, st.Error)
	}
	c := loadCache()
	if wait := c.NextTry.Sub(later); wait < 14*time.Minute || wait > 16*time.Minute {
		t.Fatalf("Retry-After should win over the 5m base backoff, got %v", wait)
	}
	for i := 0; i < 5; i++ {
		acquire(url, "s3cret", later.Add(time.Duration(i)*time.Minute))
	}
	if hits != 2 {
		t.Fatalf("no request may go out during backoff, upstream saw %d", hits)
	}
}

func TestClientShowsCacheWhenServerIsDown(t *testing.T) {
	fakeCreds(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(fakeUsage)) }))
	defer up.Close()
	usageURL = up.URL
	_, url := startServer(t)
	now := time.Now()
	acquire(url, "s3cret", now)

	st, _ := acquire("http://127.0.0.1:1", "s3cret", now) // a different server: nothing cached for it
	if st.Usage != nil || !st.Offline {
		t.Fatalf("unknown server must show no numbers: usage=%v offline=%v", st.Usage != nil, st.Offline)
	}
	c := loadCache()
	c.Remote = "http://127.0.0.1:1" // pretend the cached answer came from the dead server
	saveCache(c)
	st, _ = acquire("http://127.0.0.1:1", "s3cret", now.Add(2*time.Hour))
	if st.Usage == nil || !st.Offline || st.Error == "" {
		t.Fatalf("dead server must show cached numbers, flagged: usage=%v offline=%v err=%q", st.Usage != nil, st.Offline, st.Error)
	}
	v := buildView(st, fixedRates(nil), now.Add(2*time.Hour))
	if m := v.marker(); m != "server offline · last update 2h 00m ago" {
		t.Fatalf("status line marker = %q", m)
	}
}

func TestMarkerFlagsStaleAndFailedSampling(t *testing.T) {
	now := time.Now()
	st := &State{FetchedAt: now.Add(-40 * time.Minute), Usage: &Usage{}}
	if m := buildView(st, fixedRates(nil), now).marker(); m != "last update 40m ago" {
		t.Fatalf("stale marker = %q", m)
	}
	st = &State{FetchedAt: now.Add(-6 * time.Minute), Usage: &Usage{}, Error: "token rejected"}
	if m := buildView(st, fixedRates(nil), now).marker(); m != "sampling failed" {
		t.Fatalf("failed marker = %q", m)
	}
	st = &State{FetchedAt: now.Add(-6 * time.Minute), Usage: &Usage{}}
	if m := buildView(st, fixedRates(nil), now).marker(); m != "" {
		t.Fatalf("healthy marker = %q", m)
	}
}
