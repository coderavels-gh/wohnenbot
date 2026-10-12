package analyze

import (
	"testing"
	"time"

	"aptbot/internal/config"
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

func TestMoveIn(t *testing.T) {
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	from := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		avail    string
		flexible bool
		want     listing.Result
	}{
		{"01.12.2026", false, listing.Pass},
		{"31.12.2026", false, listing.Pass},
		{"01.01.2027", false, listing.Fail},
		{"15.10.2026", false, listing.Unknown}, // earlier: ask the landlord
		{"15.10.2026", true, listing.Pass},     // City-Wohnen style window
		{"sofort", false, listing.Unknown},
		{"nach Vereinbarung", false, listing.Unknown},
		{"", false, listing.Unknown},
	}
	for _, c := range cases {
		l := &listing.Listing{AvailableFrom: c.avail, FlexibleStart: c.flexible}
		if got := checkMoveIn(l, from, to, now); got.Result != c.want {
			t.Errorf("%q flexible=%v → %v want %v (%s)", c.avail, c.flexible, got.Result, c.want, got.Detail)
		}
	}
}

func TestFindAvailableFrom(t *testing.T) {
	cases := map[string]string{
		"Bezugsfrei ab: 01.12.2026":         "01.12.2026",
		"Bezugsfrei ab: sofort":             "sofort",
		"Frei ab nach Vereinbarung":         "nach Vereinbarung",
		"Verfügbar ab 15.12.2026":           "15.12.2026",
		"Kaltmiete 1.100 €, schöne Wohnung": "",
	}
	for in, want := range cases {
		if got := FindAvailableFrom(in); got != want {
			t.Errorf("%q → %q want %q", in, got, want)
		}
	}
}

func TestDurationFromMoveIn(t *testing.T) {
	moveIn := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	if r := checkDuration("ab 12.10.2026 bis 30.11.2027", 12, moveIn).Result; r != listing.Pass {
		t.Errorf("12 months from move-in should pass, got %v", r)
	}
	// 12.10.2026–31.10.2027 is 12.6 months from the window start but 11 from move-in.
	if r := checkDuration("ab 12.10.2026 bis 31.10.2027", 12, moveIn).Result; r != listing.Fail {
		t.Errorf("11 months from move-in should fail, got %v", r)
	}
}

func TestRentLimits(t *testing.T) {
	var cr config.Criteria
	cr.WarmRent.Furnished = config.RentLimit{Max: 1700, ReviewUpTo: 1800}
	cr.WarmRent.Unfurnished = config.RentLimit{Max: 1400}
	cases := []struct {
		rent      float64
		furnished bool
		want      listing.Result
	}{
		{1700, true, listing.Pass},
		{1790, true, listing.Unknown}, // over budget but within the stretch → REVIEW
		{1800, true, listing.Unknown},
		{1801, true, listing.Fail},
		{1400, false, listing.Pass},
		{1450, false, listing.Fail}, // no stretch for unfurnished
		{0, true, listing.Unknown},  // rent not found
	}
	for _, c := range cases {
		l := &listing.Listing{WarmRent: c.rent, Furnished: c.furnished}
		if got := checkRent(l, cr.RentLimit(c.furnished)); got.Result != c.want {
			t.Errorf("%v € furnished=%v → %v want %v (%s)", c.rent, c.furnished, got.Result, c.want, got.Detail)
		}
	}
}

func TestNotInService(t *testing.T) {
	if !notInService(map[string]string{"name": "U Steilshoop (in Bau)", "public_transport": "station"}) {
		t.Error("station under construction counted")
	}
	if !notInService(map[string]string{"railway": "construction", "name": "Bramfeld"}) {
		t.Error("railway=construction counted")
	}
	if notInService(map[string]string{"railway": "station", "station": "subway", "name": "Schlump"}) {
		t.Error("open station excluded")
	}
}
