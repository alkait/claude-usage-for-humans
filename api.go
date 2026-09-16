package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var usageURL = "https://api.anthropic.com/api/oauth/usage"

// Limit is one row of the "limits" array returned by the usage endpoint.
type Limit struct {
	Kind     string     `json:"kind"`
	Group    string     `json:"group"`
	Percent  float64    `json:"percent"`
	Severity string     `json:"severity"`
	ResetsAt *time.Time `json:"resets_at"`
	Scope    *struct {
		Model *struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
		Surface *string `json:"surface"`
	} `json:"scope"`
	IsActive bool `json:"is_active"`
}

// Key identifies a limit across fetches ("session", "weekly_all", "model:fable").
func (l Limit) Key() string {
	if l.Scope != nil && l.Scope.Model != nil && l.Scope.Model.DisplayName != "" {
		return "model:" + strings.ToLower(l.Scope.Model.DisplayName)
	}
	return l.Kind
}

// Name is the human label for a limit.
func (l Limit) Name() string {
	if l.Scope != nil && l.Scope.Model != nil && l.Scope.Model.DisplayName != "" {
		return l.Scope.Model.DisplayName
	}
	switch l.Kind {
	case "session":
		return "Session"
	case "weekly_all":
		return "Weekly"
	}
	return l.Kind
}

// Money is the endpoint's minor-unit money shape.
type Money struct {
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
	Exponent    int    `json:"exponent"`
}

// Float converts minor units to a float amount.
func (m Money) Float() float64 {
	f := float64(m.AmountMinor)
	for i := 0; i < m.Exponent; i++ {
		f /= 10
	}
	return f
}

// Spend is the extra-usage (credits) block.
type Spend struct {
	Used           Money   `json:"used"`
	Limit          Money   `json:"limit"`
	Percent        float64 `json:"percent"`
	Enabled        bool    `json:"enabled"`
	DisabledReason string  `json:"disabled_reason"`
}

// Breakdown reports which surface consumed the weekly window.
type Breakdown struct {
	WindowStartedAt *time.Time `json:"window_started_at"`
	Rows            []struct {
		Key         string  `json:"key"`
		DisplayName string  `json:"display_name"`
		Percent     float64 `json:"percent"`
	} `json:"rows"`
}

// Usage is the subset of the endpoint response we use.
type Usage struct {
	Limits            []Limit    `json:"limits"`
	Spend             *Spend     `json:"spend"`
	SevenDayBreakdown *Breakdown `json:"seven_day_breakdown"`
}

// Creds is what we need from Claude Code's stored OAuth credentials.
type Creds struct {
	AccessToken      string `json:"accessToken"`
	SubscriptionType string `json:"subscriptionType"`
	RateLimitTier    string `json:"rateLimitTier"`
}

// PlanLabel renders "MAX 20X", "PRO", etc.
func (c Creds) PlanLabel() string {
	plan := strings.ToUpper(c.SubscriptionType)
	if plan == "" {
		plan = "CLAUDE"
	}
	if i := strings.LastIndex(c.RateLimitTier, "_"); i >= 0 && strings.Contains(c.RateLimitTier, "max_") {
		mult := strings.ToUpper(c.RateLimitTier[i+1:])
		if strings.HasSuffix(mult, "X") {
			plan += " " + mult
		}
	}
	return plan
}

func credentialsPath() string {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	return filepath.Join(dir, ".credentials.json")
}

// loadCreds reads the token Claude Code keeps on disk, or in the macOS Keychain.
// It never writes: refreshing is Claude Code's job, and two refreshers would
// log each other out.
func loadCreds() (Creds, error) {
	raw, err := os.ReadFile(credentialsPath())
	if err != nil && runtime.GOOS == "darwin" {
		out, kerr := exec.Command("security", "find-generic-password", "-s", "Claude Code-credentials", "-w").Output()
		if kerr == nil {
			raw, err = out, nil
		}
	}
	if err != nil {
		return Creds{}, fmt.Errorf("no Claude Code credentials found (%v); run `claude` and log in first", err)
	}
	var wrapper struct {
		OAuth Creds `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return Creds{}, fmt.Errorf("credentials file is not valid JSON: %v", err)
	}
	if wrapper.OAuth.AccessToken == "" {
		return Creds{}, errors.New("credentials file has no access token; run `claude` and log in")
	}
	return wrapper.OAuth, nil
}

// FetchError carries the HTTP outcome so the caller can back off correctly.
type FetchError struct {
	Status     int
	RetryAfter time.Duration
	Msg        string
}

func (e *FetchError) Error() string { return e.Msg }

// fetchUsage performs exactly one request against the usage endpoint.
func fetchUsage(token string) (*Usage, json.RawMessage, error) {
	req, _ := http.NewRequest("GET", usageURL, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("User-Agent", "cuh/"+version)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, &FetchError{Status: 0, Msg: "network error: " + shortErr(err)}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusTooManyRequests:
		ra := parseRetryAfter(resp.Header.Get("Retry-After"))
		return nil, nil, &FetchError{Status: 429, RetryAfter: ra, Msg: "rate limited by Anthropic (HTTP 429)"}
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, nil, &FetchError{Status: resp.StatusCode, Msg: fmt.Sprintf("token rejected (HTTP %d); open Claude Code once so it refreshes the login", resp.StatusCode)}
	default:
		return nil, nil, &FetchError{Status: resp.StatusCode, Msg: fmt.Sprintf("HTTP %d from usage endpoint", resp.StatusCode)}
	}
	var u Usage
	if err := json.Unmarshal(body, &u); err != nil {
		return nil, nil, &FetchError{Status: 200, Msg: "could not parse usage response: " + err.Error()}
	}
	return &u, json.RawMessage(body), nil
}

func parseRetryAfter(h string) time.Duration {
	if h == "" {
		return 0
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(h)); err == nil {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		return time.Until(t)
	}
	return 0
}

func shortErr(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 {
		s = s[i+2:]
	}
	return s
}
