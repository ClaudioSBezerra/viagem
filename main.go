package main

import (
	"context"
	"embed"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"viagem/internal/flights"
	"viagem/internal/quotes"
	"viagem/internal/store"
	"viagem/internal/trip"
)

//go:embed web/index.html
var webFS embed.FS

const (
	maxBodyBytes = 8 * 1024

	// searchCooldown gates pricing, across every saved itinerary rather than
	// per trip: SerpApi's free tier is one monthly quota shared by all of
	// them, and a single run costs up to trip.SearchBudget searches (a
	// round-trip flight lookup plus one hotel search per city, for each
	// candidate date). Nothing here runs on a timer — pricing happens only
	// when someone clicks the button.
	searchCooldown = 3 * time.Hour
	searchSpacing  = 5 * time.Second
)

func main() {
	addr := envOr("ADDR", "127.0.0.1:8080")
	dbPath := envOr("DB_PATH", "data/trip.json")

	st, err := store.New(dbPath)
	if err != nil {
		log.Fatalf("failed to open store at %s: %v", dbPath, err)
	}
	// If this prints zero on every deploy instead of just the first one, the
	// persistent volume at DB_PATH's directory isn't actually persisting
	// between deploys — check the Coolify Storages tab, not the storage
	// format (a database would lose data the same way if the volume itself
	// isn't kept).
	log.Printf("store: carregado de %s (%d roteiro(s) salvo(s))", dbPath, len(st.ListTrips()))

	indexPage, err := webFS.ReadFile("web/index.html")
	if err != nil {
		log.Fatalf("failed to load embedded index.html: %v", err)
	}

	// The same SerpApi key prices both hotels (google_hotels) and flights
	// (google_flights) — one shared, limited monthly quota.
	serpAPIKey := os.Getenv("SERPAPI_KEY")

	srv := &server{
		store:         st,
		indexPage:     indexPage,
		search:        newSearcher(quotes.NewFetcher(serpAPIKey), flights.NewFetcher(serpAPIKey), st),
		searchEnabled: serpAPIKey != "",
	}

	if srv.searchEnabled {
		log.Printf("cotacao: enabled (roteiro de ate %d cidades, ate %d janelas de data, teto de %d buscas por rodada, cooldown de %s)",
			trip.MaxHotelCities, trip.MaxCandidates, trip.SearchBudget, searchCooldown)
	} else {
		log.Printf("cotacao: disabled (missing SERPAPI_KEY) — roteiros podem ser montados e salvos, mas nao cotados")
	}

	httpSrv := &http.Server{
		Addr:         addr,
		Handler:      srv.routes(),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("viagem listening on %s (db: %s)", addr, dbPath)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(ctx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}
