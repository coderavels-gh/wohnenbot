package analyze

import (
	"context"
	"path/filepath"
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
		{"Der Mietvertrag wird auf 5 Jahre befristet", d, listing.Pass},
		{"Untermiete: Hafencity ab Nov. für 3-6 Monate. Möbliert mit Balkon.", d, listing.Fail},
		{"Mietdauer: 12–24 Monate", d, listing.Pass},
		{"Zwischenmiete für 6 Monate", d, listing.Fail},
		{"Kaution: 3 Monatsmieten. Mietvertrag unbefristet", d, listing.Pass},
		{"Die Wohnung ist auf 6 Monate befristet", d, listing.Fail},
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
		avail  string
		strict bool
		want   listing.Result
	}{
		{"01.12.2026", false, listing.Pass},
		{"31.12.2026", false, listing.Pass},
		{"01.01.2027", false, listing.Fail},
		{"15.10.2026", false, listing.Unknown}, // earlier: ask the landlord
		{"15.10.2026", true, listing.Fail},     // City-Wohnen: earlier start is a rejection
		{"sofort", false, listing.Unknown},
		{"nach Vereinbarung", false, listing.Unknown},
		{"", false, listing.Unknown},
	}
	for _, c := range cases {
		l := &listing.Listing{AvailableFrom: c.avail, StrictStart: c.strict}
		if got := checkMoveIn(l, from, to, now); got.Result != c.want {
			t.Errorf("%q strict=%v → %v want %v (%s)", c.avail, c.strict, got.Result, c.want, got.Detail)
		}
	}
}

func TestFindAvailableFrom(t *testing.T) {
	cases := map[string]string{
		"Bezugsfrei ab: 01.12.2026":         "01.12.2026",
		"Bezugsfrei ab: sofort":             "sofort",
		"Frei ab nach Vereinbarung":         "nach Vereinbarung",
		"Verfügbar ab 15.12.2026":           "15.12.2026",
		"Bezugsfrei ab:: 1.1.2027":          "1.1.2027", // ImmoScout label ends in ":"
		"Bezugsfrei ab:: 21.10.26":          "21.10.26",
		"Bezugsfrei ab:: nach Absprache":    "nach Absprache",
		"Frei ab: ab sofort":                "sofort",
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

func TestParseDateTwoDigitYear(t *testing.T) {
	got, ok := ParseDate("21.10.26")
	if !ok || got != time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("got %v %v", got, ok)
	}
}

func TestWBS(t *testing.T) {
	cases := map[string]listing.Result{
		"Schöne 2-Zimmer-Wohnung in Neugraben  *nur mit Wohnberechtigungsschein*": listing.Fail,
		"Nur mit WBS!":                           listing.Fail,
		"WBS erforderlich":                       listing.Fail,
		"Ein WBS §5 muss vorliegen":              listing.Fail,
		"Öffentlich geförderte Wohnung":          listing.Fail,
		"Kein WBS erforderlich, frei finanziert": listing.Pass,
		"WBS ist nicht erforderlich":             listing.Pass,
		"Schöne Altbauwohnung mit Balkon":        listing.Pass,
	}
	for text, want := range cases {
		if got := checkWBS(text); got.Result != want {
			t.Errorf("%q → %v want %v (%s)", text, got.Result, want, got.Detail)
		}
	}
}

func TestTransitCachePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	tc := NewTransitChecker(nil, "")
	tc.nearby[stopsKey(53.5753, 9.9530, 1000, 1000)] = stopsResult{Modes: map[string]string{ModeBus: "Schlump"}, At: time.Now()}
	tc.nearby["old"] = stopsResult{Modes: map[string]string{ModeBus: "x"}, At: time.Now().Add(-100 * 24 * time.Hour)}
	tc.dirty = true
	if err := tc.SaveCache(path); err != nil {
		t.Fatal(err)
	}
	tc2 := NewTransitChecker(nil, "") // nil client: a cache miss would panic
	tc2.LoadCache(path)
	// A point ~30 m away shares the entry; no network request is made.
	modes, err := tc2.stopsNear(context.Background(), 53.5755, 9.9532, 1000, 1000)
	if err != nil || modes[ModeBus] != "Schlump" {
		t.Fatalf("modes=%v err=%v", modes, err)
	}
	if _, ok := tc2.nearby["old"]; ok {
		t.Error("expired entry loaded")
	}
}

