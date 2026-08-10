package miami

import "testing"

func TestWindowRejectsBadNights(t *testing.T) {
	if _, err := Window("2027-05-01", "2027-05-31", MinNights-1); err == nil {
		t.Error("expected error for nights below MinNights")
	}
	if _, err := Window("2027-05-01", "2027-05-31", MaxNights+1); err == nil {
		t.Error("expected error for nights above MaxNights")
	}
}

func TestWindowRejectsBadDates(t *testing.T) {
	if _, err := Window("not-a-date", "2027-05-31", 7); err == nil {
		t.Error("expected error for an invalid start date")
	}
	if _, err := Window("2027-05-01", "not-a-date", 7); err == nil {
		t.Error("expected error for an invalid end date")
	}
}

func TestWindowRejectsTightWindow(t *testing.T) {
	// 6 days between start/end can't fit a 7-night stay.
	if _, err := Window("2027-05-01", "2027-05-07", 7); err == nil {
		t.Error("expected error when the window is shorter than the requested stay")
	}
}

func TestWindowExactFit(t *testing.T) {
	// Window is exactly as long as the stay: only one valid departure date.
	got, err := Window("2027-05-01", "2027-05-08", 7)
	if err != nil {
		t.Fatalf("Window: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1", len(got))
	}
	if got[0].Depart != "2027-05-01" || got[0].Return != "2027-05-08" {
		t.Errorf("got %+v", got[0])
	}
}

func TestWindowCapsAtMaxCandidates(t *testing.T) {
	// A whole month with a short stay has far more than MaxCandidates valid
	// departure dates.
	got, err := Window("2027-05-01", "2027-05-31", 3)
	if err != nil {
		t.Fatalf("Window: %v", err)
	}
	if len(got) > MaxCandidates {
		t.Fatalf("got %d candidates, want at most %d", len(got), MaxCandidates)
	}
	if len(got) < 2 {
		t.Fatalf("got %d candidates, want more than one for a month-long window", len(got))
	}

	first, last := got[0], got[len(got)-1]
	if first.Depart != "2027-05-01" {
		t.Errorf("first candidate departs %s, want the earliest possible date", first.Depart)
	}
	if last.Depart != "2027-05-28" {
		t.Errorf("last candidate departs %s, want the latest possible date (2027-05-28)", last.Depart)
	}

	// Every candidate must actually fit inside the window and have the
	// right length.
	for _, c := range got {
		if c.Return <= c.Depart {
			t.Errorf("candidate %+v has return before/equal to depart", c)
		}
	}
}

func TestWindowCandidatesAreSortedAndUnique(t *testing.T) {
	got, err := Window("2027-05-01", "2027-05-31", 7)
	if err != nil {
		t.Fatalf("Window: %v", err)
	}
	seen := map[string]bool{}
	for i, c := range got {
		if seen[c.Depart] {
			t.Errorf("duplicate departure date %s", c.Depart)
		}
		seen[c.Depart] = true
		if i > 0 && c.Depart <= got[i-1].Depart {
			t.Errorf("candidates not strictly increasing at index %d: %s after %s", i, c.Depart, got[i-1].Depart)
		}
	}
}

func TestOriginsKnownToServeMiami(t *testing.T) {
	for _, code := range []string{"GYN", "BSB"} {
		if _, ok := Origins[code]; !ok {
			t.Errorf("Origins missing %s", code)
		}
	}
}
