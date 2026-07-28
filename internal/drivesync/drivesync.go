// Package drivesync uploads photos to a Google Drive folder on a best-effort
// basis, authenticated as the folder's own owner via a stored OAuth refresh
// token (personal Google accounts don't grant service accounts write quota
// on My Drive, so a service account can't be used here).
package drivesync

import (
	"context"
	"fmt"
	"io"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

const Scope = drive.DriveFileScope

type Client struct {
	svc      *drive.Service
	folderID string
}

// New builds a client from a client ID/secret + long-lived refresh token.
// Returns (nil, nil) if any credential is empty, so callers can treat Drive
// sync as an optional feature.
func New(ctx context.Context, clientID, clientSecret, refreshToken, folderID string) (*Client, error) {
	if clientID == "" || clientSecret == "" || refreshToken == "" || folderID == "" {
		return nil, nil
	}

	conf := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     google.Endpoint,
		Scopes:       []string{Scope},
	}
	ts := conf.TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken})

	svc, err := drive.NewService(ctx, option.WithTokenSource(ts))
	if err != nil {
		return nil, fmt.Errorf("drive service: %w", err)
	}

	return &Client{svc: svc, folderID: folderID}, nil
}

// Upload creates a new file in the configured folder and returns its Drive file ID.
func (c *Client) Upload(ctx context.Context, filename, mimeType string, r io.Reader) (string, error) {
	f := &drive.File{
		Name:    filename,
		Parents: []string{c.folderID},
	}
	created, err := c.svc.Files.Create(f).Media(r).Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("drive upload: %w", err)
	}
	return created.Id, nil
}
