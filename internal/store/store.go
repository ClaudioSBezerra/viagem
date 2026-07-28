// Package store implements a tiny JSON-file-backed store for trip photos and chat messages.
package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"viagem/internal/quotes"
)

const maxEntries = 1000

type Photo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	City string `json:"city"`
	URL  string `json:"url"`
	Ts   int64  `json:"ts"`
}

type Message struct {
	Name string `json:"name"`
	Text string `json:"text"`
	Ts   int64  `json:"ts"`
}

type data struct {
	Photos   []Photo                 `json:"photos"`
	Messages []Message               `json:"messages"`
	Quotes   map[string]quotes.Quote `json:"quotes,omitempty"`
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

func (s *Store) ListPhotos() []Photo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Photo, len(s.data.Photos))
	copy(out, s.data.Photos)
	return out
}

func (s *Store) AddPhoto(p Photo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Photos = append(s.data.Photos, p)
	if len(s.data.Photos) > maxEntries {
		s.data.Photos = s.data.Photos[len(s.data.Photos)-maxEntries:]
	}
	return s.saveLocked()
}

// DeletePhoto removes the photo with the given ID, returning it and true if found.
func (s *Store) DeletePhoto(id string) (Photo, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, p := range s.data.Photos {
		if p.ID == id {
			s.data.Photos = append(s.data.Photos[:i], s.data.Photos[i+1:]...)
			if err := s.saveLocked(); err != nil {
				return Photo{}, false, err
			}
			return p, true, nil
		}
	}
	return Photo{}, false, nil
}

func (s *Store) ListMessages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Message, len(s.data.Messages))
	copy(out, s.data.Messages)
	return out
}

func (s *Store) AddMessage(m Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Messages = append(s.data.Messages, m)
	if len(s.data.Messages) > maxEntries {
		s.data.Messages = s.data.Messages[len(s.data.Messages)-maxEntries:]
	}
	return s.saveLocked()
}

// ListQuotes returns the cached hotel quotes keyed by stay ID.
func (s *Store) ListQuotes() map[string]quotes.Quote {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]quotes.Quote, len(s.data.Quotes))
	for k, v := range s.data.Quotes {
		out[k] = v
	}
	return out
}

// SetQuote caches one quote, replacing any earlier result for that stay. A
// failed attempt is cached too, so the page can show why a price is missing
// instead of silently rendering nothing.
func (s *Store) SetQuote(q quotes.Quote) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.Quotes == nil {
		s.data.Quotes = make(map[string]quotes.Quote)
	}
	s.data.Quotes[q.ID] = q
	return s.saveLocked()
}

// saveLocked writes the store atomically: encode to a temp file in the same
// directory, then rename over the target so a crash mid-write can't corrupt it.
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
