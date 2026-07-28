// Package flights prices the trip's one long-haul round trip via SerpApi's
// Google Flights engine — a real, documented JSON API (not scraped HTML like
// internal/quotes does against Booking), so parsing is far more stable.
//
// A full round-trip price needs two SerpApi calls: the first returns outbound
// options plus a departure_token per option, and the second (passing that
// token back) returns the matching return options with the combined price.
// SerpApi's free tier is generous enough that this isn't nearly as scarce a
// resource as FlightAPI's free trial was, but it is still a shared monthly
// quota, so this stays manual-refresh-only rather than running on a timer.
package flights

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// maxBody caps how much of a response we read; SerpApi's flight results carry
// a lot of per-segment detail (layovers, carbon emissions, airline logos) but
// still normally run under a couple of megabytes.
const maxBody = 10 * 1024 * 1024

// Spec describes one round trip to price.
type Spec struct {
	ID       string
	Label    string
	Origin   string // IATA airport code
	Dest     string // IATA airport code
	Depart   string // YYYY-MM-DD
	Return   string // YYYY-MM-DD
	Adults   int
	Currency string
}

// Quote is the result of pricing a Spec. Price is empty when any step failed,
// in which case Err says why. The SerpApi key never appears here — Quote is
// what gets cached to disk and served over the API.
type Quote struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Price    string `json:"price,omitempty"`
	Currency string `json:"currency,omitempty"`
	Source   string `json:"source"`
	Err      string `json:"error,omitempty"`
	Ts       int64  `json:"ts"`
}

// Fetcher prices Specs against SerpApi's Google Flights engine.
type Fetcher struct {
	client *http.Client
	apiKey string
}

// NewFetcher builds a Fetcher. apiKey may be empty, in which case every Fetch
// fails immediately — main.go only wires this up when SERPAPI_KEY is set.
func NewFetcher(apiKey string) *Fetcher {
	return &Fetcher{
		client: &http.Client{Timeout: 45 * time.Second},
		apiKey: apiKey,
	}
}

type flightOption struct {
	Price          float64 `json:"price"`
	DepartureToken string  `json:"departure_token"`
}

type serpResponse struct {
	BestFlights  []flightOption `json:"best_flights"`
	OtherFlights []flightOption `json:"other_flights"`
	Error        string         `json:"error"`
}

// buildURL includes the API key and must never be logged — RedactedURL is the
// safe version for that.
func (f *Fetcher) buildURL(s Spec, departureToken string) string {
	q := baseParams(s)
	if departureToken != "" {
		q.Set("departure_token", departureToken)
	}
	q.Set("api_key", f.apiKey)
	return "https://serpapi.com/search.json?" + q.Encode()
}

// RedactedURL is the same request with the API key omitted, safe for logs.
func RedactedURL(s Spec) string {
	return "https://serpapi.com/search.json?" + baseParams(s).Encode()
}

func baseParams(s Spec) url.Values {
	q := url.Values{}
	q.Set("engine", "google_flights")
	q.Set("departure_id", s.Origin)
	q.Set("arrival_id", s.Dest)
	q.Set("outbound_date", s.Depart)
	q.Set("return_date", s.Return)
	q.Set("type", "1") // round trip
	q.Set("adults", strconv.Itoa(s.Adults))
	q.Set("currency", s.Currency)
	q.Set("hl", "pt-br")
	q.Set("gl", "br")
	return q
}

// Fetch prices a single round trip. It always returns a Quote: on failure the
// Quote carries Err and an empty Price, so the caller can cache the attempt.
func (f *Fetcher) Fetch(ctx context.Context, s Spec) Quote {
	q := Quote{
		ID:       s.ID,
		Label:    s.Label,
		Currency: s.Currency,
		Source:   "serpapi-google-flights",
		Ts:       time.Now().UnixMilli(),
	}

	if f.apiKey == "" {
		q.Err = "SERPAPI_KEY nao configurada"
		return q
	}

	outbound, err := f.search(ctx, s, "")
	if err != nil {
		q.Err = err.Error()
		return q
	}
	if outbound.Error != "" {
		q.Err = "serpapi: " + outbound.Error
		return q
	}

	best := cheapest(outbound)
	if best == nil {
		q.Err = "nenhum voo de ida encontrado"
		return q
	}
	if best.DepartureToken == "" {
		// Sem token de retorno para combinar: usa o preco de ida como esta.
		q.Price = fmt.Sprintf("%.0f", best.Price)
		return q
	}

	roundTrip, err := f.search(ctx, s, best.DepartureToken)
	if err != nil {
		q.Err = err.Error()
		return q
	}
	if roundTrip.Error != "" {
		q.Err = "serpapi (volta): " + roundTrip.Error
		return q
	}
	final := cheapest(roundTrip)
	if final == nil {
		q.Err = "nenhum voo de volta encontrado"
		return q
	}

	q.Price = fmt.Sprintf("%.0f", final.Price)
	return q
}

func cheapest(r serpResponse) *flightOption {
	var best *flightOption
	for i := range r.BestFlights {
		o := &r.BestFlights[i]
		if o.Price > 0 && (best == nil || o.Price < best.Price) {
			best = o
		}
	}
	for i := range r.OtherFlights {
		o := &r.OtherFlights[i]
		if o.Price > 0 && (best == nil || o.Price < best.Price) {
			best = o
		}
	}
	return best
}

func (f *Fetcher) search(ctx context.Context, s Spec, departureToken string) (serpResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.buildURL(s, departureToken), nil)
	if err != nil {
		return serpResponse{}, err
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return serpResponse{}, fmt.Errorf("falha ao contatar a SerpApi")
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return serpResponse{}, fmt.Errorf("falha ao ler resposta da SerpApi")
	}
	if resp.StatusCode != http.StatusOK {
		return serpResponse{}, fmt.Errorf("serpapi respondeu http %d: %s", resp.StatusCode, snippet(body, 300))
	}

	var parsed serpResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return serpResponse{}, fmt.Errorf("resposta da serpapi em formato inesperado (%s): %s", err.Error(), snippet(body, 300))
	}
	return parsed, nil
}

func snippet(body []byte, n int) string {
	if len(body) <= n {
		return string(body)
	}
	return string(body[:n])
}
