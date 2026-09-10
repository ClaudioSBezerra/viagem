package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"viagem/internal/flights"
	"viagem/internal/quotes"
	"viagem/internal/store"
	"viagem/internal/trip"
)

// searchRequest is one trip's pricing run as the handler hands it over,
// already validated: which saved trip it belongs to, its itinerary, and the
// candidate dates to price.
type searchRequest struct {
	TripID     string
	Origin     string
	Dest       string
	Adults     int
	Nights     int // the whole trip, i.e. every city's nights added up
	Cities     []trip.CitySpec
	Candidates []trip.Candidate
}

// searcher prices trips in the background. One run at a time across the
// whole app, not one per trip: every run spends from the same SerpApi quota,
// so the cooldown has to be global — see searchCooldown.
type searcher struct {
	job
	hotelFetcher  *quotes.Fetcher
	flightFetcher *flights.Fetcher
	store         *store.Store
	// spacing is the pause between consecutive upstream calls, so one run
	// doesn't hammer SerpApi. A field rather than the constant directly so
	// tests can drive a whole run without waiting minutes for the pauses.
	spacing time.Duration
}

func newSearcher(hotel *quotes.Fetcher, flight *flights.Fetcher, s *store.Store) *searcher {
	return &searcher{
		job:           job{cooldown: searchCooldown},
		hotelFetcher:  hotel,
		flightFetcher: flight,
		store:         s,
		spacing:       searchSpacing,
	}
}

func (sr *searcher) start(req searchRequest) (time.Time, time.Duration, bool) {
	return sr.job.start(func(startedAt time.Time) { sr.run(startedAt, req) })
}

func (sr *searcher) run(startedAt time.Time, req searchRequest) {
	// stays[i] is the itinerary laid out over candidate i's dates: which city
	// is slept in on which nights. Computed up front so the pending rows can
	// already show their dates, and so a bad date fails the whole run before
	// spending any quota.
	stays := make([][]trip.Stay, len(req.Candidates))
	run := store.Search{
		Nights:     req.Nights,
		StartedAt:  startedAt.UnixMilli(),
		Candidates: make([]store.Candidate, len(req.Candidates)),
	}
	for i, c := range req.Candidates {
		legs, err := trip.Stays(c, req.Cities)
		if err != nil {
			log.Printf("busca: %v", err)
			return
		}
		stays[i] = legs

		// Hotels is pre-sized and pre-labeled (city and dates set, price
		// still empty) so a still-pending row already shows which city and
		// which nights it's for instead of a blank one while it waits its
		// turn below.
		hotels := make([]quotes.CityQuote, len(legs))
		for hi, leg := range legs {
			hotels[hi] = quotes.CityQuote{
				ID:       leg.ID,
				City:     leg.City,
				Checkin:  leg.Checkin,
				Checkout: leg.Checkout,
				Nights:   leg.Nights,
			}
		}
		run.Candidates[i] = store.Candidate{Depart: c.Depart, Return: c.Return, Hotels: hotels}
	}

	// A trip deleted between the click and here has nothing to write to;
	// stop before spending any quota on it.
	if err := sr.store.SetSearch(req.TripID, run); err != nil {
		log.Printf("busca: %s: %v", req.TripID, err)
		return
	}

	var ok, failed int
	first := true
	spaceOut := func() {
		if first {
			first = false
			return
		}
		time.Sleep(sr.spacing)
	}

	for i, c := range req.Candidates {
		spaceOut()
		fq := fetchWithin(flightFetchTimeout, func(ctx context.Context) flights.Quote {
			return sr.flightFetcher.Fetch(ctx, flights.Spec{
				ID:       fmt.Sprintf("c%d", i),
				Label:    fmt.Sprintf("%s → %s (ida e volta)", req.Origin, req.Dest),
				Origin:   req.Origin,
				Dest:     req.Dest,
				Depart:   c.Depart,
				Return:   c.Return,
				Adults:   req.Adults,
				Currency: "BRL",
			})
		})
		run.Candidates[i].Flight = fq
		if fq.Err != "" {
			failed++
			log.Printf("busca: voo %s %s->%s %s a %s: %s", req.TripID, req.Origin, req.Dest, c.Depart, c.Return, fq.Err)
		} else {
			ok++
		}

		// One hotel per leg, each priced only for the nights the group
		// actually sleeps there — not for the whole trip.
		for hi, leg := range stays[i] {
			spaceOut()
			cq := fetchWithin(hotelFetchTimeout, func(ctx context.Context) quotes.CityQuote {
				return sr.hotelFetcher.FetchCity(ctx, quotes.CityQuery{
					ID:         leg.ID,
					City:       leg.City,
					Checkin:    leg.Checkin,
					Checkout:   leg.Checkout,
					Adults:     req.Adults,
					MaxClass:   trip.MaxHotelClass,
					RequireAll: trip.HotelFilters,
				})
			})
			if cq.Err != "" {
				failed++
				log.Printf("busca: %s %s %s->%s: %s", req.TripID, leg.ID, leg.Checkin, leg.Checkout, cq.Err)
			} else {
				ok++
			}
			run.Candidates[i].Hotels[hi] = cq
		}

		if err := sr.store.SetSearch(req.TripID, run); err != nil {
			// The trip was deleted mid-run: there's nowhere to put the rest,
			// so stop rather than burning quota on results nobody will see.
			log.Printf("busca: %s interrompida: %v", req.TripID, err)
			return
		}
	}

	run.Done = true
	if err := sr.store.SetSearch(req.TripID, run); err != nil {
		log.Printf("busca: %s: %v", req.TripID, err)
		return
	}
	log.Printf("busca: %s concluida (%d com preco, %d sem)", req.TripID, ok, failed)
}
