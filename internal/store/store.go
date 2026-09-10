// Package store implements a tiny JSON-file-backed store for the group's
// saved itineraries, each with the priced search that belongs to it.
package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"viagem/internal/flights"
	"viagem/internal/quotes"
)

// ErrNotFound is returned when a trip ID doesn't match anything stored.
var ErrNotFound = errors.New("roteiro nao encontrado")

// ErrTooMany is returned when creating a trip would exceed the cap the
// caller passed to SaveTrip.
var ErrTooMany = errors.New("limite de roteiros atingido")

type data struct {
	Trips []Trip `json:"trips"`

	// Photos/Messages are the photo gallery and group chat that earlier
	// versions of this app stored here. Nothing reads them any more, but
	// they are carried through every save as raw JSON so upgrading a running
	// deployment doesn't wipe the group's history from the volume. Drop
	// these two fields once that data is confirmed unwanted.
	Photos   json.RawMessage `json:"photos,omitempty"`
	Messages json.RawMessage `json:"messages,omitempty"`
}

// City is one leg of a saved itinerary: where to sleep and for how long.
type City struct {
	Name   string `json:"name"`
	Nights int    `json:"nights"`
}

// Trip is one itinerary the group is considering: where it goes, when it
// could happen, and the most recent pricing run for it.
type Trip struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Origin      string `json:"origin"` // departure airport, IATA code
	Dest        string `json:"dest"`   // arrival airport, IATA code
	WindowStart string `json:"windowStart"`
	WindowEnd   string `json:"windowEnd"`
	Adults      int    `json:"adults"`
	Cities      []City `json:"cities"`
	CreatedAt   int64  `json:"createdAt"`
	UpdatedAt   int64  `json:"updatedAt"`

	// Search is the latest pricing run, or nil if this trip has never been
	// priced. Editing the itinerary clears it, since prices for the old
	// cities and dates would be misleading next to the new ones.
	Search *Search `json:"search,omitempty"`
}

// Candidate is one depart/return pair priced by a search: the round-trip
// flight, plus one hotel per city of the itinerary. Hotels follows the
// itinerary's travel order, and each entry covers only that city's own
// nights, so the candidate's cost is the flight plus ALL of them — they are
// consecutive legs of one trip, not competing options. Sub-fields stay
// zero-valued (Ts == 0) until their fetch completes — the frontend polls on
// that to show live progress; city and dates are pre-filled up front so a
// still-pending row already says what it is for.
type Candidate struct {
	Depart string             `json:"depart"`
	Return string             `json:"return"`
	Flight flights.Quote      `json:"flight"`
	Hotels []quotes.CityQuote `json:"hotels"`
}

// Search is one pricing run over a trip's date window. Only the latest run
// per trip is kept — starting a new one replaces it outright.
type Search struct {
	// Nights is the whole trip: every city's nights added up.
	Nights     int         `json:"nights"`
	StartedAt  int64       `json:"startedAt"`
	Done       bool        `json:"done"`
	Candidates []Candidate `json:"candidates"`
}

type Store struct {
	mu   sync.Mutex
	path string
	data data
}

func New(path string) (*Store, error) {
	s := &Store{path: path}

	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(raw, &s.data); err != nil {
		return nil, err
	}
	return s, nil
}

// ListTrips returns every saved itinerary, newest first. The slice and the
// trips in it are copies, so callers can't mutate what's stored.
func (s *Store) ListTrips() []Trip {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Trip, len(s.data.Trips))
	for i, t := range s.data.Trips {
		out[i] = t.clone()
	}
	return out
}

// GetTrip returns one saved itinerary by ID.
func (s *Store) GetTrip(id string) (Trip, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	i := s.indexLocked(id)
	if i < 0 {
		return Trip{}, false
	}
	return s.data.Trips[i].clone(), true
}

// SaveTrip creates or replaces an itinerary, keeping the list newest-first.
// A new trip gets its CreatedAt set and is rejected once max trips are
// stored; an existing one keeps the date it was created on. Either way the
// stored Search is left alone — pricing is written by SetSearch, and a save
// that carried a stale copy of it would clobber a run in progress.
func (s *Store) SaveTrip(t Trip, max int) (Trip, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UnixMilli()
	t.UpdatedAt = now

	if i := s.indexLocked(t.ID); i >= 0 {
		t.CreatedAt = s.data.Trips[i].CreatedAt
		t.Search = s.data.Trips[i].Search
		s.data.Trips[i] = t
		if err := s.saveLocked(); err != nil {
			return Trip{}, err
		}
		return t.clone(), nil
	}

	if max > 0 && len(s.data.Trips) >= max {
		return Trip{}, ErrTooMany
	}
	t.CreatedAt = now
	t.Search = nil
	// Newest first, matching how the list is shown.
	s.data.Trips = append([]Trip{t}, s.data.Trips...)
	if err := s.saveLocked(); err != nil {
		return Trip{}, err
	}
	return t.clone(), nil
}

// DeleteTrip removes an itinerary and everything priced for it.
func (s *Store) DeleteTrip(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	i := s.indexLocked(id)
	if i < 0 {
		return ErrNotFound
	}
	s.data.Trips = append(s.data.Trips[:i], s.data.Trips[i+1:]...)
	return s.saveLocked()
}

// SetSearch replaces one trip's pricing run — called repeatedly as each
// candidate finishes, so a poll always reflects live progress instead of
// only the finished result.
func (s *Store) SetSearch(id string, run Search) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	i := s.indexLocked(id)
	if i < 0 {
		return ErrNotFound
	}
	s.data.Trips[i].Search = &run
	return s.saveLocked()
}

// ClearSearch drops a trip's pricing run, for when the itinerary changed
// under it and the old prices no longer describe it.
func (s *Store) ClearSearch(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	i := s.indexLocked(id)
	if i < 0 {
		return ErrNotFound
	}
	s.data.Trips[i].Search = nil
	return s.saveLocked()
}

func (s *Store) indexLocked(id string) int {
	if id == "" {
		return -1
	}
	for i, t := range s.data.Trips {
		if t.ID == id {
			return i
		}
	}
	return -1
}

// clone deep-copies the parts a caller could otherwise mutate through the
// returned value — the slices — so a handler holding a Trip can't reach back
// into the store's own state.
func (t Trip) clone() Trip {
	out := t
	out.Cities = append([]City(nil), t.Cities...)
	if t.Search != nil {
		run := *t.Search
		run.Candidates = make([]Candidate, len(t.Search.Candidates))
		for i, c := range t.Search.Candidates {
			c.Hotels = append([]quotes.CityQuote(nil), c.Hotels...)
			run.Candidates[i] = c
		}
		out.Search = &run
	}
	return out
}

func (s *Store) saveLocked() error {
	raw, err := json.Marshal(s.data)
	if err != nil {
		return err
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".trip-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path)
}
