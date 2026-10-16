package sources

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"aptbot/internal/analyze"
	"aptbot/internal/config"
	"aptbot/internal/listing"
)

var criteria = config.Criteria{
	MaxWarmRent: 1600, MinRooms: 2, MinSizeM2: 50, MinDurationMonths: 12,
	RequireBalcony: true, RequireWasher: true, RequireKitchen: true, RequireAnmeldung: true,
	WasherConnectionOK: true, MinTransitModes: 2, BusRadiusM: 400, RailRadiusM: 900,
}

func evaluator() *analyze.Evaluator {
	tc := analyze.NewTransitChecker(nil, "")
	tc.Offline = true
	return &analyze.Evaluator{Criteria: criteria, Transit: tc,
		Now: func() time.Time { return time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC) }}
}

func read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func result(t *testing.T, l *listing.Listing, name string) listing.Result {
	t.Helper()
	c, ok := l.Check(name)
	if !ok {
		t.Fatalf("no check %s", name)
	}
	return c.Result
}

func TestHomeCompanyDetail(t *testing.T) {
	l := ParseHomeCompanyDetail(read(t, "homecompany_detail.html"))
	if l.WarmRent != 1550 || l.SizeM2 != 45.19 || l.Rooms != 2 {
		t.Fatalf("numbers: rent=%v size=%v rooms=%v", l.WarmRent, l.SizeM2, l.Rooms)
	}
	if l.District != "Eimsbüttel" || l.AvailableFrom != "01.01.2027" {
		t.Fatalf("district=%q avail=%q", l.District, l.AvailableFrom)
	}
	evaluator().Evaluate(context.Background(), l)
	if l.Verdict != listing.Reject {
		t.Errorf("verdict %s", l.Verdict)
	}
	if r := result(t, l, listing.CritDuration); r != listing.Fail {
		t.Errorf("duration (6 months max) should fail, got %v", r)
	}
	if r := result(t, l, listing.CritSize); r != listing.Fail {
		t.Errorf("size 45 should fail")
	}
	if r := result(t, l, listing.CritBalcony); r != listing.Unknown {
		t.Errorf("balcony must not be picked up from nav/similar listings, got %v", r)
	}
	if r := result(t, l, listing.CritKitchen); r != listing.Pass {
		t.Errorf("kitchen should pass for furnished agency, got %v", r)
	}
}

func TestHomeCompanyTransitFromLage(t *testing.T) {
	l := ParseHomeCompanyDetail(read(t, "homecompany_detail.html"))
	c := evaluator().Transit.Check(context.Background(), l, criteria)
	if c.Result != listing.Pass {
		t.Fatalf("transit: %+v", c)
	}
}

func TestCityWohnenDetail(t *testing.T) {
	l := ParseCityWohnenDetail(read(t, "citywohnen_detail.html"), "https://www.city-wohnen.de/hamburg/50906-moeblierte-wohnung-hamburg-eimsbuettel-emilienstrasse")
	if l.WarmRent != 1550 || l.SizeM2 != 56 || l.Rooms != 2 {
		t.Fatalf("numbers: rent=%v size=%v rooms=%v", l.WarmRent, l.SizeM2, l.Rooms)
	}
	if l.Address != "Emilienstraße, Hamburg" || !l.AddressPrecise || l.District != "Eimsbüttel" {
		t.Fatalf("address=%q precise=%v district=%q", l.Address, l.AddressPrecise, l.District)
	}
	evaluator().Evaluate(context.Background(), l)
	want := map[string]listing.Result{
		listing.CritBalcony:   listing.Pass,
		listing.CritWasher:    listing.Unknown, // "Waschmaschine" tag but shared machine in cellar
		listing.CritDuration:  listing.Fail,    // 01.01.2027 bis 31.03.2027
		listing.CritAnmeldung: listing.Pass,
		listing.CritKitchen:   listing.Pass,
	}
	for k, v := range want {
		if r := result(t, l, k); r != v {
			c, _ := l.Check(k)
			t.Errorf("%s: got %v want %v (%s)", k, r, v, c.Detail)
		}
	}
}

