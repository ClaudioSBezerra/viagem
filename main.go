package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"viagem/internal/drivesync"
	"viagem/internal/flights"
	"viagem/internal/quotes"
	"viagem/internal/store"
)

//go:embed web/index.html
var webFS embed.FS

const (
	maxBodyBytes   = 8 * 1024
	maxUploadBytes = 15 * 1024 * 1024

	// quoteCooldown gates manual hotel-price refreshes. No background loop
	// here: SerpApi's quota is shared with the flight lookups above, and one
	// round already costs 8 searches (one per stay), so this only runs when
	// someone clicks the button, and rarely.
	quoteCooldown = 3 * time.Hour
	quoteSpacing  = 5 * time.Second

	// flightCooldown gates manual flight-price refreshes. SerpApi's free tier
	// is a monthly quota shared with anything else using the same key, and a
	// round trip costs two searches (outbound + return), so unlike hotel
	// quotes this never runs on a timer — only a person clicking the button,
	// at most once an hour.
	flightCooldown = 1 * time.Hour

	// altCooldown gates the "outra data" whole-itinerary search: it prices
	// all 8 stays plus the flight for a shifted date range in one go (as
	// many as ~18 SerpApi searches, worst case), against the same shared
	// quota — a much steeper cooldown than either individual feature.
	altCooldown = 6 * time.Hour

	// tripStartDate/tripEndDate anchor the day-offset math for the
	// alternate-date search: they must match Stays[0].Checkin - 1 day and
	// flights.Routes[0].Return in trip.go, or the shift will be wrong.
	tripStartDate = "2026-10-14"
	tripEndDate   = "2026-10-31"
)

var urlScheme = regexp.MustCompile(`(?i)^https?://`)

var mimeExt = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

