package quotes

import (
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// priceRe matches a currency marker followed by a pt-BR or European formatted
// amount: "R$ 1.234", "R$&nbsp;1.234,56", "€ 980".
var priceRe = regexp.MustCompile(`(R\$|US\$|€)\s*([0-9]{1,3}(?:\.[0-9]{3})+(?:,[0-9]{2})?|[0-9]+(?:,[0-9]{2})?)`)

// minPrice filters out matches like "R$ 0" or a stray "R$ 12" from a footer
// disclaimer. Any real multi-night stay clears this comfortably.
const minPrice = 50

var blockMarkers = []string{
	"captcha",
	"unusual traffic",
	"are you a robot",
	"verifique que voc",
	"px-captcha",
}

// ExtractPrice pulls the headline nightly/stay price out of a Booking.com
// search page, returning the price and the name of the strategy that matched.
// Booking reshuffles its markup often, so this tries several increasingly loose
// approaches rather than betting on one selector.
func ExtractPrice(body string) (price, strategy string) {
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		// Fall through to the raw-text scan; a parse error still leaves the
		// original bytes searchable.
		if p := firstPrice(body); p != "" {
			return p, "regex-raw"
		}
		return "", ""
	}

	// 1. The current price element, most precise when it is present.
	if el := findByAttr(doc, "data-testid", "price-and-discounted-price"); el != nil {
		if p := firstPrice(textOf(el)); p != "" {
			return p, "data-testid-exact"
		}
	}

	// 2. Any test id mentioning price — survives a rename of the exact value.
	if el := findByAttrContains(doc, "data-testid", "price"); el != nil {
		if p := firstPrice(textOf(el)); p != "" {
			return p, "data-testid-contains"
		}
	}

	// 3. Screen-reader labels carry the price when the visual markup nests it
	//    across several spans.
	if el := findByAttrPrice(doc, "aria-label"); el != nil {
		if p := firstPrice(attr(el, "aria-label")); p != "" {
			return p, "aria-label"
		}
	}

	// 4. Last resort: first plausible price anywhere in the rendered text.
	if p := firstPrice(textOf(doc)); p != "" {
		return p, "regex-text"
	}
	return "", ""
}

// firstPrice returns the first currency amount in s that clears minPrice,
// normalised to a single-space form like "R$ 1.234".
func firstPrice(s string) string {
	s = normalizeSpace(s)
	for _, m := range priceRe.FindAllStringSubmatch(s, -1) {
		if amountValue(m[2]) < minPrice {
			continue
		}
		return m[1] + " " + m[2]
	}
	return ""
}

// amountValue converts a pt-BR formatted amount ("1.234,56") to a float for
// magnitude checks. Returns 0 when it cannot be read.
func amountValue(s string) float64 {
	s = strings.ReplaceAll(s, ".", "")
	s = strings.ReplaceAll(s, ",", ".")
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return v
}

// normalizeSpace collapses non-breaking spaces and runs of whitespace, so the
// price regex sees "R$ 1.234" whether the page wrote a space, an &nbsp;, or a
// newline between the symbol and the digits.
func normalizeSpace(s string) string {
	s = strings.ReplaceAll(s, "\u00a0", " ")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	return strings.Join(strings.Fields(s), " ")
}

func looksBlocked(body string) bool {
	low := strings.ToLower(body)
	for _, m := range blockMarkers {
		if strings.Contains(low, m) {
			return true
		}
	}
	return false
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func findByAttr(n *html.Node, key, want string) *html.Node {
	return find(n, func(el *html.Node) bool { return attr(el, key) == want })
}

func findByAttrContains(n *html.Node, key, want string) *html.Node {
	return find(n, func(el *html.Node) bool {
		v := attr(el, key)
		return v != "" && strings.Contains(strings.ToLower(v), want)
	})
}

// findByAttrPrice finds the first element whose given attribute holds something
// that parses as a price.
func findByAttrPrice(n *html.Node, key string) *html.Node {
	return find(n, func(el *html.Node) bool { return firstPrice(attr(el, key)) != "" })
}

func find(n *html.Node, match func(*html.Node) bool) *html.Node {
	if n.Type == html.ElementNode && match(n) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if hit := find(c, match); hit != nil {
			return hit
		}
	}
	return nil
}

// textOf concatenates the text content of a node and its descendants, skipping
// script and style bodies so their JSON payloads don't pollute the scan.
func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
			return
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			b.WriteString(" ")
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}
