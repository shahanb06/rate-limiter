package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeStore is a hand-rolled AnalyticsStore for handler tests. Each method's
// behavior is configurable: canned return value or injected error.
type fakeStore struct {
	listKeysRet []string
	listKeysErr error

	summaryRet     SummaryRow
	summaryPrevRet SummaryRow
	summaryErr     error
	summaryCalls   int

	tsPoints []TimeseriesPoint
	tsErr    error

	summaryByAlgoRet []SummaryByAlgoRow
	summaryByAlgoErr error

	tsByAlgoPoints []TimeseriesAlgoPoint
	tsByAlgoErr    error

	leaderboardRet []LeaderboardRow
	leaderboardErr error

	recentBucketsRet map[string][]LeaderboardSparklinePoint
	recentBucketsErr error

	// Captured inputs from the last call (for parameter-validation assertions).
	gotSummaryKey         string
	gotSummarySince       time.Time
	gotSummaryUntil       time.Time
	gotTSKey              string
	gotTSSince            time.Time
	gotSummaryByAlgoKey   string
	gotTSByAlgoKey        string
	gotTSByAlgoSince      time.Time
	gotRecentBucketsSince time.Time
}

func (f *fakeStore) ListKeys(ctx context.Context) ([]string, error) {
	return f.listKeysRet, f.listKeysErr
}
func (f *fakeStore) Summary(ctx context.Context, key string, since, until time.Time) (SummaryRow, error) {
	f.summaryCalls++
	if f.summaryCalls == 1 {
		f.gotSummaryKey = key
		f.gotSummarySince = since
		f.gotSummaryUntil = until
		return f.summaryRet, f.summaryErr
	}
	return f.summaryPrevRet, f.summaryErr
}
func (f *fakeStore) Timeseries(ctx context.Context, key string, since time.Time) ([]TimeseriesPoint, error) {
	f.gotTSKey = key
	f.gotTSSince = since
	return f.tsPoints, f.tsErr
}
func (f *fakeStore) SummaryByAlgorithm(ctx context.Context, key string) ([]SummaryByAlgoRow, error) {
	f.gotSummaryByAlgoKey = key
	return f.summaryByAlgoRet, f.summaryByAlgoErr
}
func (f *fakeStore) TimeseriesByAlgorithm(ctx context.Context, key string, since time.Time) ([]TimeseriesAlgoPoint, error) {
	f.gotTSByAlgoKey = key
	f.gotTSByAlgoSince = since
	return f.tsByAlgoPoints, f.tsByAlgoErr
}
func (f *fakeStore) Leaderboard(ctx context.Context) ([]LeaderboardRow, error) {
	return f.leaderboardRet, f.leaderboardErr
}
func (f *fakeStore) RecentBucketsByKey(ctx context.Context, since time.Time) (map[string][]LeaderboardSparklinePoint, error) {
	f.gotRecentBucketsSince = since
	return f.recentBucketsRet, f.recentBucketsErr
}

// ----- /analytics/keys -----

