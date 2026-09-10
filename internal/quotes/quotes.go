// Package quotes prices the trip's 8 hotel stays via SerpApi's Google Hotels
// engine — a real, documented JSON API. This replaces an earlier version that
// scraped Booking.com's search page directly: Booking renders its results via
// client-side JS, so a plain HTTP fetch almost never saw a real price no
// matter how well it mimicked a browser. SerpApi does the rendering on its
// end and hands back structured JSON instead.
package quotes

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

// maxBody caps how much of a response we read.
const maxBody = 10 * 1024 * 1024

// spec is the shape of one hotel search: just the parameters that reach the
// SerpApi query. CityQuery is the public way to ask for a price; this is the
// plumbing underneath it.
type spec struct {
	Checkin  string
	Checkout string
	Adults   int
}

// Fetcher prices Specs against SerpApi's Google Hotels engine.
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

type hotelProperty struct {
	Name                string   `json:"name"`
	ExtractedHotelClass int      `json:"extracted_hotel_class"`
	Amenities           []string `json:"amenities"`
	TotalRate           struct {
		ExtractedLowest float64 `json:"extracted_lowest"`
	} `json:"total_rate"`
	RatePerNight struct {
		ExtractedLowest float64 `json:"extracted_lowest"`
	} `json:"rate_per_night"`
}

type hotelsResponse struct {
	Properties []hotelProperty `json:"properties"`
	Error      string          `json:"error"`
}

// buildURL includes the API key and must never be logged — RedactedURL is the
// safe version for that.
func (f *Fetcher) buildURL(query string, s spec) string {
	q := baseParams(query, s)
	q.Set("api_key", f.apiKey)
	return "https://serpapi.com/search.json?" + q.Encode()
}

func baseParams(query string, s spec) url.Values {
	q := url.Values{}
	q.Set("engine", "google_hotels")
	q.Set("q", query)
	q.Set("check_in_date", s.Checkin)
	q.Set("check_out_date", s.Checkout)
	q.Set("adults", strconv.Itoa(s.Adults))
	q.Set("currency", "BRL")
	q.Set("hl", "pt-br")
	q.Set("gl", "br")
	return q
}

// search runs one google_hotels query and returns the parsed response.
func (f *Fetcher) search(ctx context.Context, query string, s spec) (hotelsResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.buildURL(query, s), nil)
	if err != nil {
		return hotelsResponse{}, err
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return hotelsResponse{}, fmt.Errorf("falha ao contatar a SerpApi")
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return hotelsResponse{}, fmt.Errorf("falha ao ler resposta da SerpApi")
	}
	if resp.StatusCode != http.StatusOK {
		return hotelsResponse{}, fmt.Errorf("serpapi respondeu http %d: %s", resp.StatusCode, snippet(body, 300))
	}

	var parsed hotelsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return hotelsResponse{}, fmt.Errorf("resposta da serpapi em formato inesperado (%s): %s", err.Error(), snippet(body, 300))
	}
	if parsed.Error != "" {
		return hotelsResponse{}, fmt.Errorf("serpapi: %s", parsed.Error)
	}
	return parsed, nil
}

// nightlyTotal returns the stay's total price, falling back to rate-per-night
// times the number of nights when SerpApi only gave a nightly rate.
func nightlyTotal(p hotelProperty, nights int) float64 {
	if p.TotalRate.ExtractedLowest > 0 {
		return p.TotalRate.ExtractedLowest
	}
	if nights > 0 && p.RatePerNight.ExtractedLowest > 0 {
		return p.RatePerNight.ExtractedLowest * float64(nights)
	}
	return 0
}

func snippet(body []byte, n int) string {
	if len(body) <= n {
		return string(body)
	}
	return string(body[:n])
}
