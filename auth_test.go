package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeCreds(t *testing.T, expiresAt int64) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".credentials.json")
	doc := map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken": "old-access", "refreshToken": "old-refresh", "expiresAt": expiresAt,
			"scopes": []string{"user:inference"}, "subscriptionType": "max", "rateLimitTier": "default_claude_max_20x",
		},
		"somethingElse": map[string]any{"keep": true},
	}
	raw, _ := json.Marshal(doc)
	os.WriteFile(path, raw, 0o600)
	return path
}

func TestRefreshWritesNewPairAndPreservesOtherKeys(t *testing.T) {
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":28800}`))
	}))
	defer srv.Close()
	oauthTokenURL = srv.URL
	now := time.Now()
	path := writeCreds(t, now.Add(5*time.Minute).UnixMilli()) // expiring soon

	c, did, err := refreshIfNeeded(path, false, now)
	if err != nil || !did {
		t.Fatalf("did=%v err=%v", did, err)
	}
	if gotBody["grant_type"] != "refresh_token" || gotBody["refresh_token"] != "old-refresh" || gotBody["client_id"] != oauthClientID {
		t.Fatalf("request body %v", gotBody)
	}
	if c.AccessToken != "new-access" || c.RefreshToken != "new-refresh" || c.SubscriptionType != "max" {
		t.Fatalf("creds %+v", c)
	}
	raw, _ := os.ReadFile(path)
	var doc map[string]any
	json.Unmarshal(raw, &doc)
	oauth := doc["claudeAiOauth"].(map[string]any)
	if oauth["accessToken"] != "new-access" || oauth["refreshToken"] != "new-refresh" || oauth["subscriptionType"] != "max" {
		t.Fatalf("file not updated correctly: %v", oauth)
	}
	if _, ok := doc["somethingElse"]; !ok {
		t.Fatal("unrelated keys must survive")
	}
	exp := time.UnixMilli(int64(oauth["expiresAt"].(float64)))
	if d := exp.Sub(now); d < 7*time.Hour || d > 9*time.Hour {
		t.Fatalf("expiresAt should be ~8h out, got %v", d)
	}
}

func TestRefreshSkippedWhenTokenIsFresh(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	oauthTokenURL = srv.URL
	now := time.Now()
	path := writeCreds(t, now.Add(6*time.Hour).UnixMilli())
	c, did, err := refreshIfNeeded(path, false, now)
	if err != nil || did || hits != 0 || c.AccessToken != "old-access" {
		t.Fatalf("did=%v hits=%d err=%v creds=%+v", did, hits, err, c)
	}
}

func TestRefreshFailureKeepsFileIntact(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer srv.Close()
	oauthTokenURL = srv.URL
	now := time.Now()
	path := writeCreds(t, now.Add(time.Minute).UnixMilli())
	before, _ := os.ReadFile(path)
	if _, did, err := refreshIfNeeded(path, false, now); err == nil || did {
		t.Fatal("a 401 must surface as an error")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("file must not change on failure")
	}
}
