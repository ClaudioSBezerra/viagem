package main

import (
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"viagem/internal/store"
	"viagem/internal/trip"
)

// tripBody is an itinerary as the form sends it. The ID comes from the URL
// on an update, never from the body, so a save can't retarget another trip.
type tripBody struct {
	Name          string           `json:"name"`
	Origin        string           `json:"origin"`
	Dest          string           `json:"dest"`
	WindowStart   string           `json:"windowStart"`
	WindowEnd     string           `json:"windowEnd"`
	Adults        int              `json:"adults"`
	Cities        []trip.CityInput `json:"cities"`
	ReturnAirport string           `json:"returnAirport"`
}

// parsed is a tripBody that passed validation, with the cities resolved into
// travel order and the trip's total length worked out.
type parsed struct {
	t      store.Trip
	cities []trip.CitySpec
	nights int
}

// parseTripBody validates an itinerary the way the search will need it: the
// date window has to be able to hold the trip, or the trip is unsearchable
// and there's no point storing it.
func parseTripBody(b tripBody) (parsed, error) {
	name := strings.TrimSpace(b.Name)
	if name == "" {
		return parsed{}, errors.New("dê um nome ao roteiro")
	}
	if utf8.RuneCountInString(name) > trip.MaxNameLen {
		return parsed{}, errors.New("nome muito longo")
	}

	origin, ok := trip.NormalizeAirport(b.Origin)
	if !ok {
		return parsed{}, errors.New("aeroporto de origem invalido (codigo IATA de 3 letras, ex: GYN)")
	}
	dest, ok := trip.NormalizeAirport(b.Dest)
	if !ok {
		return parsed{}, errors.New("aeroporto de destino invalido (codigo IATA de 3 letras, ex: MIA)")
	}
	if origin == dest {
		return parsed{}, errors.New("origem e destino sao o mesmo aeroporto")
	}

	// ReturnAirport is optional: blank means the trip just flies the round
	// trip between origin and dest, same as before this field existed.
	returnAirport := ""
	if raw := strings.TrimSpace(b.ReturnAirport); raw != "" {
		ra, ok := trip.NormalizeAirport(raw)
		if !ok {
			return parsed{}, errors.New("aeroporto de volta invalido (codigo IATA de 3 letras, ex: MCO)")
		}
		if ra == origin {
			return parsed{}, errors.New("aeroporto de volta nao pode ser igual ao de origem")
		}
		returnAirport = ra
	}

	adults := b.Adults
	if adults < 1 || adults > 9 {
		return parsed{}, errors.New("numero de passageiros deve ser entre 1 e 9")
	}

	cities, err := trip.ParseCities(b.Cities)
	if err != nil {
		return parsed{}, err
	}
	nights := trip.TotalNights(cities)

	// Validate the window against the itinerary now, so an unsearchable trip
	// is rejected at save time instead of only when someone clicks "cotar".
	if _, err := trip.Window(b.WindowStart, b.WindowEnd, nights, trip.MaxCandidatesFor(len(cities), returnAirport != "")); err != nil {
		return parsed{}, err
	}

	stored := store.Trip{
		Name:          name,
		Origin:        origin,
		Dest:          dest,
		WindowStart:   b.WindowStart,
		WindowEnd:     b.WindowEnd,
		Adults:        adults,
		Cities:        make([]store.City, len(cities)),
		ReturnAirport: returnAirport,
	}
	for i, c := range cities {
		stored.Cities[i] = store.City{Name: c.Name, Nights: c.Nights}
	}
	return parsed{t: stored, cities: cities, nights: nights}, nil
}

// specsOf rebuilds the validated itinerary from what was stored, for a
// search started against a trip saved earlier.
func specsOf(t store.Trip) []trip.CityInput {
	out := make([]trip.CityInput, len(t.Cities))
	for i, c := range t.Cities {
		out[i] = trip.CityInput{Name: c.Name, Nights: c.Nights}
	}
	return out
}

func (s *server) handleListTrips(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"trips":     s.store.ListTrips(),
		"nextAfter": s.search.nextAllowedMillis(),
		"maxTrips":  trip.MaxTrips,
	})
}