func TestCityWohnenTwelveMonthLease(t *testing.T) {
	page := read(t, "citywohnen_detail.html")
	const window = "ab <b>01.01.2027</b> bis <b>31.03.2027</b>"
	pageURL := "https://www.city-wohnen.de/hamburg/50906-moeblierte-wohnung-hamburg-eimsbuettel-emilienstrasse"
	cases := []struct {
		name   string
		avail  string
		minDur int
		want   listing.Result
	}{
		{"full 12 months", "ab <b>01.11.2026</b> bis <b>31.10.2027</b>", 12, listing.Pass},
		{"no end date", "ab <b>01.11.2026</b>", 12, listing.Pass},
		{"capped at 12 when minimum is higher", "ab <b>01.11.2026</b> bis <b>31.10.2027</b>", 18, listing.Pass},
		{"shorter than 12 months", "ab <b>01.11.2026</b> bis <b>30.04.2027</b>", 12, listing.Fail},
	}
	for _, c := range cases {
		l := ParseCityWohnenDetail(strings.Replace(page, window, c.avail, 1), pageURL)
		ev := evaluator()
		ev.Criteria.MinDurationMonths = c.minDur
		ev.Evaluate(context.Background(), l)
		if r := result(t, l, listing.CritDuration); r != c.want {
			d, _ := l.Check(listing.CritDuration)
			t.Errorf("%s: got %v want %v (%s)", c.name, r, c.want, d.Detail)
		}
	}
}

func TestImmoScout(t *testing.T) {
	items, err := ParseImmoScoutList([]byte(read(t, "immoscout_list.json")))
	if err != nil || len(items) != 2 {
		t.Fatalf("items=%d err=%v", len(items), err)
	}
	l := BuildImmoScoutListing(items[0], []byte(read(t, "immoscout_expose.json")))
	if l.WarmRent != 1480 || l.SizeM2 != 62 || l.Rooms != 2.5 || !l.AddressPrecise || l.District != "Hoheluft-Ost" {
		t.Fatalf("parsed: %+v", l)
	}
	evaluator().Evaluate(context.Background(), l)
	if l.Verdict != listing.Match {
		for _, c := range l.Checks {
			t.Logf("%s %s %s", c.Result.Symbol(), c.Name, c.Detail)
		}
		t.Fatalf("verdict %s, want MATCH", l.Verdict)
	}

	l2 := BuildImmoScoutListing(items[1], nil)
	evaluator().Evaluate(context.Background(), l2)
	if l2.Verdict != listing.Reject || l2.AddressPrecise {
		t.Fatalf("furnished 1900€ flat: verdict=%s precise=%v", l2.Verdict, l2.AddressPrecise)
	}
}

func TestSearchURLToAPI(t *testing.T) {
	got, err := SearchURLToAPI("https://www.immobilienscout24.de/Suche/de/hamburg/hamburg/wohnung-mieten?numberofrooms=2.0-&price=-1600.0&livingspace=50.0-&pricetype=calculatedtotalrent&equipment=balcony,builtinkitchen&enteredFrom=result_list")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(got)
	q := u.Query()
	if u.Host != "api.mobile.immobilienscout24.de" || u.Path != "/search/list" ||
		q.Get("geocodes") != "/de/hamburg/hamburg" || q.Get("realestatetype") != "apartmentrent" ||
		q.Get("searchType") != "region" || q.Get("price") != "-1600.0" || q.Get("sorting") != "-firstactivation" ||
		q.Get("enteredFrom") != "" || q.Get("equipment") != "balcony,builtinkitchen" {
		t.Fatalf("bad api url: %s", got)
	}
}

