package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// newTestServer wires CheckHandler + ConfigHandler + HealthHandler to a
// miniredis-backed httptest server.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	rdb, _ := newTestRedis(t) // defined in limiter_test.go

	mux := http.NewServeMux()
	mux.HandleFunc("/check", CheckHandler(rdb, nil))
	mux.HandleFunc("/config", ConfigHandler(rdb))
	mux.HandleFunc("/health", HealthHandler(rdb, nil, nil))

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
	reset := resp.Header.Get("X-RateLimit-Reset")
	if reset == "" {
		t.Error("X-RateLimit-Reset missing on 200 response")
	} else {
		n, err := strconv.ParseInt(reset, 10, 64)
		if err != nil {
			t.Errorf("X-RateLimit-Reset=%q not a unix timestamp", reset)
		} else if n <= time.Now().Unix() {
			t.Errorf("X-RateLimit-Reset=%d, want a future timestamp", n)
		}
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
	if lastResp.Header.Get("X-RateLimit-Reset") == "" {
		t.Error("11th: missing X-RateLimit-Reset header")
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
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" {
		t.Errorf("status=%q, want ok", body["status"])
	}
	if body["redis"] != "connected" {
		t.Errorf("redis=%q, want connected", body["redis"])
	}
	if body["postgres"] != "disabled" {
		t.Errorf("postgres=%q, want disabled", body["postgres"])
	}
	if _, ok := body["events_dropped"].(float64); !ok {
		t.Errorf("events_dropped missing or wrong type: %v", body["events_dropped"])
	}
}

// ----- DAY 3: CONFIG ENDPOINTS -----

func TestConfigPutThenGet_QueryParams(t *testing.T) {
	srv := newTestServer(t)
	c := srv.Client()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut,
		srv.URL+"/config?key=user-1&algorithm=fixed&limit=5&window=60", nil)
	if err != nil {
		t.Fatal(err)
	}
	putResp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer putResp.Body.Close()
	if putResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(putResp.Body)
		t.Fatalf("PUT status=%d body=%s, want 200", putResp.StatusCode, body)
	}

	var stored LimitConfig
	if err := json.NewDecoder(putResp.Body).Decode(&stored); err != nil {
		t.Fatal(err)
	}
	if stored.Algorithm != "fixed" || stored.Limit != 5 || stored.Window != 60 {
		t.Errorf("PUT echo=%+v, want {fixed, 5, 60}", stored)
	}

	getResp, err := c.Get(srv.URL + "/config?key=user-1")
	if err != nil {
		t.Fatal(err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Errorf("GET status=%d, want 200", getResp.StatusCode)
	}
	var fetched LimitConfig
	if err := json.NewDecoder(getResp.Body).Decode(&fetched); err != nil {
		t.Fatal(err)
	}
	if fetched != stored {
		t.Errorf("GET=%+v, want %+v (round-trip mismatch)", fetched, stored)
	}
}

func TestConfigPut_JSONBody(t *testing.T) {
	srv := newTestServer(t)
	c := srv.Client()

	body := strings.NewReader(`{"algorithm":"token","capacity":7,"refill":2.5}`)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut,
		srv.URL+"/config?key=user-2", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s, want 200", resp.StatusCode, b)
	}
	var stored LimitConfig
	if err := json.NewDecoder(resp.Body).Decode(&stored); err != nil {
		t.Fatal(err)
	}
	if stored.Algorithm != "token" || stored.Capacity != 7 || stored.Refill != 2.5 {
		t.Errorf("stored=%+v, want {token, 7, 2.5}", stored)
	}
}

func TestConfigOverridesCheckQueryParams(t *testing.T) {
	srv := newTestServer(t)
	c := srv.Client()

	// Store a config with limit=2.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPut,
		srv.URL+"/config?key=ovr&algorithm=fixed&limit=2&window=60", nil)
	putResp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status=%d, want 200", putResp.StatusCode)
	}

	// Now /check with query limit=999 should still be capped by stored limit=2.
	post := func() *http.Response {
		r, err := c.Post(srv.URL+"/check?key=ovr&algorithm=fixed&limit=999&window=60", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	for i := 1; i <= 2; i++ {
		r := post()
		if r.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(r.Body)
			r.Body.Close()
			t.Fatalf("req %d: status=%d body=%s, want 200", i, r.StatusCode, b)
		}
		if got := r.Header.Get("X-RateLimit-Limit"); got != "2" {
			r.Body.Close()
			t.Errorf("req %d: X-RateLimit-Limit=%q, want 2 (from stored config)", i, got)
		}
		r.Body.Close()
	}

	r := post()
	defer r.Body.Close()
	if r.StatusCode != http.StatusTooManyRequests {
		body, _ := io.ReadAll(r.Body)
		t.Errorf("3rd: status=%d body=%s, want 429 (stored limit=2 should override query)",
			r.StatusCode, body)
	}
}

func TestConfigGet_NotFound(t *testing.T) {
	srv := newTestServer(t)
	resp, err := srv.Client().Get(srv.URL + "/config?key=does-not-exist")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status=%d, want 404", resp.StatusCode)
	}
	var body ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Error == "" {
		t.Error("expected JSON error message")
	}
}

func TestConfigPut_ValidationErrors(t *testing.T) {
	srv := newTestServer(t)
	c := srv.Client()

	cases := []struct {
		name string
		url  string
		body string
	}{
		{
			name: "unknown algorithm",
			url:  srv.URL + "/config?key=v1&algorithm=nope&limit=5&window=60",
		},
		{
			name: "zero limit",
			url:  srv.URL + "/config?key=v2&algorithm=fixed&limit=0&window=60",
		},
		{
			name: "negative window",
			url:  srv.URL + "/config?key=v3&algorithm=sliding&limit=5&window=-1",
		},
		{
			name: "missing capacity for token",
			url:  srv.URL + "/config?key=v4&algorithm=token&refill=1",
		},
		{
			name: "non-positive refill",
			url:  srv.URL + "/config?key=v5&algorithm=token&capacity=5&refill=0",
		},
		{
			name: "bad json body",
			url:  srv.URL + "/config?key=v6",
			body: `{"algorithm":`,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var req *http.Request
			var err error
			if tc.body != "" {
				req, err = http.NewRequestWithContext(context.Background(), http.MethodPut,
					tc.url, bytes.NewBufferString(tc.body))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", "application/json")
			} else {
				req, err = http.NewRequestWithContext(context.Background(), http.MethodPut, tc.url, nil)
				if err != nil {
					t.Fatal(err)
				}
			}
			resp, err := c.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				body, _ := io.ReadAll(resp.Body)
				t.Errorf("status=%d body=%s, want 400", resp.StatusCode, body)
			}
			var e ErrorResponse
			if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
				t.Errorf("response not JSON: %v", err)
			}
			if e.Error == "" {
				t.Error("expected non-empty JSON error message")
			}
		})
	}
}

func TestConfigMissingKey(t *testing.T) {
	srv := newTestServer(t)
	c := srv.Client()

	resp, err := c.Get(srv.URL + "/config")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", resp.StatusCode)
	}
}

func TestConfigWrongMethod(t *testing.T) {
	srv := newTestServer(t)
	resp, err := srv.Client().Post(srv.URL+"/config?key=x", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status=%d, want 405", resp.StatusCode)
	}
}
