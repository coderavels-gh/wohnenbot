package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"aptbot/internal/analyze"
	"aptbot/internal/config"
	"aptbot/internal/listing"
	"aptbot/internal/web"
)

// CityWohnen scrapes https://www.city-wohnen.de (furnished, Hamburg only).
// The result list is sorted by move-in date, not by age, so new flats can
// appear on any page. Every run reads all pages through the endpoint the
// site's own pager uses (~100 flats, 50 per request); detail pages are only
// fetched for flats not seen yet.
type CityWohnen struct {
	Client *web.Client
	Cfg    config.CityWohnenConfig
}

func (c *CityWohnen) Name() string { return "City-Wohnen" }

const cityWohnenMaxLeaseMonths = 12

var reCWDetail = regexp.MustCompile(`^https://www\.city-wohnen\.de/(?:en/)?hamburg/(\d+)-[a-z0-9-]+/?$`)

const (
	cwBase     = "https://www.city-wohnen.de/"
	cwSearch   = cwBase + "rpc.php?service=ciwo&cmd=search&action=objectlist&pageid=9&date_from=&rent_amount_min=&rent_amount_max=&order=&pagesize=50&lang=ger"
	cwPageSize = 50
	cwMaxPages = 10
)

// indexAll reads every result page; ok=false means the endpoint failed and
// the caller should fall back to the plain list pages.
func (c *CityWohnen) indexAll(ctx context.Context) (urls []string, ok bool) {
	for page := 1; page <= cwMaxPages; page++ {
		body, err := c.Client.Get(ctx, fmt.Sprintf("%s&pagenr=%d", cwSearch, page), map[string]string{
			"X-Requested-With": "XMLHttpRequest", "Accept": "application/json", "Referer": c.Cfg.ListURLs[0]})
		var resp struct {
			Total   int    `json:"total"`
			Results string `json:"results"`
		}
		if err == nil {
			err = json.Unmarshal(body, &resp)
		}
		if err != nil {
			logf(c.Name(), "search page %d: %v", page, err)
			return urls, len(urls) > 0
		}
		got := web.Links(resp.Results, cwBase, reCWDetail)
		urls = append(urls, got...)
		if len(got) == 0 || page*cwPageSize >= resp.Total {
			break
		}
	}
	return urls, len(urls) > 0
}

func (c *CityWohnen) Fetch(ctx context.Context, seen func(string) bool, emit func(*listing.Listing)) error {
	var detailURLs []string
	known := map[string]bool{}
	all, ok := c.indexAll(ctx)
	for _, u := range all {
		id := reCWDetail.FindStringSubmatch(u)[1]
		if !known[id] {
			known[id] = true
			detailURLs = append(detailURLs, u)
		}
	}
	for i, lu := range c.Cfg.ListURLs {
		if ok {
			break // all pages read; the plain list page only shows the first 20
		}
		body, err := c.Client.Get(ctx, lu, nil)
		if err != nil {
			if i == 0 && len(c.Cfg.ListURLs) == 1 {
				return err
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
		emit(l)
	}
	return nil
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
	l.StrictStart = true
	furnishedAgencyDefaults(l, "City-Wohnen")
	l.SetDefault(listing.CritAnmeldung, listing.Pass, "City-Wohnen states Anmeldung is possible for all its flats")
	// City-Wohnen rents for up to 12 months: a flat that allows the full 12
	// passes, and one with no end date is assumed to allow it.
	l.MaxLeaseMonths = cityWohnenMaxLeaseMonths
	l.SetDefault(listing.CritDuration, listing.Pass, "City-Wohnen rents for up to 12 months; no end date stated")
	return l
}