func TestAnalyticsKeys(t *testing.T) {
	store := &fakeStore{listKeysRet: []string{"alice", "bob"}}
	srv := httptest.NewServer(AnalyticsKeysHandler(store))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	var body struct {
		Keys []string `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Keys) != 2 || body.Keys[0] != "alice" || body.Keys[1] != "bob" {
		t.Errorf("keys=%v, want [alice bob]", body.Keys)
	}
}

func TestAnalyticsKeys_NilStore503(t *testing.T) {
	srv := httptest.NewServer(AnalyticsKeysHandler(nil))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status=%d, want 503", resp.StatusCode)
	}
}

func TestAnalyticsKeys_StoreError503(t *testing.T) {
	store := &fakeStore{listKeysErr: errors.New("db gone")}
	srv := httptest.NewServer(AnalyticsKeysHandler(store))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status=%d, want 503", resp.StatusCode)
	}
}

// ----- /analytics/summary -----

func TestAnalyticsSummary(t *testing.T) {
	store := &fakeStore{summaryRet: SummaryRow{Allowed: 80, Rejected: 20, Total: 100}}
	srv := httptest.NewServer(AnalyticsSummaryHandler(store))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "?key=alice")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["key"] != "alice" {
		t.Errorf("key=%v, want alice", body["key"])
	}
	if body["allowed"].(float64) != 80 {
		t.Errorf("allowed=%v, want 80", body["allowed"])
	}
	if body["rejected"].(float64) != 20 {
		t.Errorf("rejected=%v, want 20", body["rejected"])
	}
	if body["total"].(float64) != 100 {
		t.Errorf("total=%v, want 100", body["total"])
	}
	if rate := body["rejection_rate"].(float64); rate < 0.199 || rate > 0.201 {
		t.Errorf("rejection_rate=%v, want ~0.2", rate)
	}
	if store.gotSummaryKey != "alice" {
		t.Errorf("store got key=%q, want alice", store.gotSummaryKey)
	}
}

func TestAnalyticsSummary_ZeroTotalRateIsZero(t *testing.T) {
	store := &fakeStore{summaryRet: SummaryRow{}}
	srv := httptest.NewServer(AnalyticsSummaryHandler(store))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "?key=nobody")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["rejection_rate"].(float64) != 0 {
		t.Errorf("rejection_rate=%v, want 0 (no division by zero)", body["rejection_rate"])
	}
}

func TestAnalyticsSummary_MissingKey400(t *testing.T) {
	store := &fakeStore{}
	srv := httptest.NewServer(AnalyticsSummaryHandler(store))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", resp.StatusCode)
	}
}

func TestAnalyticsSummary_NilStore503(t *testing.T) {
	srv := httptest.NewServer(AnalyticsSummaryHandler(nil))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "?key=alice")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status=%d, want 503", resp.StatusCode)
	}
}

// ----- /analytics/timeseries -----

func TestAnalyticsTimeseries(t *testing.T) {
	bucket := time.Date(2026, 6, 6, 21, 15, 0, 0, time.UTC)
	store := &fakeStore{tsPoints: []TimeseriesPoint{
		{BucketStart: bucket, Allowed: 3, Rejected: 2, Total: 5},
	}}
	srv := httptest.NewServer(AnalyticsTimeseriesHandler(store))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "?key=alice&since=30m")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	var body struct {
		Key    string            `json:"key"`
		Since  string            `json:"since"`
		Points []TimeseriesPoint `json:"points"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Key != "alice" {
		t.Errorf("key=%q, want alice", body.Key)
	}
	if len(body.Points) != 1 || body.Points[0].Total != 5 {
		t.Errorf("points=%v", body.Points)
	}
	if store.gotTSKey != "alice" {
		t.Errorf("store got key=%q", store.gotTSKey)
	}
	// 30m before now, give or take a couple of seconds.
	diff := time.Since(store.gotTSSince)
	if diff < 29*time.Minute || diff > 31*time.Minute {
		t.Errorf("since=%v, want ~30m ago", diff)
	}
}

func TestAnalyticsTimeseries_DefaultSinceIsOneHour(t *testing.T) {
	store := &fakeStore{}
	srv := httptest.NewServer(AnalyticsTimeseriesHandler(store))
	t.Cleanup(srv.Close)

	if _, err := http.Get(srv.URL + "?key=alice"); err != nil {
		t.Fatal(err)
	}
	diff := time.Since(store.gotTSSince)
	if diff < 59*time.Minute || diff > 61*time.Minute {
		t.Errorf("default since=%v, want ~1h ago", diff)
	}
}

func TestAnalyticsTimeseries_RFC3339Since(t *testing.T) {
	store := &fakeStore{}
	srv := httptest.NewServer(AnalyticsTimeseriesHandler(store))
	t.Cleanup(srv.Close)

	want := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if _, err := http.Get(srv.URL + "?key=alice&since=" + want.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if !store.gotTSSince.Equal(want) {
		t.Errorf("since=%v, want %v", store.gotTSSince, want)
	}
}

func TestAnalyticsTimeseries_BadSince400(t *testing.T) {
	store := &fakeStore{}
	srv := httptest.NewServer(AnalyticsTimeseriesHandler(store))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "?key=alice&since=not-a-duration")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", resp.StatusCode)
	}
}

