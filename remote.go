package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// NowResponse is what the server returns from /now and /sample: the current
// usage plus the rates it measured from its history.
type NowResponse struct {
	FetchedAt time.Time           `json:"fetched_at"`
	Plan      string              `json:"plan"`
	Tier      string              `json:"tier"`
	PlanLabel string              `json:"plan_label"`
	Usage     *Usage              `json:"usage"`
	Raw       json.RawMessage     `json:"raw,omitempty"`
	Rates     map[string]RateInfo `json:"rates"`
	Verdict   string              `json:"verdict"`
	Headline  string              `json:"headline"`
	Limits    []limitJSON         `json:"limits"`
	Spend     *Spend              `json:"spend,omitempty"`
	History   struct {
		Samples int       `json:"samples"`
		Since   time.Time `json:"since"`
	} `json:"history"`
	Server string `json:"server"`
}

// SamplePost is what a client sends to /sample: Anthropic's response verbatim
// plus the plan from its credentials file.
type SamplePost struct {
	Usage json.RawMessage `json:"usage"`
	Plan  string          `json:"plan"`
	Tier  string          `json:"tier"`
}

func call(method, remote, path, secret string, body []byte) (*NowResponse, error) {
	req, err := http.NewRequest(method, strings.TrimRight(remote, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned HTTP %d", resp.StatusCode)
	}
	var res NowResponse
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, fmt.Errorf("bad response from server: %v", err)
	}
	return &res, nil
}

// fetchNow asks the server for /now.
func fetchNow(remote, secret string) (*NowResponse, error) {
	return call("GET", remote, "/now", secret, nil)
}

// postSample hands one Anthropic response to the server and gets /now back.
func postSample(remote, secret string, raw json.RawMessage, c Creds) (*NowResponse, error) {
	body, _ := json.Marshal(SamplePost{Usage: raw, Plan: c.SubscriptionType, Tier: c.RateLimitTier})
	return call("POST", remote, "/sample", secret, body)
}

func hostOf(remote string) string {
	if u, err := url.Parse(remote); err == nil && u.Host != "" {
		return u.Host
	}
	return remote
}

// cache is the one file the client keeps: the server's last answer, shown as
// stale when the server is unreachable, and the backoff after a failed fetch.
type cache struct {
	Remote     string      `json:"remote"`
	ReceivedAt time.Time   `json:"received_at"`
	Response   NowResponse `json:"response"`
	NextTry    time.Time   `json:"next_try"`
	Backoff    int         `json:"backoff"`
	LastError  string      `json:"last_error,omitempty"`
}

func cachePath() string {
	base, err := os.UserCacheDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, "cuh", "cache.json")
}

func loadCache() *cache {
	var c cache
	if raw, err := os.ReadFile(cachePath()); err == nil {
		json.Unmarshal(raw, &c)
	}
	return &c
}

func saveCache(c *cache) {
	path := cachePath()
	os.MkdirAll(filepath.Dir(path), 0o700)
	if raw, err := json.Marshal(c); err == nil {
		os.WriteFile(path, raw, 0o600)
	}
}
