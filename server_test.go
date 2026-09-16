package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func post(t *testing.T, h http.Handler, secret, usage string) (int, NowResponse) {
	t.Helper()
	body, _ := json.Marshal(SamplePost{Usage: json.RawMessage(usage), Plan: "max", Tier: "default_claude_max_20x"})
	req := httptest.NewRequest("POST", "/sample", strings.NewReader(string(body)))
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var res NowResponse
	json.Unmarshal(rec.Body.Bytes(), &res)
	return rec.Code, res
}

func TestServerStoresSamplesAndServesNow(t *testing.T) {
	s := &Server{opt: serveOptions{dataDir: t.TempDir(), secret: "s3cret"}}
	s.load(time.Now())
	h := s.handler()

	if code, _ := post(t, h, "", fakeUsage); code != 401 {
		t.Fatalf("no secret should be refused, got %d", code)
	}
	if code, _ := post(t, h, "s3cret", `{"limits":[]}`); code != 400 {
		t.Fatalf("empty usage should be refused, got %d", code)
	}
	code, res := post(t, h, "s3cret", fakeUsage)
	if code != 200 || res.Usage == nil || res.Usage.Limits[0].Percent != 12 || res.PlanLabel != "MAX 20X" {
		t.Fatalf("/sample: %d %+v", code, res)
	}
	if time.Since(res.FetchedAt) > time.Minute {
		t.Fatalf("server must stamp the sample with its own clock, got %v", res.FetchedAt)
	}
	// a second laptop posting within the minute updates the numbers but adds no line
	post(t, h, "s3cret", strings.Replace(fakeUsage, `"percent":12`, `"percent":13`, 1))
	if s.total != 1 || len(s.hist) != 1 {
		t.Fatalf("duplicate within a minute must not add history: total=%d", s.total)
	}
	req := httptest.NewRequest("GET", "/now", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != 200 || res.Usage.Limits[0].Percent != 13 || res.History.Samples != 1 {
		t.Fatalf("/now: %d percent=%v samples=%d", rec.Code, res.Usage.Limits[0].Percent, res.History.Samples)
	}
	if smps, _ := readSamples(s.historyPath()); len(smps) != 1 || smps[0].L["session"].P != 12 {
		t.Fatalf("history on disk: %+v", smps)
	}
}

func TestServerReloadsFromDisk(t *testing.T) {
	dir := t.TempDir()
	s := &Server{opt: serveOptions{dataDir: dir}}
	s.load(time.Now())
	now := time.Now()
	var u Usage
	json.Unmarshal([]byte(fakeUsage), &u)
	s.record(&u, json.RawMessage(fakeUsage), "max", "", now.Add(-2*time.Hour))
	s.record(&u, json.RawMessage(fakeUsage), "max", "", now)

	s2 := &Server{opt: serveOptions{dataDir: dir}}
	if err := s2.load(now); err != nil {
		t.Fatal(err)
	}
	if s2.total != 2 || len(s2.hist) != 2 || !s2.since.Equal(now.Add(-2*time.Hour).UTC().Truncate(time.Second)) {
		t.Fatalf("reload: total=%d hist=%d since=%v", s2.total, len(s2.hist), s2.since)
	}
	if res := s2.now(now); res.Usage != nil || res.FetchedAt.IsZero() == false {
		t.Fatalf("a restarted server has no current numbers until a client posts: %+v", res)
	}
}

func TestPingChecksTheSecret(t *testing.T) {
	s := &Server{opt: serveOptions{dataDir: t.TempDir(), secret: "s3cret"}}
	h := s.handler()
	ping := func(secret string) (int, map[string]any) {
		req := httptest.NewRequest("GET", "/ping", nil)
		if secret != "" {
			req.Header.Set("Authorization", "Bearer "+secret)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var body map[string]any
		json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body
	}
	if code, _ := ping(""); code != 401 {
		t.Fatalf("no secret: %d", code)
	}
	if code, _ := ping("wrong"); code != 401 {
		t.Fatalf("wrong secret: %d", code)
	}
	code, body := ping("s3cret")
	if code != 200 || body["ok"] != true || body["secret_required"] != true {
		t.Fatalf("right secret: %d %v", code, body)
	}
	s.opt.secret = ""
	code, body = ping("anything")
	if code != 200 || body["secret_required"] != false {
		t.Fatalf("open server: %d %v", code, body)
	}
}
