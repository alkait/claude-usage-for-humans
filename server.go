package main

import (
	"bufio"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	inMemoryHistory = 90 * 24 * time.Hour // enough for recent pace and the usual-pace baseline
	monthFilePrefix = "history-"
)

var monthFileRe = regexp.MustCompile(`^history-(\d{4}-\d{2})\.jsonl$`)

//go:embed web/index.html
var webFS embed.FS

type serveOptions struct {
	listen, dataDir, credentials, secret string
	interval                             time.Duration
}

// Server samples Anthropic on a schedule, keeps every sample on disk, and
// answers /now with the rates a client needs for its verdict.
type Server struct {
	opt     serveOptions
	mu      sync.RWMutex
	st      State
	hist    []Sample // recent samples, oldest first
	since   time.Time
	total   int
	stopped time.Time // when recording stopped because the login died; zero while healthy
	reason  string
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
	fs.StringVar(&o.listen, "listen", envOr("CLAUDE_USAGE_LISTEN", ":8787"), "address to listen on")
	fs.StringVar(&o.dataDir, "data-dir", envOr("CLAUDE_USAGE_DATA", "./data"), "where history files live")
	fs.StringVar(&o.credentials, "credentials", envOr("CLAUDE_USAGE_CREDENTIALS", credentialsPath()), "Claude Code credentials file (refreshed in place)")
	fs.StringVar(&o.secret, "secret", os.Getenv("CLAUDE_USAGE_SECRET"), "shared secret clients must send; empty means open")
	interval := fs.String("interval", envOr("CLAUDE_USAGE_INTERVAL", "5m"), "sampling interval")
	fs.Parse(args)
	d, err := time.ParseDuration(*interval)
	if err != nil || d < time.Minute {
		fatal(fmt.Errorf("interval must be a duration of at least 1m, got %q", *interval))
	}
	o.interval = d
	if err := os.MkdirAll(o.dataDir, 0o700); err != nil {
		fatal(err)
	}
	s := &Server{opt: o}
	if err := s.loadHistory(time.Now()); err != nil {
		fatal(err)
	}
	log.Printf("claude-usage %s serving on %s, sampling every %s, %d samples on disk in %s", version, o.listen, o.interval, s.total, o.dataDir)
	if o.secret == "" {
		log.Printf("warning: no secret set, anyone who can reach this port can read your usage")
	}
	go s.loop()
	if err := http.ListenAndServe(o.listen, s.handler()); err != nil {
		fatal(err)
	}
}

func (s *Server) loop() {
	s.sampleOnce(time.Now())
	t := time.NewTicker(s.opt.interval)
	for now := range t.C {
		s.sampleOnce(now)
	}
}

// sampleOnce refreshes the token if needed, fetches, and records one sample.
func (s *Server) sampleOnce(now time.Time) {
	s.mu.RLock()
	wait := s.st.NextAllowedAt.Sub(now)
	s.mu.RUnlock()
	if wait > 0 {
		log.Printf("skipping sample, backing off for another %s", fmtDur(wait))
		return
	}
	creds, refreshed, err := refreshIfNeeded(s.opt.credentials, false, now)
	if err != nil {
		var ae *AuthError
		expired := creds.AccessToken == "" || time.UnixMilli(creds.ExpiresAt).Before(now)
		if errors.As(err, &ae) && expired {
			s.stop(now, ae.Reason+". Log in again on the server to resume.")
			return
		}
		if expired {
			s.setError(err.Error(), now.Add(time.Minute))
			log.Printf("credentials: %v", err)
			return
		}
		log.Printf("warning: %v (using current token)", err)
	} else if refreshed {
		log.Printf("token refreshed, next expiry %s", time.UnixMilli(creds.ExpiresAt).Format(time.RFC3339))
	}
	u, raw, err := fetchUsage(creds.AccessToken)
	if err != nil {
		var fe *FetchError
		if errors.As(err, &fe) && (fe.Status == 401 || fe.Status == 403) {
			s.stop(now, "Anthropic rejected its Claude login (HTTP "+fmt.Sprint(fe.Status)+"). Log in again on the server to resume.")
			return
		}
		s.mu.Lock()
		noteFetchError(&s.st, err, now)
		msg := s.st.LastError
		s.mu.Unlock()
		log.Printf("fetch: %s", msg)
		return
	}
	s.mu.Lock()
	if !s.stopped.IsZero() {
		log.Printf("recording resumed")
	}
	s.stopped, s.reason = time.Time{}, ""
	s.mu.Unlock()
	smp := newSample(u, now)
	if err := s.appendSample(smp); err != nil {
		log.Printf("history: %v", err)
	}
	s.mu.Lock()
	s.st.Usage, s.st.Raw, s.st.FetchedAt = u, raw, now
	s.st.Plan, s.st.Tier = creds.SubscriptionType, creds.RateLimitTier
	s.st.Backoff, s.st.NextAllowedAt, s.st.LastError = 0, time.Time{}, ""
	s.hist = append(s.hist, smp)
	s.trim(now)
	s.total++
	if s.since.IsZero() {
		s.since = smp.T
	}
	s.mu.Unlock()
	log.Printf("sample %s", summarize(u))
}

