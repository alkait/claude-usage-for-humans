package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newTestServer(t *testing.T, upstream string) *Server {
	t.Helper()
	usageURL = upstream
	path := writeCreds(t, time.Now().Add(6*time.Hour).UnixMilli())
	s := &Server{opt: serveOptions{dataDir: t.TempDir(), credentials: path, secret: "s3cret", interval: time.Minute}}
	if err := s.loadHistory(time.Now()); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestServerSamplesAndServesNow(t *testing.T) {
	pct := 10.0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Replace(fakeUsage, `"percent":12`, `"percent":`+fmtFloat(pct), 1)))
	}))
	defer up.Close()
	s := newTestServer(t, up.URL)
	now := time.Now()
	s.sampleOnce(now.Add(-30 * time.Minute))
	pct = 25
	s.sampleOnce(now)

	files, _ := s.monthFiles()
	if len(files) != 1 || !strings.HasSuffix(files[0], now.UTC().Format("2006-01")+".jsonl") {
		t.Fatalf("month files: %v", files)
	}
	if s.total != 2 || len(s.hist) != 2 {
		t.Fatalf("total=%d hist=%d", s.total, len(s.hist))
	}

	h := s.handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/now", nil))
	if rec.Code != 401 {
		t.Fatalf("no secret should be refused, got %d", rec.Code)
	}
	req := httptest.NewRequest("GET", "/now", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("/now: %d %s", rec.Code, rec.Body.String())
	}
	var res NowResponse
	json.Unmarshal(rec.Body.Bytes(), &res)
	if res.Usage == nil || res.Usage.Limits[0].Percent != 25 || res.History.Samples != 2 {
		t.Fatalf("now: %+v", res)
	}
	if res.Verdict == "" || res.Headline == "" || len(res.Limits) != 1 || res.Limits[0].Verdict != res.Verdict {
		t.Fatalf("/now must carry the computed view: verdict=%q headline=%q limits=%d", res.Verdict, res.Headline, len(res.Limits))
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<title>Claude usage</title>") {
		t.Fatalf("/ should serve the page without auth: %d", rec.Code)
	}
	ri := res.Rates["session"]
	if !ri.RecentOK || ri.Recent < 29 || ri.Recent > 31 { // 15% over 30 min
		t.Fatalf("recent rate: %+v", ri)
	}

	// the client turns those rates into the same verdict it would compute locally
	as := assessAll(res.Usage, fixedRates(res.Rates), now)
	if as[0].RateSource != "recent" || as[0].Rate != ri.Recent {
		t.Fatalf("client did not use server rates: %+v", as[0])
	}

	req = httptest.NewRequest("GET", "/history?from="+now.Add(-time.Hour).Format(time.RFC3339), nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var smps []Sample
	if err := json.Unmarshal(rec.Body.Bytes(), &smps); err != nil || len(smps) != 2 {
		t.Fatalf("/history: %d samples, err %v, body %s", len(smps), err, rec.Body.String())
	}
	if smps[0].S["claude_code"] != 99 || smps[0].X == nil || smps[0].X.Limit != 2000 {
		t.Fatalf("sample should carry surfaces and spend: %+v", smps[0])
	}

	req = httptest.NewRequest("GET", "/history/"+now.UTC().Format("2006-01")+".jsonl", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || strings.Count(rec.Body.String(), "\n") != 2 {
		t.Fatalf("/history/month: %d lines=%d", rec.Code, strings.Count(rec.Body.String(), "\n"))
	}
}

func TestServerReloadsHistoryFromDisk(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(fakeUsage)) }))
	defer up.Close()
	s := newTestServer(t, up.URL)
	s.sampleOnce(time.Now().Add(-2 * time.Hour))
	s.sampleOnce(time.Now())
	// a very old sample in an older month file counts on disk but stays out of memory
	old := newSample(&Usage{Limits: []Limit{{Kind: "session", Group: "session", Percent: 1}}}, time.Now().Add(-200*24*time.Hour))
	line, _ := json.Marshal(old)
	os.WriteFile(filepath.Join(s.opt.dataDir, monthFilePrefix+old.T.Format("2006-01")+".jsonl"), append(line, '\n'), 0o600)

	s2 := &Server{opt: s.opt}
	if err := s2.loadHistory(time.Now()); err != nil {
		t.Fatal(err)
	}
	if s2.total != 3 || len(s2.hist) != 2 || !s2.since.Equal(old.T) {
		t.Fatalf("total=%d inmem=%d since=%v", s2.total, len(s2.hist), s2.since)
	}
}

func TestServerBacksOffOn429(t *testing.T) {
	hits := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Retry-After", "600")
		w.WriteHeader(429)
	}))
	defer up.Close()
	s := newTestServer(t, up.URL)
	now := time.Now()
	s.sampleOnce(now)
	s.sampleOnce(now.Add(time.Minute))
	s.sampleOnce(now.Add(5 * time.Minute))
	if hits != 1 {
		t.Fatalf("server must honour backoff, upstream saw %d requests", hits)
	}
	if res := s.now(now); res.Error == "" || res.Usage != nil {
		t.Fatalf("/now should report the error: %+v", res)
	}
}

func fmtFloat(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
