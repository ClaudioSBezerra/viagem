// get-drive-token runs a one-time local OAuth flow to obtain a refresh token
// for the viagem app's Google Drive sync. Run it once on your own machine,
// authorize with the Google account that owns the destination Drive folder,
// then copy the printed refresh token into the app's GOOGLE_REFRESH_TOKEN
// environment variable (e.g. as a Coolify secret).
//
// Usage:
//
//	GOOGLE_CLIENT_ID=... GOOGLE_CLIENT_SECRET=... go run ./cmd/get-drive-token
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
)

const listenAddr = "127.0.0.1:8085"

func main() {
	clientID := os.Getenv("GOOGLE_CLIENT_ID")
	clientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")
	if clientID == "" || clientSecret == "" {
		log.Fatal("defina GOOGLE_CLIENT_ID e GOOGLE_CLIENT_SECRET no ambiente antes de rodar")
	}

	conf := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     google.Endpoint,
		Scopes:       []string{drive.DriveFileScope},
		RedirectURL:  "http://" + listenAddr + "/callback",
	}

	state := "viagem-drive-setup"
	authURL := conf.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce)

	codeCh := make(chan string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != state {
			http.Error(w, "state invalido", http.StatusBadRequest)
			return
		}
		code := r.URL.Query().Get("code")
		fmt.Fprintln(w, "Autorizado! Pode fechar esta aba e voltar pro terminal.")
		codeCh <- code
	})
	srv := &http.Server{Addr: listenAddr, Handler: mux}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("servidor local: %v", err)
		}
	}()

	fmt.Println("Abra esse link no navegador e autorize com a conta Google dona da pasta do Drive:")
	fmt.Println()
	fmt.Println(authURL)
	fmt.Println()

	code := <-codeCh
	_ = srv.Shutdown(context.Background())

	tok, err := conf.Exchange(context.Background(), code)
	if err != nil {
		log.Fatalf("falha ao trocar o code por token: %v", err)
	}
	if tok.RefreshToken == "" {
		log.Fatal("nao recebi refresh_token (tente de novo; se ja tinha autorizado antes, revogue o acesso em https://myaccount.google.com/permissions e rode de novo)")
	}

	fmt.Println("Guarde este valor como GOOGLE_REFRESH_TOKEN (env var / secret no Coolify):")
	fmt.Println()
	fmt.Println(tok.RefreshToken)
}
