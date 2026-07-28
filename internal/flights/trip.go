package flights

import "time"

// Routes lists the trip's long-haul legs priced via FlightAPI. There is only
// one: Goiânia (GYN) <-> Lisboa (LIS), the outbound on 14 out and the return
// on 31 out 2026 — the dates mirror the flight cards in web/index.html.
// Adults matches the party size used for the hotel quotes (2 passageiros).
var Routes = []Spec{
	{ID: "gyn-lis", Label: "Goiânia → Lisboa (ida e volta)", Origin: "GYN", Dest: "LIS", Depart: "2026-10-14", Return: "2026-10-31", Adults: 2, Currency: "BRL"},
}

// RouteByID returns the route with the given ID.
func RouteByID(id string) (Spec, bool) {
	for _, r := range Routes {
		if r.ID == id {
			return r, true
		}
	}
	return Spec{}, false
}

// ShiftedRoutes returns Routes with every date moved by deltaDays and the ID
// suffixed "-alt", so results land in the cache under their own keys instead
// of overwriting the primary-date quote.
func ShiftedRoutes(deltaDays int) []Spec {
	out := make([]Spec, len(Routes))
	for i, r := range Routes {
		r.ID += "-alt"
		r.Depart = shiftDate(r.Depart, deltaDays)
		r.Return = shiftDate(r.Return, deltaDays)
		out[i] = r
	}
	return out
}

func shiftDate(d string, days int) string {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return d
	}
	return t.AddDate(0, 0, days).Format("2006-01-02")
}
