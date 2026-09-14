package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// Claude Code's public OAuth client. The refresh grant returns a new token pair,
// which we write back to the same credentials file Claude Code reads.
var (
	oauthTokenURL = "https://console.anthropic.com/v1/oauth/token"
	oauthClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
)

const refreshLeeway = 15 * time.Minute

// AuthError means the stored login cannot be used or renewed. The server
// stops recording on it and reports the reason.
type AuthError struct{ Reason string }

func (e *AuthError) Error() string { return e.Reason }

// refreshIfNeeded refreshes the access token when it is within refreshLeeway of
// expiry, or unconditionally when force is set. It returns the credentials to
// use and whether a refresh happened. Other keys in the file are preserved.
func refreshIfNeeded(path string, force bool, now time.Time) (Creds, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Creds{}, false, err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Creds{}, false, fmt.Errorf("credentials file is not valid JSON: %v", err)
	}
	var oauth map[string]any
	if err := json.Unmarshal(doc["claudeAiOauth"], &oauth); err != nil || oauth == nil {
		return Creds{}, false, errors.New("credentials file has no claudeAiOauth section")
	}
	var c Creds
	json.Unmarshal(doc["claudeAiOauth"], &c)
	var extra struct {
		RefreshTokenExpiresAt int64 `json:"refreshTokenExpiresAt"`
	}
	json.Unmarshal(doc["claudeAiOauth"], &extra)
	if c.AccessToken == "" {
		return Creds{}, false, &AuthError{"Its credentials file has no access token"}
	}
	if !force && time.UnixMilli(c.ExpiresAt).Sub(now) > refreshLeeway {
		return c, false, nil
	}
	if c.RefreshToken == "" {
		return c, false, &AuthError{"Its Claude login expired and has no refresh token"}
	}
	if extra.RefreshTokenExpiresAt > 0 && time.UnixMilli(extra.RefreshTokenExpiresAt).Before(now) {
		return c, false, &AuthError{"Its Claude login expired"}
	}

	body, _ := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": c.RefreshToken,
		"client_id":     oauthClientID,
	})
	req, _ := http.NewRequest("POST", oauthTokenURL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "claude-usage/"+version)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return c, false, fmt.Errorf("token refresh: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return c, false, &AuthError{fmt.Sprintf("Anthropic rejected its Claude login (HTTP %d)", resp.StatusCode)}
	}
	if resp.StatusCode != http.StatusOK {
		return c, false, fmt.Errorf("token refresh: HTTP %d: %s", resp.StatusCode, truncate(string(out), 200))
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(out, &tok); err != nil || tok.AccessToken == "" {
		return c, false, errors.New("token refresh: unexpected response")
	}
	c.AccessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		c.RefreshToken = tok.RefreshToken
	}
	c.ExpiresAt = now.Add(time.Duration(tok.ExpiresIn) * time.Second).UnixMilli()
	oauth["accessToken"] = c.AccessToken
	oauth["refreshToken"] = c.RefreshToken
	oauth["expiresAt"] = c.ExpiresAt
	doc["claudeAiOauth"], _ = json.Marshal(oauth)
	newRaw, _ := json.Marshal(doc)
	// Written in place rather than via rename, so it also works when the file
	// itself is bind-mounted into a container.
	if err := os.WriteFile(path, newRaw, 0o600); err != nil {
		return c, true, fmt.Errorf("token refreshed but could not save it: %v", err)
	}
	return c, true, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
