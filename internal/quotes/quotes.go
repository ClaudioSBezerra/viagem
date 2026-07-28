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
	"strings"
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
	// Match is "exato" when Price came from the searched hotel itself, or
	// "similar" when that hotel wasn't found and Price/Found describe a 3-4
	// star stand-in in the same search results instead.
	Match string `json:"match,omitempty"`
	Found string `json:"found,omitempty"`
	Err   string `json:"error,omitempty"`
	Ts    int64  `json:"ts"`
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
	Name                string `json:"name"`
	ExtractedHotelClass int    `json:"extracted_hotel_class"`
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
func (f *Fetcher) buildURL(query string, s Spec) string {
	q := baseParams(query, s)
	q.Set("api_key", f.apiKey)
	return "https://serpapi.com/search.json?" + q.Encode()
}

// RedactedURL is the same request with the API key omitted, safe for logs.
func RedactedURL(s Spec) string {
	return "https://serpapi.com/search.json?" + baseParams(s.Label, s).Encode()
}

func baseParams(query string, s Spec) url.Values {
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

	parsed, err := f.search(ctx, s.Label, s)
	if err != nil {
		q.Err = err.Error()
		return q
	}

	if len(parsed.Properties) > 0 {
		if exact := findByName(parsed.Properties, s.Label); exact != nil {
			if price := nightlyTotal(*exact, q.Nights); price > 0 {
				q.Price = fmt.Sprintf("R$ %.0f", price)
				q.Match = "exato"
				return q
			}
		}

		// O hotel buscado nao apareceu (ou apareceu sem preco) nos resultados
		// - troca por uma opcao de 3-4 estrelas mais barata na mesma busca,
		// ja que a regiao e as datas continuam as mesmas.
		if sub := cheapestInClassRange(parsed.Properties, 3, 4, q.Nights); sub != nil {
			q.Price = fmt.Sprintf("R$ %.0f", nightlyTotal(*sub, q.Nights))
			q.Match = "similar"
			q.Found = sub.Name
			return q
		}
	}

	// Buscar pelo nome do hotel especifico as vezes nao retorna nada (Google
	// nao reconhece o texto como uma propriedade), mesmo a cidade tendo
	// hoteis de sobra. Tenta de novo so com a cidade, que e uma busca bem
	// mais generica e praticamente sempre retorna resultados.
	broad, err := f.search(ctx, s.City, s)
	if err != nil {
		q.Err = err.Error()
		return q
	}
	if len(broad.Properties) == 0 {
		q.Err = "serpapi nao encontrou nada nessa regiao para essas datas"
		return q
	}
	if sub := cheapestInClassRange(broad.Properties, 3, 4, q.Nights); sub != nil {
		q.Price = fmt.Sprintf("R$ %.0f", nightlyTotal(*sub, q.Nights))
		q.Match = "similar"
		q.Found = sub.Name
		return q
	}

	q.Err = "hotel buscado nao encontrado, e nenhuma opcao 3-4 estrelas com preco disponivel nessa regiao"
	return q
}

// search runs one google_hotels query and returns the parsed response.
func (f *Fetcher) search(ctx context.Context, query string, s Spec) (hotelsResponse, error) {
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

// findByName looks for the searched hotel among the results by a loose,
// case-insensitive substring match in either direction — property names from
// SerpApi don't always match our stored label exactly (chain prefixes,
// rebrands), and requiring an exact match would trigger the "similar hotel"
// fallback far more often than it should.
func findByName(props []hotelProperty, label string) *hotelProperty {
	h := strings.ToLower(label)
	for i := range props {
		p := strings.ToLower(props[i].Name)
		if p == "" {
			continue
		}
		if strings.Contains(h, p) || strings.Contains(p, h) {
			return &props[i]
		}
	}
	return nil
}

// cheapestInClassRange returns the lowest-priced property whose star class
// falls in [min, max], or nil if none has both a class in range and a usable
// price.
func cheapestInClassRange(props []hotelProperty, min, max, nights int) *hotelProperty {
	var best *hotelProperty
	var bestPrice float64
	for i := range props {
		p := &props[i]
		if p.ExtractedHotelClass < min || p.ExtractedHotelClass > max {
			continue
		}
		price := nightlyTotal(*p, nights)
		if price <= 0 {
			continue
		}
		if best == nil || price < bestPrice {
			best, bestPrice = p, price
		}
	}
	return best
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
