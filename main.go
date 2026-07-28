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
	"syscall"
	"time"
	"unicode/utf8"

	"viagem/internal/drivesync"
	"viagem/internal/store"
)

//go:embed web/index.html
var webFS embed.FS

const (
	maxBodyBytes   = 8 * 1024
	maxUploadBytes = 15 * 1024 * 1024
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

		p := store.Photo{Name: name, City: city, URL: url, Ts: time.Now().UnixMilli()}
		if err := s.AddPhoto(p); err != nil {
			log.Printf("add photo: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "falha ao salvar"})
			return
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

		p := store.Photo{Name: name, City: city, URL: "/uploads/" + filename, Ts: time.Now().UnixMilli()}
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
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
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
