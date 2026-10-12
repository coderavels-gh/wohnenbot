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
		q.Get("searchType") != "region" || q.Get("price") != "-1600.0" || q.Get("sorting") != "2" ||
		q.Get("enteredFrom") != "" || q.Get("equipment") != "balcony,builtinkitchen" {
		t.Fatalf("bad api url: %s", got)
	}
}
