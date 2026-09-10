package quotes

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// CityQuery is a broad "cheapest hotel matching these filters in this city"
// search: it has no specific hotel to look for, just whichever qualifying
// property is cheapest in a given city over a given stretch of dates. That
// is what building an itinerary needs (internal/trip) — the group picks the
// cities, not the hotels.
type CityQuery struct {
	ID       string // result key, e.g. "downtown-miami"
	City     string // search text sent to SerpApi, e.g. "Downtown Miami"
	Checkin  string
	Checkout string
	Adults   int
	MaxClass int // hotels above this star class are excluded; 0 = no cap
	// RequireAll: each entry is a group of keyword variants (mixing
	// languages is fine — SerpApi's amenity strings follow the hl/gl params,
	// and this app also runs Portuguese-only queries elsewhere) where at
	// least one must appear (case-insensitive substring) in the hotel's
	// amenities; every group must be satisfied for the hotel to qualify.
	RequireAll [][]string
}

func (cq CityQuery) nights() int {
	in, err := time.Parse("2006-01-02", cq.Checkin)
	if err != nil {
		return 0
	}
	out, err := time.Parse("2006-01-02", cq.Checkout)
	if err != nil {
		return 0
	}
	return int(out.Sub(in).Hours() / 24)
}

// CityQuote is the result of a CityQuery: the cheapest qualifying hotel
// found, or Err explaining why none matched.
type CityQuote struct {
	ID   string `json:"id"`
	City string `json:"city"`
	// Checkin/Checkout echo the query's dates. They matter when several
	// CityQuotes are legs of one itinerary (internal/miami), each with its
	// own stretch of the trip: without them a result row can't say which
	// nights it covers, and neither can the printed version.
	Checkin  string `json:"checkin,omitempty"`
	Checkout string `json:"checkout,omitempty"`
	Hotel    string `json:"hotel,omitempty"`
	Class    int    `json:"class,omitempty"`
	Price    string `json:"price,omitempty"`
	Nights   int    `json:"nights,omitempty"`
	Source   string `json:"source"`
	Err      string `json:"error,omitempty"`
	Ts       int64  `json:"ts"`
}

// FetchCity prices a CityQuery against SerpApi's Google Hotels engine,
// picking the cheapest property that satisfies MaxClass and RequireAll. It
// always returns a CityQuote: on failure the quote carries Err and an empty
// Price, same convention as Fetch.
func (f *Fetcher) FetchCity(ctx context.Context, cq CityQuery) CityQuote {
	nights := cq.nights()
	q := CityQuote{
		ID:       cq.ID,
		City:     cq.City,
		Checkin:  cq.Checkin,
		Checkout: cq.Checkout,
		Nights:   nights,
		Source:   "serpapi-google-hotels",
		Ts:       time.Now().UnixMilli(),
	}

	if f.apiKey == "" {
		q.Err = "SERPAPI_KEY nao configurada"
		return q
	}

	parsed, err := f.search(ctx, cq.City, spec{Checkin: cq.Checkin, Checkout: cq.Checkout, Adults: cq.Adults})
	if err != nil {
		q.Err = err.Error()
		return q
	}
	if len(parsed.Properties) == 0 {
		q.Err = "serpapi nao encontrou hoteis nessa cidade para essas datas"
		return q
	}

	best, bestPrice := cheapestMatching(parsed.Properties, cq.MaxClass, cq.RequireAll, nights)
	if best == nil {
		q.Err = "nenhum hotel encontrado com os filtros pedidos (classe/comodidades)"
		return q
	}

	q.Hotel = best.Name
	q.Class = best.ExtractedHotelClass
	q.Price = fmt.Sprintf("R$ %.0f", bestPrice)
	return q
}

// cheapestMatching returns the lowest-priced property whose star class is at
// most maxClass (0 = no cap) and whose amenities satisfy every group in
// requireAll, or nil if none qualifies.
func cheapestMatching(props []hotelProperty, maxClass int, requireAll [][]string, nights int) (*hotelProperty, float64) {
	var best *hotelProperty
	var bestPrice float64
	for i := range props {
		p := &props[i]
		if maxClass > 0 && p.ExtractedHotelClass > maxClass {
			continue
		}
		if !matchesAll(p.Amenities, requireAll) {
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
	return best, bestPrice
}

// matchesAll reports whether amenities satisfies every keyword group in
// requireAll — each group is an OR (any keyword in it matching is enough),
// matched case-insensitively as a substring against the joined amenities.
func matchesAll(amenities []string, requireAll [][]string) bool {
	if len(requireAll) == 0 {
		return true
	}
	joined := strings.ToLower(strings.Join(amenities, " | "))
	for _, group := range requireAll {
		found := false
		for _, kw := range group {
			if strings.Contains(joined, strings.ToLower(kw)) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
