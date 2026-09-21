package flights

import (
	"encoding/json"
	"testing"
)

// A trimmed SerpApi google_flights option: one connection, LATAM then American.
const connectingOption = `{
  "flights": [
    {"departure_airport":{"name":"Santa Genoveva","id":"GYN","time":"2027-05-04 06:10"},
     "arrival_airport":{"name":"Guarulhos","id":"GRU","time":"2027-05-04 08:00"},
     "duration":110,"airline":"LATAM","flight_number":"LA 3111","airplane":"Airbus A320"},
    {"departure_airport":{"name":"Guarulhos","id":"GRU","time":"2027-05-04 10:30"},
     "arrival_airport":{"name":"Miami","id":"MIA","time":"2027-05-04 18:15"},
     "duration":525,"airline":"American","flight_number":"AA 930"}
  ],
  "total_duration":785,
  "price":4321,
  "departure_token":"tok"
}`

func TestLegKeepsAirlinesNumbersAndTimes(t *testing.T) {
	var o flightOption
	if err := json.Unmarshal([]byte(connectingOption), &o); err != nil {
		t.Fatal(err)
	}
	l := o.leg()
	if l == nil || len(l.Segments) != 2 {
		t.Fatalf("leg = %+v, want 2 segments", l)
	}
	if l.Minutes != 785 {
		t.Errorf("Minutes = %d, want 785", l.Minutes)
	}
	want := Segment{Airline: "LATAM", Number: "LA 3111", From: "GYN", Depart: "2027-05-04 06:10",
		To: "GRU", Arrive: "2027-05-04 08:00", Minutes: 110}
	if l.Segments[0] != want {
		t.Errorf("segment 0 = %+v, want %+v", l.Segments[0], want)
	}
	if l.Segments[1].Airline != "American" || l.Segments[1].To != "MIA" {
		t.Errorf("segment 1 = %+v", l.Segments[1])
	}
}

func TestLegNilWithoutFlightDetail(t *testing.T) {
	if l := (flightOption{Price: 100}).leg(); l != nil {
		t.Errorf("leg = %+v, want nil", l)
	}
}

func TestQuoteCloneSharesNoSegments(t *testing.T) {
	q := Quote{Outbound: &Leg{Segments: []Segment{{Airline: "LATAM"}}}}
	c := q.Clone()
	c.Outbound.Segments[0].Airline = "X"
	if q.Outbound.Segments[0].Airline != "LATAM" {
		t.Error("clone aliases the original's segments")
	}
}
