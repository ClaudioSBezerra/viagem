package main

import (
	"path/filepath"
	"testing"
	"time"

	"viagem/internal/flights"
	"viagem/internal/quotes"
	"viagem/internal/store"
	"viagem/internal/trip"
)

// newOfflineSearcher builds a searcher with no API key. Both fetchers
// short-circuit on an empty key without touching the network, so what these
// tests exercise is exactly this app's own work — laying the itinerary out
// over each candidate's dates and storing it in the shape the page polls.
func newOfflineSearcher(t *testing.T) (*searcher, *store.Store) {
	t.Helper()
	st, err := store.New(filepath.Join(t.TempDir(), "trip.json"))
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	sr := newSearcher(quotes.NewFetcher(""), flights.NewFetcher(""), st)
	sr.spacing = 0
	return sr, st
}

func TestSearcherLaysOutItinerary(t *testing.T) {
	sr, st := newOfflineSearcher(t)

	cities := []trip.CitySpec{
		{ID: "downtown-miami", Name: "Downtown Miami", Nights: 4},
		{ID: "orlando", Name: "Orlando", Nights: 3},
	}
	nights := trip.TotalNights(cities)
	candidates, err := trip.Window("2027-05-01", "2027-05-31", nights, trip.MaxCandidatesFor(len(cities)))
	if err != nil {
		t.Fatalf("Window: %v", err)
	}
	saved, err := st.SaveTrip(store.Trip{ID: "t1", Name: "Miami"}, 0)
	if err != nil {
		t.Fatalf("SaveTrip: %v", err)
	}

	sr.run(time.Now(), searchRequest{
		TripID: saved.ID, Origin: "GYN", Dest: "MIA", Adults: 2,
		Nights: nights, Cities: cities, Candidates: candidates,
	})

	got, ok := st.GetTrip(saved.ID)
	if !ok {
		t.Fatal("trip vanished")
	}
	if got.Search == nil {
		t.Fatal("no search was stored")
	}
	if !got.Search.Done {
		t.Error("search was not marked done")
	}
	if got.Search.Nights != 7 {
		t.Errorf("nights = %d, want 7 (4+3)", got.Search.Nights)
	}
	if len(got.Search.Candidates) != len(candidates) {
		t.Fatalf("stored %d candidates, want %d", len(got.Search.Candidates), len(candidates))
	}

	for _, c := range got.Search.Candidates {
		if len(c.Hotels) != len(cities) {
			t.Fatalf("candidate %s has %d hotels, want one per city", c.Depart, len(c.Hotels))
		}
		// Order must follow the requested travel order, or the dates below
		// would be attached to the wrong city.
		if c.Hotels[0].City != "Downtown Miami" || c.Hotels[1].City != "Orlando" {
			t.Errorf("candidate %s hotels out of order: %q then %q", c.Depart, c.Hotels[0].City, c.Hotels[1].City)
		}
		if c.Hotels[0].Checkin != c.Depart {
			t.Errorf("candidate %s: first stay checks in %s, want the departure date", c.Depart, c.Hotels[0].Checkin)
		}
		if c.Hotels[0].Checkout != c.Hotels[1].Checkin {
			t.Errorf("candidate %s: gap between stays — %s ends %s but %s starts %s",
				c.Depart, c.Hotels[0].City, c.Hotels[0].Checkout, c.Hotels[1].City, c.Hotels[1].Checkin)
		}
		if last := c.Hotels[len(c.Hotels)-1]; last.Checkout != c.Return {
			t.Errorf("candidate %s: last stay ends %s, want the return date %s", c.Depart, last.Checkout, c.Return)
		}
		if c.Hotels[0].Nights != 4 || c.Hotels[1].Nights != 3 {
			t.Errorf("candidate %s: nights %d/%d, want 4/3", c.Depart, c.Hotels[0].Nights, c.Hotels[1].Nights)
		}
		// Every leg was actually attempted: without an API key each one
		// comes back as a failed quote rather than being skipped.
		for _, h := range c.Hotels {
			if h.Ts == 0 {
				t.Errorf("candidate %s: %s was never fetched", c.Depart, h.City)
			}
		}
		if c.Flight.Ts == 0 {
			t.Errorf("candidate %s: flight was never fetched", c.Depart)
		}
	}
}

// A trip deleted between the click and the run has nowhere to store results;
// the run must give up instead of writing into nothing.
func TestSearcherStopsWhenTripIsGone(t *testing.T) {
	sr, st := newOfflineSearcher(t)
	candidates, err := trip.Window("2027-05-01", "2027-05-10", 5, 2)
	if err != nil {
		t.Fatalf("Window: %v", err)
	}

	sr.run(time.Now(), searchRequest{
		TripID: "nao-existe", Origin: "GYN", Dest: "MIA", Adults: 2, Nights: 5,
		Cities:     []trip.CitySpec{{ID: "miami", Name: "Miami", Nights: 5}},
		Candidates: candidates,
	})

	if n := len(st.ListTrips()); n != 0 {
		t.Errorf("the run created %d trips out of nothing", n)
	}
}

// A single-city itinerary is still an itinerary: one stay spanning the trip.
func TestSearcherSingleCity(t *testing.T) {
	sr, st := newOfflineSearcher(t)
	saved, err := st.SaveTrip(store.Trip{ID: "t1", Name: "Miami"}, 0)
	if err != nil {
		t.Fatalf("SaveTrip: %v", err)
	}
	candidates, err := trip.Window("2027-05-01", "2027-05-10", 5, trip.MaxCandidatesFor(1))
	if err != nil {
		t.Fatalf("Window: %v", err)
	}

	sr.run(time.Now(), searchRequest{
		TripID: saved.ID, Origin: "BSB", Dest: "MIA", Adults: 2, Nights: 5,
		Cities:     []trip.CitySpec{{ID: "miami", Name: "Miami", Nights: 5}},
		Candidates: candidates,
	})

	got, _ := st.GetTrip(saved.ID)
	for _, c := range got.Search.Candidates {
		if len(c.Hotels) != 1 {
			t.Fatalf("got %d hotels, want 1", len(c.Hotels))
		}
		if c.Hotels[0].Checkin != c.Depart || c.Hotels[0].Checkout != c.Return {
			t.Errorf("single stay %s-%s should span the whole trip %s-%s",
				c.Hotels[0].Checkin, c.Hotels[0].Checkout, c.Depart, c.Return)
		}
	}
}
