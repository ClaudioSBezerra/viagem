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

// Spec describes one hotel stay to price.
type Spec struct {
	ID       string
	Label    string // search text sent to SerpApi, e.g. "Sana Rex Hotel Lisboa"
	City     string
	Checkin  string
	Checkout string
	Adults   int
	Rooms    int
}

// Nights returns the stay length, or 0 if the dates don't parse.
func (s Spec) Nights() int {
	in, err := time.Parse("2006-01-02", s.Checkin)
	if err != nil {
		return 0
	}
	out, err := time.Parse("2006-01-02", s.Checkout)
	if err != nil {
		return 0
	}
	return int(out.Sub(in).Hours() / 24)
}

// Quote is the result of pricing a Spec. Price is empty when the fetch or the
// parse failed, in which case Err says why.
type Quote struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	City   string `json:"city"`
	Price  string `json:"price,omitempty"`
	Nights int    `json:"nights,omitempty"`
	Source string `json:"source"`
	Err    string `json:"error,omitempty"`
	Ts     int64  `json:"ts"`
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
	Name      string `json:"name"`
	TotalRate struct {
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
func (f *Fetcher) buildURL(s Spec) string {
	q := baseParams(s)
	q.Set("api_key", f.apiKey)
	return "https://serpapi.com/search.json?" + q.Encode()
}

// RedactedURL is the same request with the API key omitted, safe for logs.
func RedactedURL(s Spec) string {
	return "https://serpapi.com/search.json?" + baseParams(s).Encode()
}

func baseParams(s Spec) url.Values {
	q := url.Values{}
	q.Set("engine", "google_hotels")
	q.Set("q", s.Label)
	q.Set("check_in_date", s.Checkin)
	q.Set("check_out_date", s.Checkout)
	q.Set("adults", strconv.Itoa(s.Adults))
	q.Set("currency", "BRL")
	q.Set("hl", "pt-br")
	q.Set("gl", "br")
	return q
}

// Fetch prices a single stay. It always returns a Quote: on failure the Quote
// carries Err and an empty Price, so the caller can cache the attempt.
func (f *Fetcher) Fetch(ctx context.Context, s Spec) Quote {
	q := Quote{
		ID:     s.ID,
		Label:  s.Label,
		City:   s.City,
		Nights: s.Nights(),
		Source: "serpapi-google-hotels",
		Ts:     time.Now().UnixMilli(),
	}

	if f.apiKey == "" {
		q.Err = "SERPAPI_KEY nao configurada"
		return q
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.buildURL(s), nil)
	if err != nil {
		q.Err = err.Error()
		return q
	}

	resp, err := f.client.Do(req)
	if err != nil {
		q.Err = "falha ao contatar a SerpApi"
		return q
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		q.Err = "falha ao ler resposta da SerpApi"
		return q
	}
	if resp.StatusCode != http.StatusOK {
		q.Err = fmt.Sprintf("serpapi respondeu http %d: %s", resp.StatusCode, snippet(body, 300))
		return q
	}

	var parsed hotelsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		q.Err = fmt.Sprintf("resposta da serpapi em formato inesperado (%s): %s", err.Error(), snippet(body, 300))
		return q
	}
	if parsed.Error != "" {
		q.Err = "serpapi: " + parsed.Error
		return q
	}
	if len(parsed.Properties) == 0 {
		q.Err = "serpapi nao encontrou esse hotel para essas datas"
		return q
	}

	// The search text is the specific hotel + city, so Google's own ranking
	// puts the matching property first — no separate name-matching needed.
	top := parsed.Properties[0]
	best := top.TotalRate.ExtractedLowest
	if best <= 0 && q.Nights > 0 {
		best = top.RatePerNight.ExtractedLowest * float64(q.Nights)
	}
	if best <= 0 {
		q.Err = "serpapi nao retornou um preco valido"
		return q
	}

	q.Price = fmt.Sprintf("R$ %.0f", best)
	return q
}

func snippet(body []byte, n int) string {
	if len(body) <= n {
		return string(body)
	}
	return string(body[:n])
}
