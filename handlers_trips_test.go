package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"viagem/internal/flights"
	"viagem/internal/quotes"
	"viagem/internal/store"
)

func newTestServer(t *testing.T, searchEnabled bool) *server {
	t.Helper()
	st, err := store.New(filepath.Join(t.TempDir(), "trip.json"))
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	return &server{
		store:         st,
		indexPage:     []byte("<html></html>"),
		search:        newSearcher(quotes.NewFetcher(""), flights.NewFetcher(""), st),
		searchEnabled: searchEnabled,
	}
}

func do(t *testing.T, s *server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == nil {
		r = httptest.NewRequest(method, path, nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		r = httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	return w
}

func validBody() map[string]any {
	return map[string]any{
		"name": "Miami maio/2027", "origin": "gyn", "dest": "mia", "adults": 2,
		"windowStart": "2027-05-01", "windowEnd": "2027-05-31",
		"cities": []map[string]any{
			{"name": "Downtown Miami", "nights": 4},
			{"name": "Orlando", "nights": 3},
		},
	}
}

func createTrip(t *testing.T, s *server, body map[string]any) store.Trip {
	t.Helper()
	w := do(t, s, "POST", "/api/trips", body)
	if w.Code != http.StatusOK {
		t.Fatalf("create: got %d, body %s", w.Code, w.Body.String())
	}
	var got store.Trip
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return got
}

func TestCreateTripNormalizesAndStores(t *testing.T) {
	s := newTestServer(t, false)
	got := createTrip(t, s, validBody())

	if got.ID == "" {
		t.Error("no ID assigned")
	}
	// Airport codes are typed by hand, in whatever case; they reach SerpApi
	// uppercased or not at all.
	if got.Origin != "GYN" || got.Dest != "MIA" {
		t.Errorf("airports not normalized: %s -> %s", got.Origin, got.Dest)
	}
	if len(got.Cities) != 2 || got.Cities[0].Name != "Downtown Miami" {
		t.Errorf("cities wrong or reordered: %+v", got.Cities)
	}
}

func TestCreateTripRejects(t *testing.T) {
	cases := map[string]func(m map[string]any){
		"sem nome":              func(m map[string]any) { m["name"] = "  " },
		"origem invalida":       func(m map[string]any) { m["origin"] = "GOIANIA" },
		"destino invalido":      func(m map[string]any) { m["dest"] = "" },
		"origem igual destino":  func(m map[string]any) { m["dest"] = "GYN" },
		"passageiros zero":      func(m map[string]any) { m["adults"] = 0 },
		"passageiros demais":    func(m map[string]any) { m["adults"] = 10 },
		"sem cidades":           func(m map[string]any) { m["cities"] = []map[string]any{} },
		"janela menor que trip": func(m map[string]any) { m["windowEnd"] = "2027-05-03" },
		"data invalida":         func(m map[string]any) { m["windowStart"] = "01/05/2027" },
	}
	for name, mutate := range cases {
		s := newTestServer(t, false)
		body := validBody()
		mutate(body)
		w := do(t, s, "POST", "/api/trips", body)
		if w.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: got %d, want 422 (body %s)", name, w.Code, w.Body.String())
		}
	}
}

func TestUpdateTripClearsStalePricing(t *testing.T) {
	s := newTestServer(t, false)
	created := createTrip(t, s, validBody())
	if err := s.store.SetSearch(created.ID, store.Search{StartedAt: 1, Done: true}); err != nil {
		t.Fatalf("SetSearch: %v", err)
	}

	// Renaming leaves the prices alone: they still describe this trip.
	body := validBody()
	body["name"] = "Miami — outro nome"
	w := do(t, s, "PUT", "/api/trips/"+created.ID, body)
	if w.Code != http.StatusOK {
		t.Fatalf("rename: got %d, body %s", w.Code, w.Body.String())
	}
	if got, _ := s.store.GetTrip(created.ID); got.Search == nil {
		t.Error("renaming dropped the pricing run")
	}

	// Changing a city does not: those prices are for nights nobody is
	// booking any more.
	body = validBody()
	body["cities"] = []map[string]any{{"name": "Tampa", "nights": 7}}
	w = do(t, s, "PUT", "/api/trips/"+created.ID, body)
	if w.Code != http.StatusOK {
		t.Fatalf("edit: got %d, body %s", w.Code, w.Body.String())
	}
	if got, _ := s.store.GetTrip(created.ID); got.Search != nil {
		t.Error("pricing for the old itinerary survived an edit")
	}
}

func TestUpdateTripCannotRetargetAnotherTrip(t *testing.T) {
	s := newTestServer(t, false)
	a := createTrip(t, s, validBody())
	other := validBody()
	other["name"] = "outro"
	b := createTrip(t, s, other)

	// An "id" in the body must be ignored: the URL decides what is written.
	body := validBody()
	body["id"] = b.ID
	body["name"] = "editado"
	if w := do(t, s, "PUT", "/api/trips/"+a.ID, body); w.Code != http.StatusOK {
		t.Fatalf("update: got %d", w.Code)
	}

	gotA, _ := s.store.GetTrip(a.ID)
	gotB, _ := s.store.GetTrip(b.ID)
	if gotA.Name != "editado" {
		t.Errorf("the URL's trip was not updated: %q", gotA.Name)
	}
	if gotB.Name != "outro" {
		t.Errorf("the body's id overwrote a different trip: %q", gotB.Name)
	}
}

func TestMissingTripIs404(t *testing.T) {
	s := newTestServer(t, true)
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/trips/nope"},
		{"DELETE", "/api/trips/nope"},
		{"POST", "/api/trips/nope/search"},
	} {
		w := do(t, s, tc.method, tc.path, nil)
		if tc.method == "PUT" {
			continue
		}
		if w.Code != http.StatusNotFound {
			t.Errorf("%s %s: got %d, want 404", tc.method, tc.path, w.Code)
		}
	}
}

func TestSearchDisabledExplainsItself(t *testing.T) {
	s := newTestServer(t, false)
	created := createTrip(t, s, validBody())

	w := do(t, s, "POST", "/api/trips/"+created.ID+"/search", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503 (body %s)", w.Code, w.Body.String())
	}
	var body map[string]string
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["error"] == "" {
		t.Error("503 carried no explanation")
	}
}

func TestDeleteTripRemovesIt(t *testing.T) {
	s := newTestServer(t, false)
	created := createTrip(t, s, validBody())

	if w := do(t, s, "DELETE", "/api/trips/"+created.ID, nil); w.Code != http.StatusOK {
		t.Fatalf("delete: got %d", w.Code)
	}
	if w := do(t, s, "GET", "/api/trips/"+created.ID, nil); w.Code != http.StatusNotFound {
		t.Errorf("trip still readable after delete: %d", w.Code)
	}
}

// Nothing has been priced yet, so the client must be told it can search now
// rather than being handed a timestamp from year 1.
func TestNextAfterIsZeroBeforeAnySearch(t *testing.T) {
	s := newTestServer(t, true)
	w := do(t, s, "GET", "/api/trips", nil)
	var body struct {
		NextAfter int64 `json:"nextAfter"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.NextAfter != 0 {
		t.Errorf("nextAfter = %d, want 0", body.NextAfter)
	}
}