func TestImmoScoutFurnished(t *testing.T) {
	cases := map[string]bool{
		"Möblierte 2-Zimmer-Wohnung in Eppendorf": true,
		"Voll möbliert, sofort beziehbar":         true,
		"Schöne Altbauwohnung mit Balkon":         false,
		"Teilmöbliert: Küche und Einbauschrank":   false,
		"Die Wohnung wird unmöbliert vermietet":   false,
	}
	for title, want := range cases {
		l := BuildImmoScoutListing(isItem{ID: "1", Title: title}, nil)
		if l.Furnished != want {
			t.Errorf("%q → furnished=%v want %v", title, l.Furnished, want)
		}
	}
}

func TestSearchURLToAPIEnglish(t *testing.T) {
	got, err := SearchURLToAPI("https://www.immobilienscout24.de/en/search/de/hamburg/hamburg/apartments-for-rent?osmtags=bus_stop,subway&price=-1600.0&livingspace=50.0-&equipment=builtinkitchen,balcony&pricetype=calculatedtotalrent&enteredFrom=result_list")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(got)
	q := u.Query()
	if q.Get("geocodes") != "/de/hamburg/hamburg" || q.Get("realestatetype") != "apartmentrent" ||
		q.Get("osmtags") != "" || q.Get("sorting") != "-firstactivation" || q.Get("price") != "-1600.0" {
		t.Fatalf("bad api url: %s", got)
	}
}

func TestImmoScoutEarlierStartRejected(t *testing.T) {
	l := BuildImmoScoutListing(isItem{ID: "1", Title: "Wohnung"}, []byte(`{"sections":[{"label":"Bezugsfrei ab:","text":"15.10.2026"}]}`))
	if l.AvailableFrom != "15.10.2026" || !l.StrictStart {
		t.Fatalf("avail=%q strict=%v", l.AvailableFrom, l.StrictStart)
	}
}

func TestImmoScoutPaywallTag(t *testing.T) {
	items, err := ParseImmoScoutList([]byte(`{"resultListItems":[{"type":"EXPOSE_RESULT","item":{"id":"171688322","title":"T",
		"tags":[{"tag":"paywall","value":{"type":"NON_PLUS_USER"}}]}},{"type":"EXPOSE_RESULT","item":{"id":"2","title":"U"}}]}`))
	if err != nil || len(items) != 2 {
		t.Fatalf("items=%d err=%v", len(items), err)
	}
	if !BuildImmoScoutListing(items[0], nil).PlusOnly || BuildImmoScoutListing(items[1], nil).PlusOnly {
		t.Error("paywall tag not detected correctly")
	}
}

func TestHomeCompanyIndexCards(t *testing.T) {
	cards := ParseHomeCompanyIndex(read(t, "homecompany_index.html"))
	if len(cards) != 12 {
		t.Fatalf("%d cards, want 12", len(cards))
	}
	var g, s *listing.Listing
	for _, c := range cards {
		switch {
		case strings.HasPrefix(c.ID, "zentrale-lage-im-grindelhof"):
			g = c
		case strings.HasPrefix(c.ID, "stilvolle-3-zimmer-wohnung-mit-balkon"):
			s = c
		}
	}
	if g == nil || s == nil {
		t.Fatal("expected cards missing")
	}
	// The badge before a card's body belongs to that card: Grindelhof is 01.02.2027.
	if g.WarmRent != 1550 || g.Rooms != 2 || g.SizeM2 != 46 || g.AvailableFrom != "01.02.2027" || g.District != "Eimsbüttel" || !g.Partial {
		t.Errorf("grindelhof card: %+v", g)
	}
	if s.WarmRent != 2150 || s.Rooms != 3 || s.SizeM2 != 68 || s.AvailableFrom != "13.10.2026" || s.Lat < 53.56 || s.Lat > 53.57 {
		t.Errorf("stilvolle card: rent=%v rooms=%v size=%v avail=%q lat=%v", s.WarmRent, s.Rooms, s.SizeM2, s.AvailableFrom, s.Lat)
	}
	if !g.StrictStart || g.MaxLeaseMonths != 12 || !g.Furnished {
		t.Error("HomeCompany defaults missing on card")
	}
}

