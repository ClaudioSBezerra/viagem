// Package trip builds and prices an itinerary: a list of cities with the
// nights to spend in each, flown to from some airport, to happen somewhere
// inside a window of dates.
//
// The dates are the point. Rather than pricing one fixed departure, this
// samples several candidate departure dates spread evenly across the window
// and prices each one — a round-trip flight plus the cheapest qualifying
// hotel for every city's own stretch of the trip — so the group can see
// which week is actually cheapest instead of guessing.
//
// The cities are consecutive legs of one trip, not alternatives: Miami for
// 4 nights then Orlando for 3 is a single 7-night trip whose cost is the
// flight plus BOTH hotels, and the trip's length comes from adding the
// cities up rather than being asked for separately.
//
// This intentionally does not price every day in the window: at 1+one-per-
// city searches per candidate, a full month would cost 100+ calls against a
// limited SerpApi quota. SearchBudget bounds the whole run, so adding cities
// buys detail at the cost of candidate dates rather than multiplying the
// bill.
package trip

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

// MinNights/MaxNights bound the trip as a whole — the sum of every city's
// nights. MinCityNights is the floor for a single leg: one night is a real
// stopover, zero is just a city the group drives through, which needs no
// hotel search.
const (
	MinNights     = 2
	MaxNights     = 21
	MinCityNights = 1
)

// SearchBudget caps the SerpApi searches one run may spend: each candidate
// date costs one flight lookup plus one hotel lookup per city. Candidates
// are traded away for cities so a 4-city itinerary doesn't quietly cost
// twice what a 2-city one does — see MaxCandidatesFor.
const SearchBudget = 24

// MaxHotelClass keeps the search on practical, comfortable places instead of
// pricier ones — anything above this star class is excluded outright rather
// than just deprioritized.
const MaxHotelClass = 4

// MaxHotelCities caps how many cities one itinerary may string together.
// Each extra city costs one more search per candidate date, which
// SearchBudget then pays for out of the candidate count.
const MaxHotelCities = 4

// MaxNameLen bounds a saved itinerary's name, and MaxTrips how many can be
// kept at once — this is a small group planning a handful of trips, not a
// travel agency, and every stored trip carries a full priced run with it.
const (
	MaxNameLen = 60
	MaxTrips   = 20
)

// MaxCandidatesFor is how many departure dates a search over cities-many
// cities may sample without exceeding SearchBudget. At two cities it works
// out to the full MaxCandidates.
func MaxCandidatesFor(cities int) int {
	if cities < 1 {
		cities = 1
	}
	n := SearchBudget / (1 + cities)
	if n > MaxCandidates {
		n = MaxCandidates
	}
	if n < 1 {
		n = 1
	}
	return n
}

var airportCodeRe = regexp.MustCompile(`^[A-Z]{3}$`)

// NormalizeAirport trims and uppercases an airport field, returning false if
// what's left isn't a well-formed 3-letter IATA code.
func NormalizeAirport(code string) (string, bool) {
	c := strings.ToUpper(strings.TrimSpace(code))
	return c, airportCodeRe.MatchString(c)
}

// HotelFilters is required of every hotel the search will consider:
// breakfast and parking included. Keyword groups mix Portuguese and English
// since SerpApi's amenity strings can come back in either depending on the
// query and region.
var HotelFilters = [][]string{
	{"café da manhã", "cafe da manha", "breakfast"},
	{"estacionamento", "parking"},
}

// CitySpec is one leg of the itinerary: a place to sleep and for how long.
type CitySpec struct {
	ID     string // result key, derived from Name
	Name   string // search text sent to SerpApi
	Nights int    // nights slept here, before moving on to the next city
}

// CityInput is one leg as the form sends it, before validation.
type CityInput struct {
	Name   string `json:"name"`
	Nights int    `json:"nights"`
}

var slugNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// slugify turns a city name into a stable, JSON-key-safe ID. It doesn't
// bother transliterating accents — non-ASCII runes just fall out as
// separators — since the result is only ever an internal key, never shown to
// anyone; CitySpec.Name carries the real display text.
func slugify(name string) string {
	s := slugNonAlnum.ReplaceAllString(strings.ToLower(name), "-")
	return strings.Trim(s, "-")
}

// ParseCities validates the itinerary in the order given — that order is the
// travel order, so it decides which nights each city gets. Rows with a blank
// name are dropped (the form keeps an empty row for typing into), duplicates
// are rejected rather than silently merged, since two rows for the same city
// would otherwise book overlapping stays.
func ParseCities(in []CityInput) ([]CitySpec, error) {
	var out []CitySpec
	total := 0
	seen := map[string]bool{}
	for _, c := range in {
		name := strings.TrimSpace(c.Name)
		if name == "" {
			continue
		}
		id := slugify(name)
		if id == "" {
			continue
		}
		if seen[id] {
			return nil, fmt.Errorf("cidade repetida: %s", name)
		}
		if c.Nights < MinCityNights {
			return nil, fmt.Errorf("%s: informe ao menos %d noite por cidade", name, MinCityNights)
		}
		if c.Nights > MaxNights {
			return nil, fmt.Errorf("%s: no maximo %d noites", name, MaxNights)
		}
		seen[id] = true
		total += c.Nights
		out = append(out, CitySpec{ID: id, Name: name, Nights: c.Nights})
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("informe ao menos uma cidade para buscar hotel")
	}
	if len(out) > MaxHotelCities {
		return nil, fmt.Errorf("no maximo %d cidades por roteiro", MaxHotelCities)
	}
	if total < MinNights || total > MaxNights {
		return nil, fmt.Errorf("o total de noites (%d) deve ficar entre %d e %d", total, MinNights, MaxNights)
	}
	return out, nil
}

// TotalNights is how long the whole trip lasts: every city's nights added
// up. This is the stay length the date window has to accommodate.
func TotalNights(cities []CitySpec) int {
	total := 0
	for _, c := range cities {
		total += c.Nights
	}
	return total
}

// Candidate is one departure/return pair to price.
type Candidate struct {
	Depart string // YYYY-MM-DD
	Return string // YYYY-MM-DD
}

// Window generates up to maxCandidates depart/return pairs of the given
// length, evenly spaced across [start, end]. The returned candidates always
// include the earliest possible departure and, when more than one fits, the
// latest one too. Pass MaxCandidatesFor(len(cities)) for maxCandidates, so a
// longer itinerary spends its search budget on cities instead.
func Window(start, end string, nights, maxCandidates int) ([]Candidate, error) {
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

	if maxCandidates < 1 {
		maxCandidates = 1
	}
	// lastDepartOffset is how many days after start the latest valid
	// departure is, so departure+nights still lands on or before end.
	lastDepartOffset := totalDays - nights
	count := lastDepartOffset + 1
	if count > maxCandidates {
		count = maxCandidates
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

// Stay is one city's slice of a candidate trip, with the dates to price.
type Stay struct {
	ID       string
	City     string
	Nights   int
	Checkin  string // YYYY-MM-DD
	Checkout string // YYYY-MM-DD
}

// Stays lays the itinerary out over one candidate's dates: the first city is
// checked into on the departure date and each following city picks up the
// day the previous one is left, so there are no gaps and no double-booked
// nights. Because the candidate's length is exactly TotalNights(cities), the
// last checkout lands on the return date.
func Stays(c Candidate, cities []CitySpec) ([]Stay, error) {
	day, err := time.Parse("2006-01-02", c.Depart)
	if err != nil {
		return nil, fmt.Errorf("data de partida inválida %q: %w", c.Depart, err)
	}

	out := make([]Stay, 0, len(cities))
	for _, city := range cities {
		next := day.AddDate(0, 0, city.Nights)
		out = append(out, Stay{
			ID:       city.ID,
			City:     city.Name,
			Nights:   city.Nights,
			Checkin:  day.Format("2006-01-02"),
			Checkout: next.Format("2006-01-02"),
		})
		day = next
	}
	return out, nil
}