// stop records why sampling cannot continue. The loop keeps checking the
// credentials file on every tick, so replacing it resumes recording.
func (s *Server) stop(now time.Time, reason string) {
	s.mu.Lock()
	first := s.stopped.IsZero()
	if first {
		s.stopped = now
	}
	s.reason = reason
	s.st.LastError, s.st.LastErrorAt = reason, now
	s.mu.Unlock()
	if first {
		log.Print(reason)
	}
}

func (s *Server) setError(msg string, retryAt time.Time) {
	s.mu.Lock()
	s.st.LastError, s.st.LastErrorAt, s.st.NextAllowedAt = msg, time.Now(), retryAt
	s.mu.Unlock()
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

func (s *Server) monthPath(t time.Time) string {
	return filepath.Join(s.opt.dataDir, monthFilePrefix+t.UTC().Format("2006-01")+".jsonl")
}

func (s *Server) appendSample(smp Sample) error {
	line, err := json.Marshal(smp)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.monthPath(smp.T), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// monthFiles lists history files oldest first.
func (s *Server) monthFiles() ([]string, error) {
	entries, err := os.ReadDir(s.opt.dataDir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if monthFileRe.MatchString(e.Name()) {
			out = append(out, filepath.Join(s.opt.dataDir, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

func readSamples(path string, keep func(Sample) bool) ([]Sample, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	var out []Sample
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var smp Sample
		if json.Unmarshal(sc.Bytes(), &smp) != nil || smp.T.IsZero() {
			continue
		}
		n++
		if keep == nil || keep(smp) {
			out = append(out, smp)
		}
	}
	return out, n, sc.Err()
}

// loadHistory fills the in-memory window from disk and counts everything.
func (s *Server) loadHistory(now time.Time) error {
	files, err := s.monthFiles()
	if err != nil {
		return err
	}
	cut := now.Add(-inMemoryHistory)
	for _, path := range files {
		smps, n, err := readSamples(path, func(smp Sample) bool { return !smp.T.Before(cut) })
		if err != nil {
			return fmt.Errorf("%s: %v", path, err)
		}
		s.total += n
		s.hist = append(s.hist, smps...)
		if s.since.IsZero() && n > 0 {
			first, _, _ := readSamples(path, nil)
			if len(first) > 0 {
				s.since = first[0].T
			}
		}
	}
	sort.SliceStable(s.hist, func(i, j int) bool { return s.hist[i].T.Before(s.hist[j].T) })
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
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("/now", s.auth(s.handleNow))
	mux.HandleFunc("/history", s.auth(s.handleHistory))
	mux.HandleFunc("/history/", s.auth(s.handleHistoryFile))
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
	res := NowResponse{FetchedAt: s.st.FetchedAt, Plan: s.st.Plan, Tier: s.st.Tier, Usage: s.st.Usage, Raw: s.st.Raw, Error: s.st.LastError, Rates: map[string]RateInfo{}, Server: version}
	res.PlanLabel = Creds{SubscriptionType: s.st.Plan, RateLimitTier: s.st.Tier}.PlanLabel()
	res.History.Samples, res.History.Since = s.total, s.since
	res.Recording = Recording{Active: s.stopped.IsZero(), StoppedAt: s.stopped, Reason: s.reason, LastSample: s.st.FetchedAt}
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

func (s *Server) handleNow(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.now(time.Now()))
}

func parseWhen(v string, def time.Time) (time.Time, error) {
	if v == "" {
		return def, nil
	}
	v = strings.ReplaceAll(v, " ", "+") // an unescaped "+04:00" offset arrives as a space
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return t, nil
	}
	return time.Time{}, errors.New("use RFC3339 or YYYY-MM-DD")
}

// handleHistory streams samples in a range as a JSON array.
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	from, err1 := parseWhen(r.URL.Query().Get("from"), now.Add(-7*24*time.Hour))
	to, err2 := parseWhen(r.URL.Query().Get("to"), now)
	if err1 != nil || err2 != nil || !to.After(from) {
		http.Error(w, "bad range: from must precede to; "+errors.Join(err1, err2).Error(), http.StatusBadRequest)
		return
	}
	files, err := s.monthFiles()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte("["))
	first := true
	enc := json.NewEncoder(w)
	for _, path := range files {
		m := monthFileRe.FindStringSubmatch(filepath.Base(path))[1]
		start, _ := time.Parse("2006-01", m)
		if start.AddDate(0, 1, 0).Before(from) || start.After(to) {
			continue
		}
		smps, _, err := readSamples(path, func(smp Sample) bool { return !smp.T.Before(from) && !smp.T.After(to) })
		if err != nil {
			continue
		}
		for _, smp := range smps {
			if !first {
				w.Write([]byte(","))
			}
			first = false
			enc.Encode(smp)
		}
	}
	w.Write([]byte("]\n"))
}

// handleHistoryFile serves one month as raw JSONL.
func (s *Server) handleHistoryFile(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/history/")
	if !regexp.MustCompile(`^\d{4}-\d{2}\.jsonl$`).MatchString(name) {
		http.Error(w, "use /history/YYYY-MM.jsonl", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	http.ServeFile(w, r, filepath.Join(s.opt.dataDir, monthFilePrefix+name))
}
