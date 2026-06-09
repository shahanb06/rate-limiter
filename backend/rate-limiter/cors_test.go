package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCORSAppliedToAnalytics verifies that wrapping a handler with the CORS
// middleware adds the expected headers to GET responses.
func TestCORSAppliedToAnalytics(t *testing.T) {
	store := &fakeStore{listKeysRet: []string{"k1"}}
	t.Setenv("ENV", "dev")
	cors := NewCORS()
	srv := httptest.NewServer(cors(AnalyticsKeysHandler(store)))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin=%q, want %q", got, "*")
	}
	if got := resp.Header.Get("Access-Control-Allow-Methods"); got == "" {
		t.Error("Access-Control-Allow-Methods missing on analytics response")
	}
}

// TestCORSPreflight verifies that OPTIONS requests short-circuit with 204 and
// carry the CORS headers (so the browser allows the follow-up GET).
func TestCORSPreflight(t *testing.T) {
	t.Setenv("ENV", "dev")
	cors := NewCORS()
	// Inner handler should never be called for OPTIONS.
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("inner handler called on preflight; want 204 short-circuit")
	})
	srv := httptest.NewServer(cors(inner))
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodOptions, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("OPTIONS status=%d, want 204", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin=%q on preflight, want %q", got, "*")
	}
}

// TestCORSNotAppliedToCheck verifies that /check responses do NOT carry the
// CORS header — the rate limiter is server-to-server, browsers should not be
// able to call it cross-origin.
func TestCORSNotAppliedToCheck(t *testing.T) {
	rdb, _ := newTestRedis(t)
	t.Setenv("ENV", "dev")
	cors := NewCORS()

	mux := http.NewServeMux()
	mux.HandleFunc("/check", CheckHandler(rdb, nil))
	mux.Handle("/analytics/keys", cors(AnalyticsKeysHandler(&fakeStore{})))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/check?key=k&algorithm=fixed&limit=5&window=60", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("/check carries Access-Control-Allow-Origin=%q, want empty", got)
	}
}
