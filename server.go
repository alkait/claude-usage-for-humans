package main

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	inMemoryHistory = 90 * 24 * time.Hour // enough for recent pace and the usual-pace baseline
	dedupeWindow    = time.Minute         // two laptops posting within a minute count once
)

//go:embed web/index.html
var webFS embed.FS

type serveOptions struct {
	listen, dataDir, secret string
}

// Server holds no Claude login. Clients sample Anthropic with their own login
// and post the result here; the server keeps every sample on disk, measures
// pace from that history, and answers /now with the verdict.
type Server struct {
	opt   serveOptions
	mu    sync.RWMutex
	st    State
	hist  []Sample // recent samples, oldest first
	since time.Time
	total int
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func runServe(args []string) {
	var o serveOptions
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	fs.StringVar(&o.listen, "listen", envOr("CUH_LISTEN", ":8787"), "address to listen on")
	fs.StringVar(&o.dataDir, "data-dir", envOr("CUH_DATA", "./data"), "where history files live")
	fs.StringVar(&o.secret, "secret", os.Getenv("CUH_SECRET"), "shared secret clients must send; empty means open")
	fs.Parse(args)
	if err := os.MkdirAll(o.dataDir, 0o700); err != nil {
		fatal(err)
	}
	s := &Server{opt: o}
	if err := s.load(time.Now()); err != nil {
		fatal(err)
	}
	log.Printf("cuh %s serving on %s, %d samples on disk in %s", version, o.listen, s.total, o.dataDir)
	if o.secret == "" {
		log.Printf("warning: no secret set, anyone who can reach this port can read your usage")
	}
	if err := http.ListenAndServe(o.listen, s.handler()); err != nil {
		fatal(err)
	}
}

// record stores one posted usage. Samples inside dedupeWindow of the previous
// one update the numbers but add no history line.
func (s *Server) record(u *Usage, raw json.RawMessage, plan, tier string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fresh := s.st.FetchedAt.IsZero() || now.Sub(s.st.FetchedAt) >= dedupeWindow
	s.st = State{FetchedAt: now, Plan: plan, Tier: tier, Usage: u, Raw: raw}
	if !fresh {
		return
	}
	smp := newSample(u, now)
	if err := s.appendSample(smp); err != nil {
		log.Printf("history: %v", err)
	}
	s.hist = append(s.hist, smp)
	s.trim(now)
	s.total++
	if s.since.IsZero() {
		s.since = smp.T
	}
	log.Printf("sample %s", summarize(u))
}

func summarize(u *Usage) string {
	var parts []string
	for _, l := range u.Limits {
		parts = append(parts, fmt.Sprintf("%s %d%%", l.Key(), int(l.Percent+0.5)))
	}
	return strings.Join(parts, " ")
}

func (s *Server) trim(now time.Time) {
	cut := now.Add(-inMemoryHistory)
	i := 0
	for i < len(s.hist) && s.hist[i].T.Before(cut) {
		i++
	}
	s.hist = s.hist[i:]
}

func (s *Server) historyPath() string { return filepath.Join(s.opt.dataDir, "history.jsonl") }

func (s *Server) appendSample(smp Sample) error {
	line, err := json.Marshal(smp)
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

// load fills the in-memory window from disk and counts everything. Current
// numbers arrive with the next client post; until then /now says so.
func (s *Server) load(now time.Time) error {
	smps, err := readSamples(s.historyPath())
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	sort.SliceStable(smps, func(i, j int) bool { return smps[i].T.Before(smps[j].T) })
	s.total = len(smps)
	if len(smps) > 0 {
		s.since = smps[0].T
	}
	cut := now.Add(-inMemoryHistory)
	for _, smp := range smps {
		if !smp.T.Before(cut) {
			s.hist = append(s.hist, smp)
		}
	}
	return nil
}

// ---- HTTP ----

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	page, _ := webFS.ReadFile("web/index.html")
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(page)
	})
	mux.HandleFunc("/ping", s.auth(s.handlePing))
	mux.HandleFunc("/now", s.auth(s.handleNow))
	mux.HandleFunc("/sample", s.auth(s.handleSample))
	return mux
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.opt.secret != "" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(s.opt.secret)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next(w, r)
	}
}

func (s *Server) now(at time.Time) NowResponse {
	s.mu.RLock()
	defer s.mu.RUnlock()
	res := NowResponse{FetchedAt: s.st.FetchedAt, Plan: s.st.Plan, Tier: s.st.Tier, Usage: s.st.Usage, Raw: s.st.Raw, Rates: map[string]RateInfo{}, Server: version}
	res.PlanLabel = Creds{SubscriptionType: s.st.Plan, RateLimitTier: s.st.Tier}.PlanLabel()
	res.History.Samples, res.History.Since = s.total, s.since
	res.Verdict = VUnknown.Slug()
	if s.st.Usage == nil {
		res.Headline = headline(nil, nil)
		return res
	}
	hist := historyRates(s.hist)
	as := assessAll(s.st.Usage, hist, at)
	b := binding(as)
	if b != nil {
		res.Verdict = b.Verdict.Slug()
	}
	res.Headline = headline(b, as)
	res.Limits = limitsJSON(as)
	res.Spend = s.st.Usage.Spend
	for _, l := range s.st.Usage.Limits {
		var ri RateInfo
		if l.ResetsAt != nil {
			if w := windowFor(l.Group); w > 0 {
				ri.Recent, ri.RecentOK = hist.Recent(l.Key(), *l.ResetsAt, w/8, at, l.Percent)
			}
		}
		ri.Typical, ri.TypicalOK = hist.Typical(l.Key())
		res.Rates[l.Key()] = ri
	}
	return res
}

// handlePing answers only when the caller's secret is accepted, so the
// dashboard can test a secret without pulling the whole /now payload.
func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "secret_required": s.opt.secret != "", "server": version})
}

func (s *Server) handleNow(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.now(time.Now()))
}

// handleSample takes one usage response a client fetched from Anthropic,
// stamps it with the server clock, and answers like /now.
func (s *Server) handleSample(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var in SamplePost
	var u Usage
	if json.Unmarshal(body, &in) != nil || json.Unmarshal(in.Usage, &u) != nil || len(u.Limits) == 0 {
		http.Error(w, "bad sample", http.StatusBadRequest)
		return
	}
	now := time.Now()
	s.record(&u, in.Usage, in.Plan, in.Tier, now)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.now(now))
}
