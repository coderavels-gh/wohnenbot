package analyze

import (
	"testing"
	"time"

	"aptbot/internal/listing"
)

func TestParseGermanNumber(t *testing.T) {
	cases := map[string]float64{"1.550": 1550, "45,19": 45.19, "1.550,50": 1550.5, "2.5": 2.5, "2": 2, "12.000": 12000}
	for in, want := range cases {
		if got, ok := ParseGermanNumber(in); !ok || got != want {
			t.Errorf("%s → %v want %v", in, got, want)
		}
	}
}

func TestWarmRent(t *testing.T) {
	cases := []struct {
		text string
		want float64
	}{
		{"Kaltmiete: 1.100 €\nNebenkosten: 180 €\nHeizkosten: 90 €", 1370},
		{"Kaltmiete: 1.100 €\nNebenkosten: 250 €\nHeizkosten: 90 €\nHeizkosten in Nebenkosten enthalten", 1350},
		{"Warmmiete 1.420 €", 1420},
		{"Kaltmiete: 1.100 €", 0},
	}
	for _, c := range cases {
		if got := FindWarmRent(c.text, "").Warm; got != c.want {
			t.Errorf("%q → %v want %v", c.text, got, c.want)
		}
	}
}

func TestRoomsNotFromRoomNames(t *testing.T) {
	if r := FindRooms("Wohnzimmer mit Balkon 11 m²\n3 Zimmer"); r != 3 {
		t.Fatalf("rooms %v", r)
	}
}

func TestFeatures(t *testing.T) {
	now := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	type tc struct {
		text string
		f    func(string) listing.Result
		want listing.Result
	}
	b := func(s string) listing.Result { return checkBalcony(s).Result }
	w := func(s string) listing.Result { return checkWasher(s, true).Result }
	k := func(s string) listing.Result { return checkKitchen(s).Result }
	a := func(s string) listing.Result { return checkAnmeldung(s).Result }
	d := func(s string) listing.Result { return checkDuration(s, 12, now).Result }
	for _, c := range []tc{
		{"Die Wohnung hat leider keinen Balkon.", b, listing.Fail},
		{"Sonniger Südbalkon", b, listing.Pass},
		{"Details: Balkon/Garten", b, listing.Unknown},
		{"Waschmaschinenanschluss im Bad", w, listing.Pass},
		{"Waschmaschine vorhanden", w, listing.Pass},
		{"Nutzung der Gemeinschaftswaschmaschine", w, listing.Unknown},
		{"Wohnung ohne Einbauküche", k, listing.Fail},
		{"EBK mit Geschirrspüler", k, listing.Pass},
		{"Küche, Bad, Flur", k, listing.Unknown},
		{"Eine Anmeldung ist leider nicht möglich.", a, listing.Fail},
		{"Anmeldung beim Bezirksamt ist möglich", a, listing.Pass},
		{"Mietvertrag unbefristet", d, listing.Pass},
		{"Befristet auf 2 Jahre", d, listing.Pass},
		{"Befristet bis 31.03.2027", d, listing.Fail},
		{"Zwischenmiete für ein Semester", d, listing.Unknown},
		{"Maximale Mietzeit: 6 Kalendermonate", d, listing.Fail},
	} {
		if got := c.f(c.text); got != c.want {
			t.Errorf("%q → %v want %v", c.text, got, c.want)
		}
	}
}

func TestTransitText(t *testing.T) {
	m := modesFromText("Lage: U2, U3, Bus 4 (Schlump 600m)")
	if len(m) != 2 {
		t.Fatalf("modes %v", m)
	}
	m = modesFromText("S-Bahn Holstenstraße und Metrobus 3 in der Nähe, U3 Feldstraße")
	if len(m) != 3 {
		t.Fatalf("modes %v", m)
	}
}

func TestClassifyStop(t *testing.T) {
	if got := classifyStop(map[string]string{"railway": "station", "station": "subway", "name": "Schlump"}); len(got) != 1 || got[0] != ModeUBahn {
		t.Fatal(got)
	}
	if got := classifyStop(map[string]string{"railway": "station", "station": "light_rail", "name": "Sternschanze"}); len(got) != 1 || got[0] != ModeSBahn {
		t.Fatal(got)
	}
	if got := classifyStop(map[string]string{"highway": "bus_stop"}); len(got) != 1 || got[0] != ModeBus {
		t.Fatal(got)
	}
}
