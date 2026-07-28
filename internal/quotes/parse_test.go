package quotes

import (
	"strings"
	"testing"
)

func TestExtractPriceStrategies(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		wantPrice    string
		wantStrategy string
	}{
		{
			name:         "exact test id",
			body:         `<html><body><div data-testid="price-and-discounted-price"><span>R$&nbsp;2.480</span></div></body></html>`,
			wantPrice:    "R$ 2.480",
			wantStrategy: "data-testid-exact",
		},
		{
			name:         "renamed test id still contains price",
			body:         `<html><body><span data-testid="unit-price-total">R$ 1.199,90</span></body></html>`,
			wantPrice:    "R$ 1.199,90",
			wantStrategy: "data-testid-contains",
		},
		{
			name:         "aria label carries the price",
			body:         `<html><body><div aria-label="Preço atual R$ 3.150"><span>ver</span></div></body></html>`,
			wantPrice:    "R$ 3.150",
			wantStrategy: "aria-label",
		},
		{
			name:         "plain text fallback",
			body:         `<html><body><p>Diaria a partir de R$ 890 para 2 adultos</p></body></html>`,
			wantPrice:    "R$ 890",
			wantStrategy: "regex-text",
		},
		{
			name:         "euro prices are supported",
			body:         `<html><body><div data-testid="price-and-discounted-price">€ 420</div></body></html>`,
			wantPrice:    "€ 420",
			wantStrategy: "data-testid-exact",
		},
		{
			name:         "literal nbsp between symbol and digits",
			body:         "<html><body><div data-testid=\"price-and-discounted-price\">R$\u00a01.050</div></body></html>",
			wantPrice:    "R$ 1.050",
			wantStrategy: "data-testid-exact",
		},
		{
			name: "price split across nested spans",
			body: `<html><body><div data-testid="price-and-discounted-price">` +
				`<span>R$</span><span>&nbsp;</span><span>4.320</span></div></body></html>`,
			wantPrice:    "R$ 4.320",
			wantStrategy: "data-testid-exact",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			price, strategy := ExtractPrice(tt.body)
			if price != tt.wantPrice {
				t.Errorf("price = %q, want %q", price, tt.wantPrice)
			}
			if strategy != tt.wantStrategy {
				t.Errorf("strategy = %q, want %q", strategy, tt.wantStrategy)
			}
		})
	}
}

// Booking footers carry token amounts like "R$ 0" and tax blurbs; those must
// not be mistaken for the headline price.
func TestExtractPriceIgnoresTinyAmounts(t *testing.T) {
	body := `<html><body><span>taxa R$ 0</span><span>desconto R$ 12</span></body></html>`
	if price, _ := ExtractPrice(body); price != "" {
		t.Errorf("price = %q, want empty", price)
	}
}

func TestExtractPricePrefersRealPriceOverNoise(t *testing.T) {
	body := `<html><body><span>taxa R$ 0</span>` +
		`<div data-testid="price-and-discounted-price">R$ 2.100</div></body></html>`
	price, strategy := ExtractPrice(body)
	if price != "R$ 2.100" {
		t.Errorf("price = %q, want R$ 2.100", price)
	}
	if strategy != "data-testid-exact" {
		t.Errorf("strategy = %q, want data-testid-exact", strategy)
	}
}

func TestExtractPriceEmptyWhenNoPrice(t *testing.T) {
	if price, strategy := ExtractPrice(`<html><body><p>sem precos</p></body></html>`); price != "" || strategy != "" {
		t.Errorf("got (%q, %q), want empty", price, strategy)
	}
}

func TestLooksBlocked(t *testing.T) {
	if !looksBlocked(`<html><body><div id="px-captcha"></div></body></html>`) {
		t.Error("expected captcha page to be detected as blocked")
	}
	if looksBlocked(`<html><body>resultados</body></html>`) {
		t.Error("expected normal page not to be flagged")
	}
}

func TestSearchURLCarriesStayParams(t *testing.T) {
	got := SearchURL(Spec{
		Label: "Sana Rex Hotel Lisboa", Checkin: "2026-10-15", Checkout: "2026-10-17",
		Adults: 2, Rooms: 1,
	})
	for _, want := range []string{
		"ss=Sana+Rex+Hotel+Lisboa",
		"checkin=2026-10-15",
		"checkout=2026-10-17",
		"group_adults=2",
		"no_rooms=1",
		"selected_currency=BRL",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("URL %q missing %q", got, want)
		}
	}
}

func TestNights(t *testing.T) {
	s := Spec{Checkin: "2026-10-15", Checkout: "2026-10-17"}
	if n := s.Nights(); n != 2 {
		t.Errorf("Nights() = %d, want 2", n)
	}
	if n := (Spec{Checkin: "nope"}).Nights(); n != 0 {
		t.Errorf("Nights() on bad date = %d, want 0", n)
	}
}