func TestAreaInRunningText(t *testing.T) {
	if a := FindArea("Helle Wohnung, 62 m², ruhig gelegen"); a != 62 {
		t.Fatalf("area %v", a)
	}
}

func TestCommuteRule(t *testing.T) {
	cr := config.Criteria{CommuteHbf: true, MinTransitModes: 2, RailRadiusM: 1000, BusRadiusM: 1000}
	osm := modesSource{osm: true}
	text := modesSource{note: " (from listing text)"}
	cases := []struct {
		name  string
		modes map[string]string
		where modesSource
		want  listing.Result
	}{
		{"U-Bahn + bus", map[string]string{ModeUBahn: "Schlump", ModeBus: "x"}, osm, listing.Pass},
		{"bus + ferry", map[string]string{ModeFerry: "Finkenwerder", ModeBus: "y"}, osm, listing.Pass},
		{"S-Bahn + U-Bahn", map[string]string{ModeSBahn: "a", ModeUBahn: "b"}, osm, listing.Pass},
		{"S-Bahn only: no fallback", map[string]string{ModeSBahn: "Sternschanze"}, osm, listing.Fail},
		{"bus only", map[string]string{ModeBus: "Moränenweg"}, osm, listing.Fail},
		{"nothing", map[string]string{}, osm, listing.Fail},
		{"text: S-Bahn + bus", map[string]string{ModeSBahn: "S1", ModeBus: "Bus 5"}, text, listing.Pass},
		{"text: S-Bahn only", map[string]string{ModeSBahn: "S1"}, text, listing.Unknown},
	}
	for _, c := range cases {
		if got := commuteCheck(c.modes, c.where, cr); got.Result != c.want {
			t.Errorf("%s: %v want %v (%s)", c.name, got.Result, c.want, got.Detail)
		}
	}
	if m := modesFromText("Lage: 5 Minuten zum HADAG-Fähranleger, Bus 150"); !hasMode(m, ModeFerry) || !hasMode(m, ModeBus) {
		t.Errorf("text modes %v", m)
	}
	if got := classifyStop(map[string]string{"amenity": "ferry_terminal", "name": "Neumühlen/Övelgönne"}); len(got) != 1 || got[0] != ModeFerry {
		t.Errorf("ferry terminal classified %v", got)
	}
}

func TestCityRule(t *testing.T) {
	cases := []struct {
		l    listing.Listing
		want listing.Result
	}{
		{listing.Listing{Address: "22045 Hamburg, Tonndorf (unvollständige Adresse)"}, listing.Pass},
		{listing.Listing{Address: "20357 Hamburg", District: "Eimsbüttel"}, listing.Pass},
		{listing.Listing{Address: "Emilienstraße, Hamburg"}, listing.Pass},
		{listing.Listing{Address: "22846 Norderstedt, Garstedt"}, listing.Fail},
		{listing.Listing{Address: "25421 Pinneberg"}, listing.Fail},
		{listing.Listing{}, listing.Pass},
	}
	for _, c := range cases {
		if got := checkCity(&c.l, "Hamburg"); got.Result != c.want {
			t.Errorf("%q → %v want %v (%s)", c.l.Address, got.Result, c.want, got.Detail)
		}
	}
}

func TestRentLowerBound(t *testing.T) {
	lim := config.RentLimit{Max: 1400, ReviewUpTo: 1500}
	if r := checkRent(&listing.Listing{WarmRent: 1200, RentLowerBound: true}, lim).Result; r != listing.Unknown {
		t.Errorf("cold 1200 only: %v, want unknown", r)
	}
	if r := checkRent(&listing.Listing{WarmRent: 1600, RentLowerBound: true}, lim).Result; r != listing.Fail {
		t.Errorf("cold 1600 only: %v, want fail", r)
	}
}
