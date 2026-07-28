package quotes

import "time"

// Stays lists the hotels of the Iberian itinerary in travel order. The dates
// mirror the cards in web/index.html — when a stay moves there, it has to move
// here too, or the quote will price the wrong nights.
//
// Adults/Rooms are 2/1: the group quotes were switched to two passengers.
var Stays = []Spec{
	{ID: "lisboa", City: "Lisboa", Label: "Sana Rex Hotel Lisboa", Checkin: "2026-10-15", Checkout: "2026-10-17", Adults: 2, Rooms: 1},
	{ID: "porto", City: "Porto", Label: "Bessahotel Boavista Porto", Checkin: "2026-10-17", Checkout: "2026-10-19", Adults: 2, Rooms: 1},
	{ID: "coruna", City: "A Coruña", Label: "Hesperia A Coruña Centro", Checkin: "2026-10-19", Checkout: "2026-10-20", Adults: 2, Rooms: 1},
	{ID: "madrid", City: "Madrid", Label: "Hotel Madrid Centro Affiliated by Melia", Checkin: "2026-10-20", Checkout: "2026-10-22", Adults: 2, Rooms: 1},
	{ID: "granada", City: "Granada", Label: "Porcel Alixares Granada", Checkin: "2026-10-22", Checkout: "2026-10-24", Adults: 2, Rooms: 1},
	{ID: "torremolinos", City: "Torremolinos", Label: "Hotel Los Jazmines Torremolinos", Checkin: "2026-10-24", Checkout: "2026-10-27", Adults: 2, Rooms: 1},
	{ID: "sevilha", City: "Sevilha", Label: "Duquesa Sevilla Hotel", Checkin: "2026-10-27", Checkout: "2026-10-29", Adults: 2, Rooms: 1},
	{ID: "salamanca", City: "Salamanca", Label: "Hotel San Polo Salamanca", Checkin: "2026-10-29", Checkout: "2026-10-31", Adults: 2, Rooms: 1},
}

// StayByID returns the stay with the given ID.
func StayByID(id string) (Spec, bool) {
	for _, s := range Stays {
		if s.ID == id {
			return s, true
		}
	}
	return Spec{}, false
}

// ShiftedStays returns Stays with every date moved by deltaDays and the ID
// suffixed "-alt", so results land in the cache under their own keys instead
// of overwriting the primary-date quotes.
func ShiftedStays(deltaDays int) []Spec {
	out := make([]Spec, len(Stays))
	for i, s := range Stays {
		s.ID += "-alt"
		s.Checkin = shiftDate(s.Checkin, deltaDays)
		s.Checkout = shiftDate(s.Checkout, deltaDays)
		out[i] = s
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