func TestAnalyticsTimeseries_MissingKey400(t *testing.T) {
	store := &fakeStore{}
	srv := httptest.NewServer(AnalyticsTimeseriesHandler(store))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", resp.StatusCode)
	}
}

func TestAnalyticsTimeseries_NilStore503(t *testing.T) {
	srv := httptest.NewServer(AnalyticsTimeseriesHandler(nil))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "?key=alice")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status=%d, want 503", resp.StatusCode)
	}
}

// ----- /check is untouched by analytics wiring -----

// Confirms that adding the analytics routes (with a nil store) to the same mux
// does not regress /check. /check must work whether or not Postgres is wired.
func TestCheckUnchangedByAnalyticsWiring(t *testing.T) {
	rdb, _ := newTestRedis(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/check", CheckHandler(rdb, nil))
	mux.HandleFunc("/analytics/keys", AnalyticsKeysHandler(nil))
	mux.HandleFunc("/analytics/summary", AnalyticsSummaryHandler(nil))
	mux.HandleFunc("/analytics/timeseries", AnalyticsTimeseriesHandler(nil))
	mux.HandleFunc("/analytics/leaderboard", AnalyticsLeaderboardHandler(nil))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/check?key=t&algorithm=fixed&limit=2&window=60", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/check status=%d, want 200", resp.StatusCode)
	}

	// Same key should still get a 429 on its 3rd hit — proves limiter state is real.
	for i := 0; i < 2; i++ {
		r, err := http.Post(srv.URL+"/check?key=t&algorithm=fixed&limit=2&window=60", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if i == 1 && r.StatusCode != http.StatusTooManyRequests {
			t.Errorf("3rd /check status=%d, want 429", r.StatusCode)
		}
	}

	// And the nil-store analytics endpoint should be 503 on the same mux.
	r, err := http.Get(srv.URL + "/analytics/keys")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("/analytics/keys with nil store status=%d, want 503", r.StatusCode)
	}

	// Same for leaderboard: nil store on the mux → 503.
	r, err = http.Get(srv.URL + "/analytics/leaderboard")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("/analytics/leaderboard with nil store status=%d, want 503", r.StatusCode)
	}
}

// ----- group_by=algorithm branches -----

// Default (no group_by) must keep the Day 7/8 shape: top-level allowed,
// rejected, total, rejection_rate — no by_algorithm field. This is the
// invariant the Day 8 dashboard relies on.
func TestAnalyticsSummary_DefaultShapeUnchanged(t *testing.T) {
	store := &fakeStore{summaryRet: SummaryRow{Allowed: 7, Rejected: 3, Total: 10}}
	srv := httptest.NewServer(AnalyticsSummaryHandler(store))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "?key=alice")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if _, present := body["by_algorithm"]; present {
		t.Error("default response must not include by_algorithm")
	}
	if body["allowed"].(float64) != 7 || body["rejected"].(float64) != 3 || body["total"].(float64) != 10 {
		t.Errorf("default shape regressed: %v", body)
	}
}

