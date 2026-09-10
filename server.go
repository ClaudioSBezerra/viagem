package main

import (
	"net/http"

	"viagem/internal/store"
)

// server holds everything the handlers need. Bundling it here (instead of
// closing over locals inside main) keeps main to wiring and lets a test
// build a server with a temp store and exercise the routes directly.
type server struct {
	store     *store.Store
	indexPage []byte
	search    *searcher

	// searchEnabled is false when there's no SerpApi key: itineraries can
	// still be written and read, they just can't be priced — the pricing
	// route then refuses with an explanation and the page hides the button.
	searchEnabled bool
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /api/config", s.handleConfig)

	mux.HandleFunc("GET /api/trips", s.handleListTrips)
	mux.HandleFunc("POST /api/trips", s.handleCreateTrip)
	mux.HandleFunc("GET /api/trips/{id}", s.handleGetTrip)
	mux.HandleFunc("PUT /api/trips/{id}", s.handleUpdateTrip)
	mux.HandleFunc("DELETE /api/trips/{id}", s.handleDeleteTrip)

	mux.HandleFunc("POST /api/trips/{id}/search", s.handleSearchTrip)

	return mux
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(s.indexPage)
}

// handleConfig tells the page what this deployment can do, so the UI doesn't
// have to guess from a 404 whether pricing is switched on.
func (s *server) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"searchEnabled":  s.searchEnabled,
		"searchCooldown": int(searchCooldown.Seconds()),
	})
}
