package trip

import (
	"fmt"
	"testing"
)

func TestWindowRejectsBadNights(t *testing.T) {
	if _, err := Window("2027-05-01", "2027-05-31", MinNights-1, MaxCandidates); err == nil {
		t.Error("expected error for nights below MinNights")
	}
	if _, err := Window("2027-05-01", "2027-05-31", MaxNights+1, MaxCandidates); err == nil {
		t.Error("expected error for nights above MaxNights")
	}
}

func TestWindowRejectsBadDates(t *testing.T) {
	if _, err := Window("not-a-date", "2027-05-31", 7, MaxCandidates); err == nil {
		t.Error("expected error for an invalid start date")
	}
	if _, err := Window("2027-05-01", "not-a-date", 7, MaxCandidates); err == nil {
		t.Error("expected error for an invalid end date")
	}
}

func TestWindowRejectsTightWindow(t *testing.T) {
	// 6 days between start/end can't fit a 7-night stay.
	if _, err := Window("2027-05-01", "2027-05-07", 7, MaxCandidates); err == nil {
		t.Error("expected error when the window is shorter than the requested stay")
	}
}

func TestWindowExactFit(t *testing.T) {
	// Window is exactly as long as the stay: only one valid departure date.
	got, err := Window("2027-05-01", "2027-05-08", 7, MaxCandidates)
	if err != nil {
		t.Fatalf("Window: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1", len(got))
	}
	if got[0].Depart != "2027-05-01" || got[0].Return != "2027-05-08" {
		t.Errorf("got %+v", got[0])
	}
}

func TestWindowCapsAtMaxCandidates(t *testing.T) {
	// A whole month with a short stay has far more than MaxCandidates valid
	// departure dates.
	got, err := Window("2027-05-01", "2027-05-31", 3, MaxCandidates)
	if err != nil {
		t.Fatalf("Window: %v", err)
	}
	if len(got) > MaxCandidates {
		t.Fatalf("got %d candidates, want at most %d", len(got), MaxCandidates)
	}
	if len(got) < 2 {
		t.Fatalf("got %d candidates, want more than one for a month-long window", len(got))
	}

	first, last := got[0], got[len(got)-1]
	if first.Depart != "2027-05-01" {
		t.Errorf("first candidate departs %s, want the earliest possible date", first.Depart)
	}
	if last.Depart != "2027-05-28" {
		t.Errorf("last candidate departs %s, want the latest possible date (2027-05-28)", last.Depart)
	}

	// Every candidate must actually fit inside the window and have the
	// right length.
	for _, c := range got {
		if c.Return <= c.Depart {
			t.Errorf("candidate %+v has return before/equal to depart", c)
		}
	}
}

func TestWindowCandidatesAreSortedAndUnique(t *testing.T) {
	got, err := Window("2027-05-01", "2027-05-31", 7, MaxCandidates)
	if err != nil {
		t.Fatalf("Window: %v", err)
	}
	seen := map[string]bool{}
	for i, c := range got {
		if seen[c.Depart] {
			t.Errorf("duplicate departure date %s", c.Depart)
		}
		seen[c.Depart] = true
		if i > 0 && c.Depart <= got[i-1].Depart {
			t.Errorf("candidates not strictly increasing at index %d: %s after %s", i, c.Depart, got[i-1].Depart)
		}
	}
}

func TestParseCitiesBuildsItineraryInOrder(t *testing.T) {
	got, err := ParseCities([]CityInput{
		{Name: "Miami", Nights: 4},
		{Name: " Orlando ", Nights: 3},
		{Name: "   ", Nights: 0}, // linha em branco do formulário
	})
	if err != nil {
		t.Fatalf("ParseCities: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d cities, want 2 (blank row dropped)", len(got))
	}
	// A ordem informada é a ordem da viagem, então não pode ser reordenada.
	if got[0].Name != "Miami" || got[1].Name != "Orlando" {
		t.Errorf("got %+v, want Miami then Orlando", got)
	}
	if got[1].ID != "orlando" {
		t.Errorf("got ID %q, want the trimmed slug", got[1].ID)
	}
	if n := TotalNights(got); n != 7 {
		t.Errorf("TotalNights = %d, want 7", n)
	}
}