func TestAnalyticsSummary_Delta(t *testing.T) {
	store := &fakeStore{
		summaryRet:     SummaryRow{Allowed: 30, Rejected: 90, Total: 120},
		summaryPrevRet: SummaryRow{Allowed: 20, Rejected: 45, Total: 75},
	}
	srv := httptest.NewServer(AnalyticsSummaryHandler(store))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "?key=alice&window=1h")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	var body struct {
		Total    int64 `json:"total"`
		Previous struct {
			Total int64 `json:"total"`
		} `json:"previous"`
		Delta struct {
			TotalPct *float64 `json:"total_pct"`
		} `json:"delta"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Total != 120 {
		t.Errorf("top-level total=%d, want 120 (current window)", body.Total)
	}
	if body.Previous.Total != 75 {
		t.Errorf("previous.total=%d, want 75", body.Previous.Total)
	}
	if body.Delta.TotalPct == nil {
		t.Fatalf("delta.total_pct is nil, want ~60.0")
	}
	if d := *body.Delta.TotalPct; d < 59.95 || d > 60.05 {
		t.Errorf("delta.total_pct=%v, want ~60.0", d)
	}
}

func TestAnalyticsSummary_DeltaNullWhenNoPrevious(t *testing.T) {
	store := &fakeStore{
		summaryRet: SummaryRow{Allowed: 10, Rejected: 5, Total: 15},
		// summaryPrevRet defaults to zero — no traffic in the previous window.
	}
	srv := httptest.NewServer(AnalyticsSummaryHandler(store))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "?key=alice&window=1h")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	var body struct {
		Delta struct {
			TotalPct *float64 `json:"total_pct"`
		} `json:"delta"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Delta.TotalPct != nil {
		t.Errorf("delta.total_pct=%v, want nil when previous window had zero total", *body.Delta.TotalPct)
	}
}

func TestAnalyticsSummary_GroupByAlgorithm(t *testing.T) {
	store := &fakeStore{summaryByAlgoRet: []SummaryByAlgoRow{
		{Algorithm: "fixed", Allowed: 40, Rejected: 10, Total: 50},
		{Algorithm: "sliding", Allowed: 18, Rejected: 2, Total: 20},
		{Algorithm: "token", Allowed: 0, Rejected: 0, Total: 0},
	}}
	srv := httptest.NewServer(AnalyticsSummaryHandler(store))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "?key=alice&group_by=algorithm")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	var body struct {
		Key         string `json:"key"`
		ByAlgorithm []struct {
			Algorithm     string  `json:"algorithm"`
			Allowed       int64   `json:"allowed"`
			Rejected      int64   `json:"rejected"`
			Total         int64   `json:"total"`
			RejectionRate float64 `json:"rejection_rate"`
		} `json:"by_algorithm"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Key != "alice" {
		t.Errorf("key=%q, want alice", body.Key)
	}
	if len(body.ByAlgorithm) != 3 {
		t.Fatalf("by_algorithm len=%d, want 3", len(body.ByAlgorithm))
	}
	if body.ByAlgorithm[0].Algorithm != "fixed" || body.ByAlgorithm[0].Total != 50 {
		t.Errorf("first row=%+v", body.ByAlgorithm[0])
	}
	if r := body.ByAlgorithm[0].RejectionRate; r < 0.199 || r > 0.201 {
		t.Errorf("fixed rejection_rate=%v, want ~0.2", r)
	}
	// Zero-total algorithm must report rate 0, not NaN.
	if body.ByAlgorithm[2].RejectionRate != 0 {
		t.Errorf("zero-total rejection_rate=%v, want 0", body.ByAlgorithm[2].RejectionRate)
	}
	if store.gotSummaryByAlgoKey != "alice" {
		t.Errorf("store got key=%q", store.gotSummaryByAlgoKey)
	}
}

func TestAnalyticsSummary_GroupByGarbage400(t *testing.T) {
	store := &fakeStore{}
	srv := httptest.NewServer(AnalyticsSummaryHandler(store))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "?key=alice&group_by=key")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", resp.StatusCode)
	}
}

// Default timeseries (no group_by) must keep the Day 7/8 shape: points without
// the `algorithm` field. The Day 8 chart depends on this.
func TestAnalyticsTimeseries_DefaultShapeUnchanged(t *testing.T) {
	bucket := time.Date(2026, 6, 6, 21, 15, 0, 0, time.UTC)
	store := &fakeStore{tsPoints: []TimeseriesPoint{
		{BucketStart: bucket, Allowed: 3, Rejected: 2, Total: 5},
	}}
	srv := httptest.NewServer(AnalyticsTimeseriesHandler(store))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "?key=alice")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	points, ok := body["points"].([]any)
	if !ok || len(points) != 1 {
		t.Fatalf("points=%v", body["points"])
	}
	first := points[0].(map[string]any)
	if _, present := first["algorithm"]; present {
		t.Error("default timeseries row must not include algorithm field")
	}
}

func TestAnalyticsTimeseries_GroupByAlgorithm(t *testing.T) {
	bucket := time.Date(2026, 6, 6, 21, 15, 0, 0, time.UTC)
	store := &fakeStore{tsByAlgoPoints: []TimeseriesAlgoPoint{
		{BucketStart: bucket, Algorithm: "fixed", Allowed: 4, Rejected: 1, Total: 5},
		{BucketStart: bucket, Algorithm: "sliding", Allowed: 2, Rejected: 0, Total: 2},
	}}
	srv := httptest.NewServer(AnalyticsTimeseriesHandler(store))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "?key=alice&group_by=algorithm&since=30m")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	var body struct {
		Key    string                `json:"key"`
		Points []TimeseriesAlgoPoint `json:"points"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Points) != 2 {
		t.Fatalf("points len=%d, want 2", len(body.Points))
	}
	if body.Points[0].Algorithm != "fixed" || body.Points[0].Total != 5 {
		t.Errorf("first=%+v", body.Points[0])
	}
	if body.Points[1].Algorithm != "sliding" {
		t.Errorf("second=%+v", body.Points[1])
	}
	if store.gotTSByAlgoKey != "alice" {
		t.Errorf("store got key=%q", store.gotTSByAlgoKey)
	}
}

