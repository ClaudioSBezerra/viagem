// Package quotes fetches nightly hotel prices from Booking.com search pages.
//
// The site is static, so a price baked into the HTML would go stale within
// hours. Instead the server refetches on a schedule and the page reads the
// cached result — every quote carries the timestamp it was captured at, and a
// failed fetch leaves the existing deep link as the fallback.
package quotes

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// maxBody caps how much of a search page we read; prices sit well within the
// first chunk and Booking pages run to several MB of tracking markup.
const maxBody = 3 * 1024 * 1024

// Spec describes one hotel stay to price.
type Spec struct {
	ID       string
	Label    string
	City     string
	Checkin  string
	Checkout string
	Adults   int
	Rooms    int
}

// Quote is the result of pricing a Spec. Price is empty when the fetch or the
// parse failed, in which case Err says why.
type Quote struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	City     string `json:"city"`
	Price    string `json:"price,omitempty"`
	Nights   int    `json:"nights,omitempty"`
	URL      string `json:"url"`
	Source   string `json:"source"`
	Strategy string `json:"strategy,omitempty"`
	Err      string `json:"error,omitempty"`
	Ts       int64  `json:"ts"`
}

// Fetcher prices Specs against Booking.com.
type Fetcher struct {
	client *http.Client
}

func NewFetcher() *Fetcher {
	return &Fetcher{
		client: &http.Client{Timeout: 25 * time.Second},
	}
}

// SearchURL builds the Booking.com search URL for a stay. It is also what the
// page links to, so the button and the scraped price always agree.
func SearchURL(s Spec) string {
	q := url.Values{}
	q.Set("ss", s.Label)
	q.Set("checkin", s.Checkin)
	q.Set("checkout", s.Checkout)
	q.Set("group_adults", strconv.Itoa(s.Adults))
	q.Set("no_rooms", strconv.Itoa(s.Rooms))
	q.Set("group_children", "0")
	q.Set("selected_currency", "BRL")
	q.Set("lang", "pt-br")
	return "https://www.booking.com/searchresults.html?" + q.Encode()
}

// Nights returns the stay length, or 0 if the dates don't parse.
func (s Spec) Nights() int {
	in, err := time.Parse("2006-01-02", s.Checkin)
	if err != nil {
		return 0
	}
	out, err := time.Parse("2006-01-02", s.Checkout)
	if err != nil {
		return 0
	}
	return int(out.Sub(in).Hours() / 24)
}

// Fetch prices a single stay. It always returns a Quote: on failure the Quote
// carries Err and an empty Price, so the caller can cache the attempt and the
// page can fall back to the plain link.
func (f *Fetcher) Fetch(ctx context.Context, s Spec) Quote {
	q := Quote{
		ID:     s.ID,
		Label:  s.Label,
		City:   s.City,
		Nights: s.Nights(),
		URL:    SearchURL(s),
		Source: "booking",
		Ts:     time.Now().UnixMilli(),
	}

	body, err := f.get(ctx, q.URL)
	if err != nil {
		q.Err = err.Error()
		return q
	}

	price, strategy := ExtractPrice(body)
	if price == "" {
		if looksBlocked(body) {
			q.Err = "booking respondeu com pagina de verificacao (anti-bot)"
		} else {
			q.Err = "preco nao encontrado no HTML"
		}
		return q
	}

	q.Price = price
	q.Strategy = strategy
	return q
}

// Probe fetches a URL and reports what came back, without parsing. It backs the
// debug endpoint: when a selector stops matching, this is what tells us whether
// the page was blocked, redirected, or simply restructured.
func (f *Fetcher) Probe(ctx context.Context, target string) map[string]any {
	out := map[string]any{"url": target}

	body, err := f.get(ctx, target)
	if err != nil {
		out["error"] = err.Error()
		return out
	}

	price, strategy := ExtractPrice(body)
	out["bytes"] = len(body)
	out["blocked_markers"] = looksBlocked(body)
	out["price"] = price
	out["strategy"] = strategy
	out["snippet"] = snippet(body, 1200)
	return out
}

func (f *Fetcher) get(ctx context.Context, target string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", err
	}

	// Booking serves a stripped page to obvious bots; a plain browser header set
	// is enough to get the normal server-rendered results.
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "pt-BR,pt;q=0.9,en;q=0.8")
	req.Header.Set("Cache-Control", "no-cache")

	resp, err := f.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("http %d", resp.StatusCode)
	}
	return string(raw), nil
}

func snippet(body string, n int) string {
	body = strings.TrimSpace(body)
	if len(body) <= n {
		return body
	}
	return body[:n]
}
