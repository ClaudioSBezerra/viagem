package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
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

// writeCooldown is the answer to every search the throttle turned down: one
// 429 shape, so the front end has a single branch to handle.
func writeCooldown(w http.ResponseWriter, msg string, wait time.Duration) {
	writeJSON(w, http.StatusTooManyRequests, map[string]any{
		"error":      msg,
		"retryAfter": int(wait.Seconds()),
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}
