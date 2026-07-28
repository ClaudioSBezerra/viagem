package flights

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