func TestAnalyticsTimeseries_GroupByGarbage400(t *testing.T) {
	store := &fakeStore{}
	srv := httptest.NewServer(AnalyticsTimeseriesHandler(store))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "?key=alice&group_by=key")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", resp.StatusCode)
	}
}

// ----- /analytics/leaderboard -----

func TestAnalyticsLeaderboard(t *testing.T) {
	store := &fakeStore{leaderboardRet: []LeaderboardRow{
		{Key: "bob", Allowed: 800, Rejected: 200, Total: 1000},
		{Key: "alice", Allowed: 70, Rejected: 30, Total: 100},
		{Key: "ghost", Allowed: 0, Rejected: 0, Total: 0},
	}}
	srv := httptest.NewServer(AnalyticsLeaderboardHandler(store))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	var body struct {
		Rows []struct {
			Key           string  `json:"key"`
			Allowed       int64   `json:"allowed"`
			Rejected      int64   `json:"rejected"`
			Total         int64   `json:"total"`
			RejectionRate float64 `json:"rejection_rate"`
		} `json:"rows"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Rows) != 3 {
		t.Fatalf("rows len=%d, want 3", len(body.Rows))
	}
	// Order is preserved from the store (which is supposed to order by total
	// desc); handler must not re-sort.
	if body.Rows[0].Key != "bob" || body.Rows[0].Total != 1000 {
		t.Errorf("first row=%+v", body.Rows[0])
	}
	if r := body.Rows[0].RejectionRate; r < 0.199 || r > 0.201 {
		t.Errorf("bob rejection_rate=%v, want ~0.2", r)
	}
	// Zero-total → rate 0, never NaN (which would not survive JSON encoding).
	if body.Rows[2].RejectionRate != 0 {
		t.Errorf("ghost rejection_rate=%v, want 0", body.Rows[2].RejectionRate)
	}
}

func TestAnalyticsLeaderboard_Empty(t *testing.T) {
	store := &fakeStore{leaderboardRet: []LeaderboardRow{}}
	srv := httptest.NewServer(AnalyticsLeaderboardHandler(store))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	var body struct {
		Rows []map[string]any `json:"rows"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Rows == nil {
		t.Error("rows must be an empty array, not null")
	}
	if len(body.Rows) != 0 {
		t.Errorf("rows len=%d, want 0", len(body.Rows))
	}
}

func TestAnalyticsLeaderboard_NilStore503(t *testing.T) {
	srv := httptest.NewServer(AnalyticsLeaderboardHandler(nil))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status=%d, want 503", resp.StatusCode)
	}
}

func TestAnalyticsLeaderboard_StoreError503(t *testing.T) {
	store := &fakeStore{leaderboardErr: errors.New("db gone")}
	srv := httptest.NewServer(AnalyticsLeaderboardHandler(store))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status=%d, want 503", resp.StatusCode)
	}
}

