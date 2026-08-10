// Package miami implements the flexible-date search for a possible second
// trip — separate from the Iberian itinerary the rest of this app plans.
// Given a window of dates, a desired stay length, a destination airport and
// one or two hotel-search cities, it samples a handful of candidate
// departure dates spread evenly across the window and prices a round-trip
// flight plus the cheapest qualifying hotel in each city for each one, so
// the group can compare which specific week (and which of the cities) is
// cheapest instead of guessing.
//
// The name and the "miami-search" API route predate this becoming
// destination-agnostic — it started as a Miami/Fort Lauderdale-only search
// (maio 2027) and the caller now picks the destination and hotel cities
// instead of them being fixed in code, but renaming the route/package felt
// like unnecessary churn for what's still the same shape of search.
//
// This intentionally does not search every day in the window: at up to
// 1+MaxHotelCities SerpApi searches per candidate (round-trip flight costs
// two calls, one hotel search per city), a full month would cost 100+ calls
// against the same shared quota hotels/flights above depend on.
// MaxCandidates bounds that; see miamiCooldown in main.go for the resulting
// cooldown.
package miami

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// MaxCandidates is the most departure dates a single search samples across
// the window, evenly spaced (always including the earliest and latest
// possible departure).
const MaxCandidates = 8

// MinNights/MaxNights bound the "noites" field on the search form.
const (
	MinNights = 2
	MaxNights = 21
)

// MaxHotelClass matches the "até 4 estrelas" ask — anything pricier/fancier
// is excluded rather than just deprioritized.
const MaxHotelClass = 4

// MaxHotelCities caps how many hotel-search areas one request can compare —
// each extra city costs one more SerpApi search per candidate, same
// reasoning as MaxCandidates.
const MaxHotelCities = 2

// Origins lists the airports the search form may pick as departure, both
// close enough to the group to be interchangeable.
var Origins = map[string]string{
	"GYN": "Goiânia",
	"BSB": "Brasília",
}

var airportCodeRe = regexp.MustCompile(`^[A-Z]{3}$`)

// ValidAirportCode reports whether code is a well-formed 3-letter IATA
// airport code once trimmed and uppercased.
func ValidAirportCode(code string) bool {
	return airportCodeRe.MatchString(strings.ToUpper(strings.TrimSpace(code)))
}

// CitySpec is one hotel search area.
type CitySpec struct {
	ID   string // result key, derived from Name
	Name string // search text sent to SerpApi
}

var slugNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// slugify turns a city name into a stable, URL/JSON-key-safe ID. It doesn't
// bother transliterating accents — non-ASCII runes just fall out as
// separators — since the result is only ever used as an internal map/JSON
// key, never shown to anyone; CitySpec.Name carries the real display text.
func slugify(name string) string {
	s := slugNonAlnum.ReplaceAllString(strings.ToLower(name), "-")
	return strings.Trim(s, "-")
}

// ParseHotelCities splits a "cidades para hotel" form field (comma-separated
// free text) into up to MaxHotelCities named searches, deduplicated by slug.
func ParseHotelCities(raw string) ([]CitySpec, error) {
	var out []CitySpec
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		id := slugify(name)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, CitySpec{ID: id, Name: name})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("informe ao menos uma cidade para buscar hotel")
	}
	if len(out) > MaxHotelCities {
		return nil, fmt.Errorf("no maximo %d cidades para comparar", MaxHotelCities)
	}
	return out, nil
}

// HotelFilters is passed to quotes.CityQuery.RequireAll for every search:
// breakfast and parking included, matching the "prático, confortável, custo
// baixo" brief. Keyword groups mix Portuguese and English since SerpApi's
// amenity strings can come back in either depending on the query/region.
var HotelFilters = [][]string{
	{"café da manhã", "cafe da manha", "breakfast"},
	{"estacionamento", "parking"},
}

// Candidate is one departure/return pair to price.
type Candidate struct {
	Depart string // YYYY-MM-DD
	Return string // YYYY-MM-DD
}

// Window generates up to MaxCandidates depart/return pairs of the given
// length, evenly spaced across [start, end]. The returned candidates always
// include the earliest possible departure and, when more than one fits,
// the latest one too.
func Window(start, end string, nights int) ([]Candidate, error) {
	if nights < MinNights || nights > MaxNights {
		return nil, fmt.Errorf("noites deve ser entre %d e %d", MinNights, MaxNights)
	}
	ts, err := time.Parse("2006-01-02", start)
	if err != nil {
		return nil, fmt.Errorf("data de início inválida, use AAAA-MM-DD")
	}
	te, err := time.Parse("2006-01-02", end)
	if err != nil {
		return nil, fmt.Errorf("data de término inválida, use AAAA-MM-DD")
	}
	totalDays := int(te.Sub(ts).Hours() / 24)
	if totalDays < nights {
		return nil, fmt.Errorf("a janela informada (%d dias) é menor que a estadia pedida (%d noites)", totalDays, nights)
	}

	// lastDepartOffset is how many days after start the latest valid
	// departure is, so departure+nights still lands on or before end.
	lastDepartOffset := totalDays - nights
	count := lastDepartOffset + 1
	if count > MaxCandidates {
		count = MaxCandidates
	}

	seen := make(map[int]bool, count)
	out := make([]Candidate, 0, count)
	for i := 0; i < count; i++ {
		offset := 0
		if count > 1 {
			offset = i * lastDepartOffset / (count - 1)
		}
		if seen[offset] {
			continue
		}
		seen[offset] = true
		depart := ts.AddDate(0, 0, offset)
		out = append(out, Candidate{
			Depart: depart.Format("2006-01-02"),
			Return: depart.AddDate(0, 0, nights).Format("2006-01-02"),
		})
	}
	return out, nil
}
