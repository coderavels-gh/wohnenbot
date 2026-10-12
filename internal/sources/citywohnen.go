package sources

import (
	"context"
	"regexp"
	"strings"

	"aptbot/internal/analyze"
	"aptbot/internal/config"
	"aptbot/internal/listing"
	"aptbot/internal/web"
)

// CityWohnen scrapes https://www.city-wohnen.de (furnished, Hamburg only).
// The result list is sorted newest-first, so polling the first page every
// 30 minutes catches every new listing; we don't need its JS pagination.
type CityWohnen struct {
	Client *web.Client
	Cfg    config.CityWohnenConfig
}

func (c *CityWohnen) Name() string { return "City-Wohnen" }

var reCWDetail = regexp.MustCompile(`^https://www\.city-wohnen\.de/(?:en/)?hamburg/(\d+)-[a-z0-9-]+/?$`)

func (c *CityWohnen) Fetch(ctx context.Context, seen func(string) bool) ([]*listing.Listing, error) {
	var detailURLs []string
	known := map[string]bool{}
	for i, lu := range c.Cfg.ListURLs {
		body, err := c.Client.Get(ctx, lu, nil)
		if err != nil {
			if i == 0 && len(c.Cfg.ListURLs) == 1 {
				return nil, err
			}
			logf(c.Name(), "list %s: %v", lu, err)
			continue
		}
		for _, u := range web.Links(string(body), lu, reCWDetail) {
			id := reCWDetail.FindStringSubmatch(u)[1]
			if !known[id] {
				known[id] = true
				detailURLs = append(detailURLs, u)
			}
		}
	}
	logf(c.Name(), "%d listings on index", len(detailURLs))
	var out []*listing.Listing
	for _, u := range detailURLs {
		id := reCWDetail.FindStringSubmatch(u)[1]
		if seen(c.Name() + ":" + id) {
			continue
		}
		body, err := c.Client.Get(ctx, u, nil)
		if err != nil {
			logf(c.Name(), "detail %s: %v", u, err)
			continue
		}
		l := ParseCityWohnenDetail(string(body), u)
		l.ID, l.URL = id, u
		out = append(out, l)
	}
	return out, nil
}

var (
	// "Hamburg-Eimsbüttel, Emilienstraße" (district, street — no house number)
	reCWAddr      = regexp.MustCompile(`Hamburg-([A-ZÄÖÜ][^\n,:|]{1,35}),\s*([A-ZÄÖÜ][^\n,:|()]{2,50})`)
	reCWAvailable = regexp.MustCompile(`(?i)(?:Verfügbarkeit|ab)\s*:?\s*(?:ab\s*)?(\d{1,2}\.\d{1,2}\.\d{4})`)
	reCWSlug      = regexp.MustCompile(`/hamburg/\d+-moeblierte?-(?:wohnung|haus|apartment)-hamburg-([a-z0-9-]+)`)
	// Teaser lines on the page that mention features of *other* flats.
	reCWNoise = regexp.MustCompile(`(?im)^.*(?:Wohnungen mit Balkon|Wohnungen mit Haustier|Business Apartments für|Wohnungen für Familien|\| Wohnen auf Zeit in Hamburg).*$`)
)

// ParseCityWohnenDetail extracts a listing from a City-Wohnen detail page.
func ParseCityWohnenDetail(page, pageURL string) *listing.Listing {
	text := web.HTMLToText(page)
	text = web.CutAt(text, "Das perfekte Match", "Ähnliche Wohnungen", "Beliebte Stadtteile", "Wo man sich Zuhause fühlt", "Das könnte Sie auch interessieren", "City-Wohnen Magazin")
	text = cleanText(reCWNoise.ReplaceAllString(text, ""))
	l := &listing.Listing{Source: "City-Wohnen", Title: web.Title(page), Text: text}
	r := analyze.FindWarmRent(text, `Miete`)
	l.WarmRent, l.RentNote = r.Warm, r.Note
	if l.WarmRent > 0 && l.RentNote == "" {
		l.RentNote = "City-Wohnen rent incl. utilities"
	}
	l.SizeM2 = analyze.FindArea(text)
	l.Rooms = analyze.FindRooms(text)
	if m := reCWAddr.FindStringSubmatch(l.Title + "\n" + text); m != nil {
		l.District = strings.TrimSpace(m[1])
		street := strings.TrimSpace(m[2])
		l.Address = street + ", Hamburg"
		l.AddressPrecise = true
	} else if m := reCWSlug.FindStringSubmatch(pageURL); m != nil {
		l.Address = strings.ReplaceAll(m[1], "-", " ") + " Hamburg"
	}
	if m := reCWAvailable.FindStringSubmatch(text); m != nil {
		l.AvailableFrom = m[1]
	}
	furnishedAgencyDefaults(l, "City-Wohnen")
	l.SetDefault(listing.CritAnmeldung, listing.Pass, "City-Wohnen states Anmeldung is possible for all its flats")
	return l
}