func TestHomeCompanyLiveDetail(t *testing.T) {
	l := ParseHomeCompanyDetail(read(t, "homecompany_detail_live.html"))
	if l.WarmRent != 2150 || l.SizeM2 != 68 || l.Rooms != 3 || l.AvailableFrom != "13.10.2026" || l.District != "Eimsbüttel" {
		t.Fatalf("parsed: rent=%v size=%v rooms=%v avail=%q district=%q", l.WarmRent, l.SizeM2, l.Rooms, l.AvailableFrom, l.District)
	}
	ev := evaluator()
	ev.Criteria.MoveInFrom, ev.Criteria.MoveInTo = "2026-12-01", "2026-12-31"
	ev.Criteria.MoveFrom = time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	ev.Criteria.MoveTo = time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	if !ev.QuickReject(l) {
		t.Error("2150 € / available 13.10. should be rejected from the card facts")
	}
	ev.Evaluate(context.Background(), l)
	if r := result(t, l, listing.CritBalcony); r != listing.Pass {
		t.Errorf("Südbalkon not found: %v", r)
	}
	if r := result(t, l, listing.CritDuration); r != listing.Pass {
		c, _ := l.Check(listing.CritDuration)
		t.Errorf("duration %v (%s): Mindestmietdauer is a minimum, not a limit", r, c.Detail)
	}
}

func TestImmoweltSearch(t *testing.T) {
	cards, total, err := ParseImmoweltSearch(read(t, "immowelt_search.html"))
	if err != nil || total != 323 || len(cards) != 30 {
		t.Fatalf("cards=%d total=%d err=%v", len(cards), total, err)
	}
	var first, korallus *listing.Listing
	for _, c := range cards {
		if strings.Contains(c.Address, "Korallusring") {
			korallus = c
		}
	}
	first = cards[0]
	// Warm rent below cold rent (197 € vs 1545 €) is implausible: use cold as a lower bound.
	if first.WarmRent < 1545 || !first.RentLowerBound || first.AvailableFrom != "01.12.2026" || first.District != "Marienthal" {
		t.Errorf("first card: %+v", first)
	}
	if korallus == nil || korallus.WarmRent < 1650 || korallus.WarmRent > 1651 || !korallus.AddressPrecise || korallus.Rooms == 0 || korallus.SizeM2 == 0 || !korallus.Partial || !korallus.StrictStart {
		t.Errorf("Korallusring card: %+v", korallus)
	}
	u, _ := iwPageURL("https://www.immowelt.de/classified-search?locations=AD08DE1113&order=Default", 3)
	if !strings.Contains(u, "order=DateDesc") || !strings.Contains(u, "page=3") {
		t.Errorf("page url %s", u)
	}
}

func TestImmoweltExpose(t *testing.T) {
	l := ParseImmoweltExpose(read(t, "immowelt_expose.html"))
	if l.WarmRent < 2102 || l.WarmRent > 2104 || l.SizeM2 < 115 || l.Rooms != 4 || l.AvailableFrom != "17.10.2026" {
		t.Fatalf("parsed: rent=%v (%s) size=%v rooms=%v avail=%q", l.WarmRent, l.RentNote, l.SizeM2, l.Rooms, l.AvailableFrom)
	}
	if strings.Contains(l.Text, "Lagerhalle") {
		t.Error("site navigation not cut off")
	}
	evaluator().Evaluate(context.Background(), l)
	for _, crit := range []string{listing.CritBalcony, listing.CritKitchen} {
		if r := result(t, l, crit); r != listing.Pass {
			c, _ := l.Check(crit)
			t.Errorf("%s: %v (%s)", crit, r, c.Detail)
		}
	}
}