func TestParseCitiesRejects(t *testing.T) {
	cases := []struct {
		name string
		in   []CityInput
	}{
		{"nenhuma cidade", []CityInput{{Name: "  ", Nights: 3}}},
		{"cidade repetida", []CityInput{{Name: "Miami", Nights: 2}, {Name: "miami", Nights: 2}}},
		{"noites zeradas", []CityInput{{Name: "Miami", Nights: 0}}},
		{"total abaixo do minimo", []CityInput{{Name: "Miami", Nights: MinNights - 1}}},
		{"total acima do maximo", []CityInput{
			{Name: "Miami", Nights: MaxNights},
			{Name: "Orlando", Nights: 1},
		}},
		{"cidades demais", cityInputs(MaxHotelCities+1, 1)},
	}
	for _, tc := range cases {
		if _, err := ParseCities(tc.in); err == nil {
			t.Errorf("%s: expected an error, got none", tc.name)
		}
	}
}

// cityInputs builds n distinct one-leg rows of the given nights each, so the
// limit tests follow MaxHotelCities instead of hardcoding it.
func cityInputs(n, nights int) []CityInput {
	out := make([]CityInput, n)
	for i := range out {
		out[i] = CityInput{Name: fmt.Sprintf("Cidade %d", i+1), Nights: nights}
	}
	return out
}

func TestParseCitiesAcceptsMaxCities(t *testing.T) {
	got, err := ParseCities(cityInputs(MaxHotelCities, 2))
	if err != nil {
		t.Fatalf("ParseCities with exactly MaxHotelCities cities: %v", err)
	}
	if len(got) != MaxHotelCities {
		t.Errorf("got %d cities, want %d", len(got), MaxHotelCities)
	}
	// The budget has to leave room for more than one departure date even
	// at the cap, or the longest itineraries lose the date comparison.
	if n := MaxCandidatesFor(MaxHotelCities); n < 2 {
		t.Errorf("MaxCandidatesFor(%d) = %d, want at least 2", MaxHotelCities, n)
	}
}

func TestStaysChainWithoutGaps(t *testing.T) {
	cities := []CitySpec{
		{ID: "miami", Name: "Miami", Nights: 4},
		{ID: "orlando", Name: "Orlando", Nights: 3},
	}
	candidates, err := Window("2027-05-01", "2027-05-31", TotalNights(cities), MaxCandidatesFor(len(cities)))
	if err != nil {
		t.Fatalf("Window: %v", err)
	}

	for _, c := range candidates {
		stays, err := Stays(c, cities)
		if err != nil {
			t.Fatalf("Stays(%+v): %v", c, err)
		}
		if len(stays) != len(cities) {
			t.Fatalf("got %d stays, want %d", len(stays), len(cities))
		}
		if stays[0].Checkin != c.Depart {
			t.Errorf("first checkin %s, want the departure date %s", stays[0].Checkin, c.Depart)
		}
		// Cada cidade começa no dia em que a anterior termina — sem buracos
		// e sem noites cobradas em duas cidades ao mesmo tempo.
		for i := 1; i < len(stays); i++ {
			if stays[i].Checkin != stays[i-1].Checkout {
				t.Errorf("stay %d checks in %s but the previous one ends %s",
					i, stays[i].Checkin, stays[i-1].Checkout)
			}
		}
		if last := stays[len(stays)-1]; last.Checkout != c.Return {
			t.Errorf("last checkout %s, want the return date %s", last.Checkout, c.Return)
		}
	}
}

func TestMaxCandidatesForStaysInBudget(t *testing.T) {
	for cities := 1; cities <= MaxHotelCities; cities++ {
		n := MaxCandidatesFor(cities)
		if n < 1 {
			t.Errorf("%d cities: got %d candidates, want at least 1", cities, n)
		}
		if spend := n * (1 + cities); spend > SearchBudget {
			t.Errorf("%d cities: %d candidates would spend %d searches, over the %d budget",
				cities, n, spend, SearchBudget)
		}
	}
	// O caso comum (duas cidades) não pode ter encolhido com a mudança.
	if got := MaxCandidatesFor(2); got != MaxCandidates {
		t.Errorf("MaxCandidatesFor(2) = %d, want the full %d", got, MaxCandidates)
	}
}

func TestNormalizeAirport(t *testing.T) {
	valid := map[string]string{"MIA": "MIA", "mia": "MIA", "  gyn  ": "GYN"}
	for in, want := range valid {
		got, ok := NormalizeAirport(in)
		if !ok {
			t.Errorf("NormalizeAirport(%q) rejected a valid code", in)
		}
		if got != want {
			t.Errorf("NormalizeAirport(%q) = %q, want %q", in, got, want)
		}
	}
	for _, in := range []string{"", "MI", "MIAM", "MI1", "M-A", "米AI"} {
		if _, ok := NormalizeAirport(in); ok {
			t.Errorf("NormalizeAirport(%q) accepted an invalid code", in)
		}
	}
}