func (s *server) handleCreateTrip(w http.ResponseWriter, r *http.Request) {
	var body tripBody
	if !decodeJSON(w, r, &body) {
		return
	}
	p, err := parseTripBody(body)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}

	p.t.ID = randomHex(8)
	saved, err := s.store.SaveTrip(p.t, trip.MaxTrips)
	if errors.Is(err, store.ErrTooMany) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "limite de roteiros salvos atingido — apague um antes de criar outro",
		})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "falha ao salvar o roteiro"})
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *server) handleGetTrip(w http.ResponseWriter, r *http.Request) {
	t, ok := s.store.GetTrip(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "roteiro nao encontrado"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"trip":      t,
		"nextAfter": s.search.nextAllowedMillis(),
	})
}

func (s *server) handleUpdateTrip(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, ok := s.store.GetTrip(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "roteiro nao encontrado"})
		return
	}

	var body tripBody
	if !decodeJSON(w, r, &body) {
		return
	}
	p, err := parseTripBody(body)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}

	p.t.ID = id
	saved, err := s.store.SaveTrip(p.t, trip.MaxTrips)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "falha ao salvar o roteiro"})
		return
	}

	// Prices found for the old cities or the old window don't describe this
	// itinerary any more, and showing them next to the new one would invite
	// a decision based on numbers for a trip nobody is taking.
	if existing.Search != nil && itineraryChanged(existing, saved) {
		if err := s.store.ClearSearch(id); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "falha ao salvar o roteiro"})
			return
		}
		saved.Search = nil
	}
	writeJSON(w, http.StatusOK, saved)
}

// itineraryChanged reports whether anything the prices depend on moved. The
// name is deliberately not part of it: renaming a trip doesn't invalidate
// what it costs.
func itineraryChanged(a, b store.Trip) bool {
	if a.Origin != b.Origin || a.Dest != b.Dest || a.Adults != b.Adults ||
		a.WindowStart != b.WindowStart || a.WindowEnd != b.WindowEnd ||
		a.ReturnAirport != b.ReturnAirport ||
		len(a.Cities) != len(b.Cities) {
		return true
	}
	for i := range a.Cities {
		if a.Cities[i] != b.Cities[i] {
			return true
		}
	}
	return false
}

func (s *server) handleDeleteTrip(w http.ResponseWriter, r *http.Request) {
	err := s.store.DeleteTrip(r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "roteiro nao encontrado"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "falha ao apagar o roteiro"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleSearchTrip starts pricing one saved itinerary. It answers right away
// and the work continues in the background: pricing every candidate takes
// far longer than the server's write timeout, so the client polls GET
// /api/trips/{id} for progress.
func (s *server) handleSearchTrip(w http.ResponseWriter, r *http.Request) {
	// Registered even with pricing off, so the answer says why instead of
	// the bare 405 that a POST to an unregistered route produces here.
	if !s.searchEnabled {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "cotacao indisponivel neste servidor (SERPAPI_KEY nao configurada)",
		})
		return
	}

	id := r.PathValue("id")
	t, ok := s.store.GetTrip(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "roteiro nao encontrado"})
		return
	}

	// Re-validate rather than trusting what's stored: the rules may have
	// tightened since this trip was saved, and a stored trip is the only
	// input here that didn't just come through parseTripBody.
	p, err := parseTripBody(tripBody{
		Name: t.Name, Origin: t.Origin, Dest: t.Dest,
		WindowStart: t.WindowStart, WindowEnd: t.WindowEnd,
		Adults: t.Adults, Cities: specsOf(t), ReturnAirport: t.ReturnAirport,
	})
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}

	candidates, err := trip.Window(t.WindowStart, t.WindowEnd, p.nights, trip.MaxCandidatesFor(len(p.cities), t.ReturnAirport != ""))
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}

	startedAt, wait, ok := s.search.start(searchRequest{
		TripID:        id,
		Origin:        t.Origin,
		Dest:          t.Dest,
		Adults:        t.Adults,
		Nights:        p.nights,
		Cities:        p.cities,
		Candidates:    candidates,
		ReturnAirport: t.ReturnAirport,
	})
	if !ok {
		writeCooldown(w, "busca recente demais, aguarde (a cota de consultas e compartilhada por todos os roteiros)", wait)
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"started":       true,
		"startedAt":     startedAt.UnixMilli(),
		"candidates":    len(candidates),
		"cities":        len(p.cities),
		"nights":        p.nights,
		"returnAirport": t.ReturnAirport != "",
	})
}
