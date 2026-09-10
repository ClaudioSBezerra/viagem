package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trip.json")
	s, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, path
}

func sampleTrip(name string) Trip {
	return Trip{
		ID: name, Name: name, Origin: "GYN", Dest: "MIA",
		WindowStart: "2027-05-01", WindowEnd: "2027-05-31", Adults: 2,
		Cities: []City{{Name: "Miami", Nights: 4}, {Name: "Orlando", Nights: 3}},
	}
}

func TestSaveTripCreatesThenUpdates(t *testing.T) {
	s, _ := newTestStore(t)

	created, err := s.SaveTrip(sampleTrip("a"), 0)
	if err != nil {
		t.Fatalf("SaveTrip: %v", err)
	}
	if created.CreatedAt == 0 || created.UpdatedAt == 0 {
		t.Errorf("timestamps not set: %+v", created)
	}

	updated := sampleTrip("a")
	updated.Name = "renomeado"
	got, err := s.SaveTrip(updated, 0)
	if err != nil {
		t.Fatalf("SaveTrip (update): %v", err)
	}
	if got.Name != "renomeado" {
		t.Errorf("name = %q, want the updated one", got.Name)
	}
	// An update must not look like a new trip, or the list order and any
	// "created on" display would jump around on every edit.
	if got.CreatedAt != created.CreatedAt {
		t.Errorf("CreatedAt changed on update: %d -> %d", created.CreatedAt, got.CreatedAt)
	}
	if n := len(s.ListTrips()); n != 1 {
		t.Errorf("got %d trips, want 1 — the update should not have added one", n)
	}
}

func TestSaveTripKeepsSearchAcrossUpdates(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.SaveTrip(sampleTrip("a"), 0); err != nil {
		t.Fatalf("SaveTrip: %v", err)
	}
	if err := s.SetSearch("a", Search{Nights: 7, StartedAt: 42, Done: true}); err != nil {
		t.Fatalf("SetSearch: %v", err)
	}

	// A save carrying no Search (the handlers never send one) must not wipe
	// the run that SetSearch wrote — otherwise a rename mid-search would
	// throw away everything already priced.
	if _, err := s.SaveTrip(sampleTrip("a"), 0); err != nil {
		t.Fatalf("SaveTrip (update): %v", err)
	}
	got, ok := s.GetTrip("a")
	if !ok {
		t.Fatal("trip vanished")
	}
	if got.Search == nil || got.Search.StartedAt != 42 {
		t.Errorf("search lost on update: %+v", got.Search)
	}
}

func TestSaveTripEnforcesMax(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.SaveTrip(sampleTrip("a"), 2); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := s.SaveTrip(sampleTrip("b"), 2); err != nil {
		t.Fatalf("second: %v", err)
	}
	if _, err := s.SaveTrip(sampleTrip("c"), 2); err != ErrTooMany {
		t.Errorf("third trip: got %v, want ErrTooMany", err)
	}
	// The cap must not block editing what's already there.
	if _, err := s.SaveTrip(sampleTrip("a"), 2); err != nil {
		t.Errorf("updating an existing trip at the cap: %v", err)
	}
}

func TestListTripsNewestFirst(t *testing.T) {
	s, _ := newTestStore(t)
	for _, id := range []string{"a", "b", "c"} {
		if _, err := s.SaveTrip(sampleTrip(id), 0); err != nil {
			t.Fatalf("SaveTrip %s: %v", id, err)
		}
	}
	got := s.ListTrips()
	if len(got) != 3 || got[0].ID != "c" || got[2].ID != "a" {
		t.Errorf("got order %s/%s/%s, want c/b/a", got[0].ID, got[1].ID, got[2].ID)
	}
}

func TestListTripsReturnsCopies(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.SaveTrip(sampleTrip("a"), 0); err != nil {
		t.Fatalf("SaveTrip: %v", err)
	}

	got := s.ListTrips()
	got[0].Name = "mexido"
	got[0].Cities[0].Name = "mexido"

	fresh, _ := s.GetTrip("a")
	if fresh.Name == "mexido" || fresh.Cities[0].Name == "mexido" {
		t.Error("a caller mutating the returned trip reached into the store")
	}
}

func TestSearchLifecycle(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.SaveTrip(sampleTrip("a"), 0); err != nil {
		t.Fatalf("SaveTrip: %v", err)
	}
	if err := s.SetSearch("nope", Search{}); err != ErrNotFound {
		t.Errorf("SetSearch on a missing trip: got %v, want ErrNotFound", err)
	}
	if err := s.SetSearch("a", Search{StartedAt: 7}); err != nil {
		t.Fatalf("SetSearch: %v", err)
	}
	if err := s.ClearSearch("a"); err != nil {
		t.Fatalf("ClearSearch: %v", err)
	}
	got, _ := s.GetTrip("a")
	if got.Search != nil {
		t.Errorf("search still present after ClearSearch: %+v", got.Search)
	}
}

func TestDeleteTrip(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.SaveTrip(sampleTrip("a"), 0); err != nil {
		t.Fatalf("SaveTrip: %v", err)
	}
	if err := s.DeleteTrip("nope"); err != ErrNotFound {
		t.Errorf("deleting a missing trip: got %v, want ErrNotFound", err)
	}
	if err := s.DeleteTrip("a"); err != nil {
		t.Fatalf("DeleteTrip: %v", err)
	}
	if n := len(s.ListTrips()); n != 0 {
		t.Errorf("got %d trips after delete, want 0", n)
	}
}

func TestPersistsAcrossReopen(t *testing.T) {
	s, path := newTestStore(t)
	if _, err := s.SaveTrip(sampleTrip("a"), 0); err != nil {
		t.Fatalf("SaveTrip: %v", err)
	}

	reopened, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := reopened.ListTrips()
	if len(got) != 1 || got[0].Name != "a" || len(got[0].Cities) != 2 {
		t.Errorf("reopened store lost data: %+v", got)
	}
}

// An existing deployment's file holds a photo gallery and chat history that
// this version no longer shows. Writing a trip must carry them through
// untouched rather than dropping the group's history from the volume.
func TestLegacyPhotoAndChatDataSurvivesASave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trip.json")
	legacy := `{"photos":[{"id":"p1","name":"Ana","city":"Lisboa","url":"/uploads/x.jpg","ts":1}],` +
		`"messages":[{"name":"Ana","text":"oi","ts":2}]}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	s, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := s.SaveTrip(sampleTrip("a"), 0); err != nil {
		t.Fatalf("SaveTrip: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var back struct {
		Trips    []Trip            `json:"trips"`
		Photos   []json.RawMessage `json:"photos"`
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back.Trips) != 1 {
		t.Errorf("got %d trips, want 1", len(back.Trips))
	}
	if len(back.Photos) != 1 {
		t.Errorf("photo history was dropped by the save: %s", raw)
	}
	if len(back.Messages) != 1 {
		t.Errorf("chat history was dropped by the save: %s", raw)
	}
}
