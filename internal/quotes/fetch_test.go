package quotes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestFetcher points a Fetcher at a stub server by rewriting the request
// host, so the full fetch-and-parse path is exercised without touching
// Booking.com.
func newTestFetcher(srv *httptest.Server) *Fetcher {
	f := NewFetcher()
	base := srv.Client().Transport
	f.client = srv.Client()
	f.client.Transport = rewriteHost{base: base, target: srv.URL}
	return f
}

type rewriteHost struct {
	base   http.RoundTripper
	target string
}

func (r rewriteHost) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(r.target, "http://")
	return r.base.RoundTrip(req)
}

var lisboa = Spec{
	ID: "lisboa", Label: "Sana Rex Hotel Lisboa", City: "Lisboa",
	Checkin: "2026-10-15", Checkout: "2026-10-17", Adults: 2, Rooms: 1,
}

func TestFetchReturnsPrice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("group_adults"); got != "2" {
			t.Errorf("group_adults = %q, want 2", got)
		}
		w.Write([]byte(`<html><body><div data-testid="price-and-discounted-price">R$ 2.480</div></body></html>`))
	}))
	defer srv.Close()

	q := newTestFetcher(srv).Fetch(context.Background(), lisboa)
	if q.Err != "" {
		t.Fatalf("unexpected error: %s", q.Err)
	}
	if q.Price != "R$ 2.480" {
		t.Errorf("Price = %q, want R$ 2.480", q.Price)
	}
	if q.Nights != 2 {
		t.Errorf("Nights = %d, want 2", q.Nights)
	}
	if q.Ts == 0 {
		t.Error("Ts should be set")
	}
	// The cached URL must stay clickable so the page can fall back to it.
	if !strings.Contains(q.URL, "booking.com") {
		t.Errorf("URL = %q, want a booking.com link", q.URL)
	}
}

func TestFetchReportsBlockedPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><body><div id="px-captcha">verify</div></body></html>`))
	}))
	defer srv.Close()

	q := newTestFetcher(srv).Fetch(context.Background(), lisboa)
	if q.Price != "" {
		t.Errorf("Price = %q, want empty", q.Price)
	}
	if !strings.Contains(q.Err, "anti-bot") {
		t.Errorf("Err = %q, want it to mention anti-bot", q.Err)
	}
}

func TestFetchReportsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()

	q := newTestFetcher(srv).Fetch(context.Background(), lisboa)
	if q.Err == "" {
		t.Fatal("expected an error for a 403 response")
	}
	if !strings.Contains(q.Err, "403") {
		t.Errorf("Err = %q, want it to mention the status code", q.Err)
	}
	// Even a failed quote keeps the link, which is what the page falls back to.
	if q.URL == "" {
		t.Error("URL should be set even when the fetch fails")
	}
}

func TestFetchReportsMissingPrice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><body><p>nenhuma acomodacao encontrada</p></body></html>`))
	}))
	defer srv.Close()

	q := newTestFetcher(srv).Fetch(context.Background(), lisboa)
	if !strings.Contains(q.Err, "nao encontrado") {
		t.Errorf("Err = %q, want it to say the price was not found", q.Err)
	}
}

func TestProbeReportsWhatCameBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><body><div data-testid="price-and-discounted-price">R$ 990</div></body></html>`))
	}))
	defer srv.Close()

	out := newTestFetcher(srv).Probe(context.Background(), srv.URL)
	if out["price"] != "R$ 990" {
		t.Errorf("price = %v, want R$ 990", out["price"])
	}
	if out["strategy"] != "data-testid-exact" {
		t.Errorf("strategy = %v", out["strategy"])
	}
	if out["snippet"] == "" {
		t.Error("snippet should carry the page start for debugging")
	}
}

// A refresh that fails must not erase a price that was already captured —
// losing every price to one blocked burst is the failure this guards against.
func TestMergeKeepsGoodPriceWhenRefreshFails(t *testing.T) {
	prev := Quote{ID: "lisboa", Price: "R$ 2.480", Strategy: "data-testid-exact", Ts: 1000, CheckedTs: 1000}
	next := Quote{ID: "lisboa", Err: "http 403", Ts: 5000, CheckedTs: 5000}

	got := Merge(prev, next)
	if got.Price != "R$ 2.480" {
		t.Errorf("Price = %q, want the earlier price kept", got.Price)
	}
	if got.Ts != 1000 {
		t.Errorf("Ts = %d, want 1000 — the price was captured then, not now", got.Ts)
	}
	if got.CheckedTs != 5000 {
		t.Errorf("CheckedTs = %d, want 5000 — the attempt did happen", got.CheckedTs)
	}
	if got.Err != "http 403" {
		t.Errorf("Err = %q, want the failure recorded", got.Err)
	}
	if got.Strategy != "data-testid-exact" {
		t.Errorf("Strategy = %q, want it preserved with the price", got.Strategy)
	}
}

func TestMergeTakesFreshPrice(t *testing.T) {
	prev := Quote{ID: "lisboa", Price: "R$ 2.480", Ts: 1000}
	next := Quote{ID: "lisboa", Price: "R$ 2.190", Ts: 5000, CheckedTs: 5000}

	got := Merge(prev, next)
	if got.Price != "R$ 2.190" || got.Ts != 5000 {
		t.Errorf("got price %q at ts %d, want the fresh R$ 2.190 at 5000", got.Price, got.Ts)
	}
	if got.Err != "" {
		t.Errorf("Err = %q, want it cleared on success", got.Err)
	}
}

func TestMergeKeepsFailureWhenNothingCached(t *testing.T) {
	next := Quote{ID: "lisboa", Err: "http 403", Ts: 5000, CheckedTs: 5000}

	got := Merge(Quote{}, next)
	if got.Price != "" || got.Err != "http 403" {
		t.Errorf("got %+v, want the failure recorded as-is", got)
	}
}

func TestFetchStampsBothTimestamps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><body><div data-testid="price-and-discounted-price">R$ 2.480</div></body></html>`))
	}))
	defer srv.Close()

	q := newTestFetcher(srv).Fetch(context.Background(), lisboa)
	if q.Ts == 0 || q.CheckedTs == 0 {
		t.Errorf("Ts = %d, CheckedTs = %d, both should be stamped", q.Ts, q.CheckedTs)
	}
}
