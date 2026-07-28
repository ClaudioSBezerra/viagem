package quotes

import (
	"html"
	"net/url"
	"os"
	"regexp"
	"testing"
)

var bookingLinkRe = regexp.MustCompile(`https://www\.booking\.com/searchresults\.html\?[^"]+`)

// The stay table and the hotel cards in the page are two copies of the same
// itinerary. This locks them together: a date edited in one place and not the
// other would otherwise quietly price the wrong nights.
func TestStaysMatchThePage(t *testing.T) {
	raw, err := os.ReadFile("../../web/index.html")
	if err != nil {
		t.Fatalf("read page: %v", err)
	}

	links := bookingLinkRe.FindAllString(string(raw), -1)
	if len(links) != len(Stays) {
		t.Fatalf("page has %d booking links, stay table has %d entries", len(links), len(Stays))
	}

	byLabel := map[string]Spec{}
	for _, s := range Stays {
		byLabel[s.Label] = s
	}

	for _, link := range links {
		u, err := url.Parse(html.UnescapeString(link))
		if err != nil {
			t.Fatalf("parse %q: %v", link, err)
		}
		q := u.Query()
		label := q.Get("ss")

		s, ok := byLabel[label]
		if !ok {
			t.Errorf("page prices %q but no stay in the table matches it", label)
			continue
		}
		if got := q.Get("checkin"); got != s.Checkin {
			t.Errorf("%s: page checkin %q, table %q", label, got, s.Checkin)
		}
		if got := q.Get("checkout"); got != s.Checkout {
			t.Errorf("%s: page checkout %q, table %q", label, got, s.Checkout)
		}
		if got := q.Get("group_adults"); got != "2" {
			t.Errorf("%s: page group_adults %q, want 2", label, got)
		}
		if got := q.Get("no_rooms"); got != "1" {
			t.Errorf("%s: page no_rooms %q, want 1", label, got)
		}
	}
}

func TestStayByID(t *testing.T) {
	if s, ok := StayByID("granada"); !ok || s.City != "Granada" {
		t.Errorf("StayByID(granada) = %+v, %v", s, ok)
	}
	if _, ok := StayByID("nao-existe"); ok {
		t.Error("StayByID should report a miss for an unknown id")
	}
}

func TestStayIDsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range Stays {
		if seen[s.ID] {
			t.Errorf("duplicate stay id %q", s.ID)
		}
		seen[s.ID] = true
	}
}
