package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestServer wires CheckHandler + HealthHandler to a miniredis-backed
// httptest server.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	rdb, _ := newTestRedis(t) // defined in limiter_test.go

	mux := http.NewServeMux()
	mux.HandleFunc("/check", CheckHandler(rdb))
	mux.HandleFunc("/health", HealthHandler(rdb))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestHandlerCheckAllowed(t *testing.T) {
	srv := newTestServer(t)

	resp, err := http.Post(srv.URL+"/check?key=t&algorithm=fixed&limit=10&window=60", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status=%d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("X-RateLimit-Limit"); got != "10" {
		t.Errorf("X-RateLimit-Limit=%q, want 10", got)
	}
	if got := resp.Header.Get("X-RateLimit-Remaining"); got != "9" {
		t.Errorf("X-RateLimit-Remaining=%q, want 9", got)
	}
	if got := resp.Header.Get("Retry-After"); got != "" {
		t.Errorf("Retry-After=%q on allowed request, want empty", got)
	}

	var body CheckResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.Allowed || body.Remaining != 9 || body.RetryAfter != 0 ||
		body.Algorithm != "fixed" || body.Key != "t" {
		t.Errorf("body=%+v", body)
	}
}

func TestHandlerCheckBlocked(t *testing.T) {
	srv := newTestServer(t)

	var lastResp *http.Response
	for i := 1; i <= 11; i++ {
		resp, err := http.Post(srv.URL+"/check?key=blk&algorithm=fixed&limit=10&window=60", "", nil)
		if err != nil {
			t.Fatalf("req %d: %v", i, err)
		}
		if i < 11 {
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("req %d: status=%d, want 200", i, resp.StatusCode)
			}
			continue
		}
		lastResp = resp
	}
	defer lastResp.Body.Close()

	if lastResp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("11th: status=%d, want 429", lastResp.StatusCode)
	}
	if lastResp.Header.Get("Retry-After") == "" {
		t.Error("11th: missing Retry-After header")
	}
	if got := lastResp.Header.Get("X-RateLimit-Remaining"); got != "0" {
		t.Errorf("11th: X-RateLimit-Remaining=%q, want 0", got)
	}

	var body CheckResponse
	if err := json.NewDecoder(lastResp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Allowed {
		t.Error("11th: body.Allowed=true, want false")
	}
	if body.RetryAfter <= 0 {
		t.Errorf("11th: body.RetryAfter=%d, want >0", body.RetryAfter)
	}
}

func TestHandlerMissingKey(t *testing.T) {
	srv := newTestServer(t)

	resp, err := http.Post(srv.URL+"/check?limit=10", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", resp.StatusCode)
	}
	var body ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body.Error, "key") {
		t.Errorf("error=%q, want message about 'key'", body.Error)
	}
}

func TestHandlerUnknownAlgorithm(t *testing.T) {
	srv := newTestServer(t)

	resp, err := http.Post(srv.URL+"/check?key=x&algorithm=bogus", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "algorithm must be") {
		t.Errorf("body=%s, want it to mention valid algorithms", b)
	}
}

func TestHandlerWrongMethod(t *testing.T) {
	srv := newTestServer(t)

	resp, err := http.Get(srv.URL + "/check?key=x")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status=%d, want 405", resp.StatusCode)
	}
}

func TestHandlerHealth(t *testing.T) {
	srv := newTestServer(t)

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status=%d, want 200", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" {
		t.Errorf("status=%q, want ok", body["status"])
	}
	if body["redis"] != "connected" {
		t.Errorf("redis=%q, want connected", body["redis"])
	}
}