func TestAnalyticsLeaderboard_EmbedsSparklines(t *testing.T) {
	store := &fakeStore{
		leaderboardRet: []LeaderboardRow{
			{Key: "alice", Allowed: 70, Rejected: 30, Total: 100},
			{Key: "bob", Allowed: 800, Rejected: 200, Total: 1000},
		},
		recentBucketsRet: map[string][]LeaderboardSparklinePoint{
			"alice": {
				{Allowed: 10, Rejected: 0, Total: 10},
				{Allowed: 15, Rejected: 5, Total: 20},
			},
			// bob has no recent buckets — handler should encode it as null.
		},
	}
	srv := httptest.NewServer(AnalyticsLeaderboardHandler(store))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	var body struct {
		Rows []struct {
			Key       string                      `json:"key"`
			Sparkline []LeaderboardSparklinePoint `json:"sparkline"`
		} `json:"rows"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	byKey := map[string][]LeaderboardSparklinePoint{}
	for _, r := range body.Rows {
		byKey[r.Key] = r.Sparkline
	}
	alice, ok := byKey["alice"]
	if !ok {
		t.Fatalf("missing alice row: %+v", body.Rows)
	}
	if len(alice) != 2 {
		t.Fatalf("alice sparkline len=%d, want 2", len(alice))
	}
	if alice[1].Total != 20 || alice[1].Allowed != 15 || alice[1].Rejected != 5 {
		t.Errorf("alice sparkline[1]=%+v, want {15 5 20}", alice[1])
	}
	if bob, ok := byKey["bob"]; !ok || bob != nil {
		t.Errorf("bob sparkline=%v, want nil (key missing from sparkline map)", bob)
	}

	// Handler must request roughly the last hour. Allow slop for test latency.
	ago := time.Since(store.gotRecentBucketsSince)
	if ago < 50*time.Minute || ago > 70*time.Minute {
		t.Errorf("RecentBucketsByKey since=%v (now-%v), want ~now-1h", store.gotRecentBucketsSince, ago)
	}
}

func TestAnalyticsLeaderboard_WindowParam(t *testing.T) {
	// Explicit window=24h widens the sparkline lookback to 24h.
	store := &fakeStore{
		leaderboardRet: []LeaderboardRow{{Key: "alice", Total: 1}},
	}
	srv := httptest.NewServer(AnalyticsLeaderboardHandler(store))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "?window=24h")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	ago := time.Since(store.gotRecentBucketsSince)
	if ago < 23*time.Hour+50*time.Minute || ago > 24*time.Hour+10*time.Minute {
		t.Errorf("with window=24h, since=now-%v, want ~now-24h", ago)
	}
}

func TestAnalyticsLeaderboard_InvalidWindow400(t *testing.T) {
	store := &fakeStore{leaderboardRet: []LeaderboardRow{{Key: "alice", Total: 1}}}
	srv := httptest.NewServer(AnalyticsLeaderboardHandler(store))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "?window=garbage")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "window") {
		t.Errorf("error body=%q, want it to mention the param name 'window'", string(body))
	}
}

// ----- parseSince unit -----

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		in      string
		want    time.Time
		wantErr bool
	}{
		{"", now.Add(-time.Hour), false},
		{"1h", now.Add(-time.Hour), false},
		{"30m", now.Add(-30 * time.Minute), false},
		{"-1h", now.Add(-time.Hour), false}, // negative duration normalized
		{"2026-06-01T00:00:00Z", time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), false},
		{"garbage", time.Time{}, true},
	}
	for _, c := range cases {
		got, err := parseSince(c.in, now, "since")
		if (err != nil) != c.wantErr {
			t.Errorf("parseSince(%q): err=%v, wantErr=%v", c.in, err, c.wantErr)
		}
		if !c.wantErr && !got.Equal(c.want) {
			t.Errorf("parseSince(%q): got %v, want %v", c.in, got, c.want)
		}
	}
}
