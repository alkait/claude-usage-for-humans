package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// NowResponse is what a claude-usage server returns from /now: the current
// usage plus the rates it has already measured from its history.
type NowResponse struct {
	FetchedAt time.Time           `json:"fetched_at"`
	Plan      string              `json:"plan"`
	Tier      string              `json:"tier"`
	PlanLabel string              `json:"plan_label"`
	Usage     *Usage              `json:"usage"`
	Raw       json.RawMessage     `json:"raw,omitempty"`
	Error     string              `json:"error,omitempty"`
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

// fetchNow asks a server for /now.
func fetchNow(remote, secret string) (*NowResponse, error) {
	req, err := http.NewRequest("GET", strings.TrimRight(remote, "/")+"/now", nil)
	if err != nil {
		return nil, err
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned HTTP %d", resp.StatusCode)
	}
	var out NowResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("bad response from server: %v", err)
	}
	return &out, nil
}

func hostOf(remote string) string {
	if u, err := url.Parse(remote); err == nil && u.Host != "" {
		return u.Host
	}
	return remote
}