func main() {
	addr := envOr("ADDR", "127.0.0.1:8080")
	dbPath := envOr("DB_PATH", "data/trip.json")
	uploadsDir := filepath.Join(filepath.Dir(dbPath), "uploads")

	if err := os.MkdirAll(uploadsDir, 0o755); err != nil {
		log.Fatalf("failed to create uploads dir %s: %v", uploadsDir, err)
	}

	s, err := store.New(dbPath)
	if err != nil {
		log.Fatalf("failed to open store at %s: %v", dbPath, err)
	}
	// If this prints all zeros on every deploy instead of just the first one,
	// the persistent volume at DB_PATH's directory isn't actually persisting
	// between deploys — check the Coolify Storages tab, not the storage
	// format (a database would lose data the same way if the volume itself
	// isn't kept).
	log.Printf("store: carregado de %s (%d fotos, %d mensagens, %d cotacoes de hotel, %d cotacoes de voo)",
		dbPath, len(s.ListPhotos()), len(s.ListMessages()), len(s.ListQuotes()), len(s.ListFlightQuotes()))

	driveClient, err := drivesync.New(context.Background(),
		os.Getenv("GOOGLE_CLIENT_ID"),
		os.Getenv("GOOGLE_CLIENT_SECRET"),
		os.Getenv("GOOGLE_REFRESH_TOKEN"),
		os.Getenv("GOOGLE_DRIVE_FOLDER_ID"),
	)
	if err != nil {
		log.Fatalf("drive sync setup: %v", err)
	}
	if driveClient != nil {
		log.Printf("drive sync: enabled")
	} else {
		log.Printf("drive sync: disabled (missing GOOGLE_* env vars)")
	}

	indexPage, err := webFS.ReadFile("web/index.html")
	if err != nil {
		log.Fatalf("failed to load embedded index.html: %v", err)
	}

	// Same SerpApi key prices both hotels (google_hotels) and flights
	// (google_flights) — one shared, limited monthly quota.
	serpAPIKey := os.Getenv("SERPAPI_KEY")

	quotesEnabled := serpAPIKey != "" && envOr("QUOTES_ENABLED", "1") != "0"
	refresher := &quoteRefresher{fetcher: quotes.NewFetcher(serpAPIKey), store: s}
	if quotesEnabled {
		log.Printf("quotes: enabled (%d hospedagens, cotacao manual, cooldown de %s)", len(quotes.Stays), quoteCooldown)
	} else {
		log.Printf("quotes: disabled (missing SERPAPI_KEY or QUOTES_ENABLED=0)")
	}

	flightsEnabled := serpAPIKey != ""
	flightRefresh := &flightRefresher{fetcher: flights.NewFetcher(serpAPIKey), store: s}
	if flightsEnabled {
		log.Printf("flights: enabled (%d rota(s), cotacao manual, cooldown de %s)", len(flights.Routes), flightCooldown)
	} else {
		log.Printf("flights: disabled (missing SERPAPI_KEY)")
	}

	altEnabled := quotesEnabled && flightsEnabled
	altRefresh := &altRefresher{hotelFetcher: refresher.fetcher, flightFetcher: flightRefresh.fetcher, store: s}
	if altEnabled {
		log.Printf("alt-search: enabled (cotacao manual do roteiro inteiro em outra data, cooldown de %s)", altCooldown)
	} else {
		log.Printf("alt-search: disabled (precisa de quotes e flights habilitados)")
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexPage)
	})

	mux.HandleFunc("GET /api/photos", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.ListPhotos())
	})

	mux.HandleFunc("POST /api/photos", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name string `json:"name"`
			City string `json:"city"`
			URL  string `json:"url"`
		}
		if !decodeJSON(w, r, &body) {
			return
		}

		name := strings.TrimSpace(body.Name)
		city := strings.TrimSpace(body.City)
		url := strings.TrimSpace(body.URL)

		if !validField(name, 40) || !validField(city, 60) || !validField(url, 500) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "campos invalidos"})
			return
		}
		if !urlScheme.MatchString(url) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "link precisa comecar com http:// ou https://"})
			return
		}

		p := store.Photo{ID: randomHex(8), Name: name, City: city, URL: url, Ts: time.Now().UnixMilli()}
		if err := s.AddPhoto(p); err != nil {
			log.Printf("add photo: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "falha ao salvar"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	mux.HandleFunc("DELETE /api/photos/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		p, found, err := s.DeletePhoto(id)
		if err != nil {
			log.Printf("delete photo: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "falha ao apagar"})
			return
		}
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "foto nao encontrada"})
			return
		}
		if name, ok := strings.CutPrefix(p.URL, "/uploads/"); ok {
			if err := os.Remove(filepath.Join(uploadsDir, name)); err != nil && !os.IsNotExist(err) {
				log.Printf("remove upload file: %v", err)
			}
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	mux.Handle("GET /uploads/", http.StripPrefix("/uploads/", http.FileServer(http.Dir(uploadsDir))))

	mux.HandleFunc("POST /api/upload", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
		if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "arquivo muito grande (max 15MB)"})
			return
		}

		name := strings.TrimSpace(r.FormValue("name"))
		city := strings.TrimSpace(r.FormValue("city"))
		if !validField(name, 40) || !validField(city, 60) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "campos invalidos"})
			return
		}

		file, _, err := r.FormFile("photo")
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "arquivo nao enviado"})
			return
		}
		defer file.Close()

		sniff := make([]byte, 512)
		n, _ := io.ReadFull(file, sniff)
		contentType := http.DetectContentType(sniff[:n])
		ext, ok := mimeExt[contentType]
		if !ok {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "formato de imagem nao suportado (use jpg, png, gif ou webp)"})
			return
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "falha ao processar arquivo"})
			return
		}

		filename := fmt.Sprintf("%d-%s%s", time.Now().UnixNano(), randomHex(8), ext)
		dst, err := os.Create(filepath.Join(uploadsDir, filename))
		if err != nil {
			log.Printf("create upload file: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "falha ao salvar arquivo"})
			return
		}
		defer dst.Close()

		if _, err := io.Copy(dst, file); err != nil {
			log.Printf("write upload file: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "falha ao salvar arquivo"})
			return
		}

		p := store.Photo{ID: randomHex(8), Name: name, City: city, URL: "/uploads/" + filename, Ts: time.Now().UnixMilli()}
		if err := s.AddPhoto(p); err != nil {
			log.Printf("add photo: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "falha ao salvar"})
			return
		}

		if driveClient != nil {
			go syncToDrive(driveClient, uploadsDir, filename, contentType)
		}

		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	if quotesEnabled {
		mux.HandleFunc("GET /api/quotes", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{
				"quotes":    s.ListQuotes(),
				"nextAfter": refresher.nextAllowed().UnixMilli(),
			})
		})

		// Refresh runs in the background: pricing eight stays takes far longer
		// than the server's write timeout, so the request only kicks it off.
		// No debug endpoint here, unlike the old Booking scraper — every probe
		// against a real API would burn a real credit from the shared quota.
		mux.HandleFunc("POST /api/quotes/refresh", func(w http.ResponseWriter, r *http.Request) {
			startedAt, wait, ok := refresher.start()
			if !ok {
				writeJSON(w, http.StatusTooManyRequests, map[string]any{
					"error":      "cotacao recente demais, aguarde",
					"retryAfter": int(wait.Seconds()),
				})
				return
			}
			writeJSON(w, http.StatusAccepted, map[string]any{"started": true, "startedAt": startedAt.UnixMilli()})
		})
	}

	if flightsEnabled {
		mux.HandleFunc("GET /api/flights", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{
				"flights":   s.ListFlightQuotes(),
				"nextAfter": flightRefresh.nextAllowed().UnixMilli(),
			})
		})

		// No background loop here, unlike hotel quotes: SerpApi's quota is
		// shared and a round trip costs two searches, so pricing only happens
		// when someone clicks the button, gated by flightCooldown.
		mux.HandleFunc("POST /api/flights/refresh", func(w http.ResponseWriter, r *http.Request) {
			startedAt, wait, ok := flightRefresh.start()
			if !ok {
				writeJSON(w, http.StatusTooManyRequests, map[string]any{
					"error":      "cotacao recente demais, aguarde",
					"retryAfter": int(wait.Seconds()),
				})
				return
			}
			writeJSON(w, http.StatusAccepted, map[string]any{"started": true, "startedAt": startedAt.UnixMilli()})
		})
	}

	if altEnabled {
		// Prices the whole itinerary (8 stays + the flight) for a start date
		// the caller picks, so the group can compare against the primary
		// dates. Results land in the same /api/quotes and /api/flights
		// responses under "-alt"-suffixed IDs — no separate GET endpoint.
		mux.HandleFunc("POST /api/alt-search", func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				StartDate string `json:"startDate"`
			}
			if !decodeJSON(w, r, &body) {
				return
			}

			delta, err := daysBetween(tripStartDate, body.StartDate)
			if err != nil {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "data invalida, use AAAA-MM-DD"})
				return
			}

			startedAt, wait, ok := altRefresh.start(body.StartDate)
			if !ok {
				writeJSON(w, http.StatusTooManyRequests, map[string]any{
					"error":      "busca alternativa recente demais, aguarde (cota compartilhada com hoteis e voo)",
					"retryAfter": int(wait.Seconds()),
				})
				return
			}

			shiftedEnd := ""
			if endDate, eerr := time.Parse("2006-01-02", tripEndDate); eerr == nil {
				shiftedEnd = endDate.AddDate(0, 0, delta).Format("2006-01-02")
			}

			writeJSON(w, http.StatusAccepted, map[string]any{
				"started":   true,
				"startedAt": startedAt.UnixMilli(),
				"newStart":  body.StartDate,
				"newEnd":    shiftedEnd,
			})
		})
	}

	mux.HandleFunc("GET /api/chat", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.ListMessages())
	})

	mux.HandleFunc("POST /api/chat", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name string `json:"name"`
			Text string `json:"text"`
		}
		if !decodeJSON(w, r, &body) {
			return
		}

		name := strings.TrimSpace(body.Name)
		text := strings.TrimSpace(body.Text)

		if !validField(name, 40) || !validField(text, 500) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "campos invalidos"})
			return
		}

		m := store.Message{Name: name, Text: text, Ts: time.Now().UnixMilli()}
		if err := s.AddMessage(m); err != nil {
			log.Printf("add message: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "falha ao salvar"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	srv := &http.Server{
		Addr:        addr,
		Handler:     mux,
		ReadTimeout: 10 * time.Second,
		// The quote debug endpoint fetches a Booking page inline and can take
		// tens of seconds; a 10s write deadline would truncate its response.
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("viagem listening on %s (db: %s)", addr, dbPath)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}

// quoteRefresher prices every stay in the background and caches the results.
// Only one run happens at a time, and runs are spaced by quoteCooldown, so a
// visitor leaning on the refresh button cannot turn the site into a scraper.
type quoteRefresher struct {
	fetcher *quotes.Fetcher
	store   *store.Store

	mu      sync.Mutex
	running bool
	last    time.Time
}

// start begins a refresh unless one is running or the cooldown has not
// elapsed, in which case it reports how long is left. On success it also
// returns the server's own clock reading for the start of this run — the
// caller echoes it back to the client, which must compare it against Quote.Ts
// (also server time) instead of its own clock. Comparing a browser's Date.now()
// against a server timestamp breaks under any clock drift between the two.
func (qr *quoteRefresher) start() (time.Time, time.Duration, bool) {
	qr.mu.Lock()
	defer qr.mu.Unlock()

	if qr.running {
		return time.Time{}, quoteCooldown, false
	}
	if wait := time.Until(qr.last.Add(quoteCooldown)); wait > 0 {
		return time.Time{}, wait, false
	}

	startedAt := time.Now()
	qr.running = true
	go qr.run()
	return startedAt, 0, true
}

func (qr *quoteRefresher) nextAllowed() time.Time {
	qr.mu.Lock()
	defer qr.mu.Unlock()
	return qr.last.Add(quoteCooldown)
}

func (qr *quoteRefresher) run() {
	defer func() {
		qr.mu.Lock()
		qr.running = false
		qr.last = time.Now()
		qr.mu.Unlock()
	}()

	var ok, failed int
	for i, spec := range quotes.Stays {
		if i > 0 {
			time.Sleep(quoteSpacing)
		}

		// Matches the Fetcher's own 45s client timeout.
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		q := qr.fetcher.Fetch(ctx, spec)
		cancel()

		if q.Err != "" {
			failed++
			log.Printf("quotes: %s (%s): %s", spec.ID, quotes.RedactedURL(spec), q.Err)
		} else {
			ok++
		}
		if err := qr.store.SetQuote(q); err != nil {
			log.Printf("quotes: cache %s: %v", spec.ID, err)
		}
	}
	log.Printf("quotes: ciclo concluido (%d com preco, %d sem)", ok, failed)
}

// flightRefresher prices the trip's round trip(s) in the background, same
// start/nextAllowed shape as quoteRefresher but with no loop() — see
// flightCooldown for why this is manual-only.
type flightRefresher struct {
	fetcher *flights.Fetcher
	store   *store.Store

	mu      sync.Mutex
	running bool
	last    time.Time
}

func (fr *flightRefresher) start() (time.Time, time.Duration, bool) {
	fr.mu.Lock()
	defer fr.mu.Unlock()

	if fr.running {
		return time.Time{}, flightCooldown, false
	}
	if wait := time.Until(fr.last.Add(flightCooldown)); wait > 0 {
		return time.Time{}, wait, false
	}

	startedAt := time.Now()
	fr.running = true
	go fr.run()
	return startedAt, 0, true
}

func (fr *flightRefresher) nextAllowed() time.Time {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	return fr.last.Add(flightCooldown)
}

func (fr *flightRefresher) run() {
	defer func() {
		fr.mu.Lock()
		fr.running = false
		fr.last = time.Now()
		fr.mu.Unlock()
	}()

	for _, spec := range flights.Routes {
		// Fetch makes up to two sequential SerpApi calls (outbound, then
		// return); this must comfortably cover both at the Fetcher's own 45s
		// client timeout each.
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
		q := fr.fetcher.Fetch(ctx, spec)
		cancel()

		if q.Err != "" {
			log.Printf("flights: %s (%s): %s", spec.ID, flights.RedactedURL(spec), q.Err)
		} else {
			log.Printf("flights: %s: %s %s", spec.ID, q.Price, q.Currency)
		}
		if err := fr.store.SetFlightQuote(q); err != nil {
			log.Printf("flights: cache %s: %v", spec.ID, err)
		}
	}
}

// altRefresher prices the whole itinerary (8 stays + the flight) for a
// shifted date range, so the group can compare "what if we went a bit
// earlier/later" against the primary dates without losing those quotes —
// results are cached under "-alt"-suffixed IDs in the same store.
type altRefresher struct {
	hotelFetcher  *quotes.Fetcher
	flightFetcher *flights.Fetcher
	store         *store.Store

	mu      sync.Mutex
	running bool
	last    time.Time
}

func (ar *altRefresher) start(startDate string) (time.Time, time.Duration, bool) {
	ar.mu.Lock()
	defer ar.mu.Unlock()

	if ar.running {
		return time.Time{}, altCooldown, false
	}
	if wait := time.Until(ar.last.Add(altCooldown)); wait > 0 {
		return time.Time{}, wait, false
	}

	startedAt := time.Now()
	ar.running = true
	go ar.run(startDate)
	return startedAt, 0, true
}

func (ar *altRefresher) nextAllowed() time.Time {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	return ar.last.Add(altCooldown)
}

func (ar *altRefresher) run(startDate string) {
	defer func() {
		ar.mu.Lock()
		ar.running = false
		ar.last = time.Now()
		ar.mu.Unlock()
	}()

	delta, err := daysBetween(tripStartDate, startDate)
	if err != nil {
		log.Printf("alt-search: data invalida %q: %v", startDate, err)
		return
	}

	var ok, failed int
	for i, spec := range quotes.ShiftedStays(delta) {
		if i > 0 {
			time.Sleep(quoteSpacing)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		q := ar.hotelFetcher.Fetch(ctx, spec)
		cancel()

		if q.Err != "" {
			failed++
			log.Printf("alt-search: %s: %s", spec.ID, q.Err)
		} else {
			ok++
		}
		if err := ar.store.SetQuote(q); err != nil {
			log.Printf("alt-search: cache %s: %v", spec.ID, err)
		}
	}

	for _, spec := range flights.ShiftedRoutes(delta) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
		q := ar.flightFetcher.Fetch(ctx, spec)
		cancel()

		if q.Err != "" {
			failed++
			log.Printf("alt-search: %s: %s", spec.ID, q.Err)
		} else {
			ok++
		}
		if err := ar.store.SetFlightQuote(q); err != nil {
			log.Printf("alt-search: cache %s: %v", spec.ID, err)
		}
	}

	log.Printf("alt-search: ciclo concluido para %s (%d com preco, %d sem)", startDate, ok, failed)
}

// daysBetween returns how many days b is after a (negative if before), or an
// error if either isn't a valid YYYY-MM-DD date.
func daysBetween(a, b string) (int, error) {
	ta, err := time.Parse("2006-01-02", a)
	if err != nil {
		return 0, err
	}
	tb, err := time.Parse("2006-01-02", b)
	if err != nil {
		return 0, err
	}
	return int(tb.Sub(ta).Hours() / 24), nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func validField(v string, maxLen int) bool {
	return v != "" && utf8.RuneCountInString(v) <= maxLen
}

func syncToDrive(c *drivesync.Client, uploadsDir, filename, contentType string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	f, err := os.Open(filepath.Join(uploadsDir, filename))
	if err != nil {
		log.Printf("drive sync: reopen file: %v", err)
		return
	}
	defer f.Close()

	if _, err := c.Upload(ctx, filename, contentType, f); err != nil {
		log.Printf("drive sync: %v", err)
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "corpo vazio"})
			return false
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "JSON invalido ou corpo muito grande"})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}
