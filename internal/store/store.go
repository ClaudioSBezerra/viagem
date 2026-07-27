// Package store implements a tiny JSON-file-backed store for trip photos and chat messages.
package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

const maxEntries = 1000

type Photo struct {
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
	Photos   []Photo   `json:"photos"`
	Messages []Message `json:"messages"`
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
