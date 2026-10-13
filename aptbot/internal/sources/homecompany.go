package sources

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"aptbot/internal/analyze"
	"aptbot/internal/config"
	"aptbot/internal/listing"
	"aptbot/internal/web"
)

// HomeCompany scrapes https://www.homecompany.de (furnished, "Wohnen auf Zeit").
// The Hamburg agency list is paginated with ?pg=N; detail pages are
// /immobilie/<slug>/.
type HomeCompany struct {
	Client *web.Client
	Cfg    config.HomeCompanyConfig
}

func (h *HomeCompany) Name() string { return "HomeCompany" }

var reHCDetail = regexp.MustCompile(`^https://www\.homecompany\.de/(?:en/)?immobilie/([a-z0-9-]+)/?$`)

func (h *HomeCompany) pageURL(n int) string {
	if n <= 1 {
		return h.Cfg.ListURL
	}
	sep := "?"
	if strings.Contains(h.Cfg.ListURL, "?") {
		sep = "&"
	}
	return fmt.Sprintf("%s%spg=%d", h.Cfg.ListURL, sep, n)
}

func (h *HomeCompany) Fetch(ctx context.Context, seen func(string) bool, emit func(*listing.Listing)) error {
	var detailURLs []string
	known := map[string]bool{}
	for p := 1; p <= h.Cfg.MaxPages; p++ {
		body, err := h.Client.Get(ctx, h.pageURL(p), nil)
		if err != nil {
			if p == 1 {
				return err
			}
			logf(h.Name(), "page %d: %v", p, err)
			break
		}
		links := web.Links(string(body), h.pageURL(p), reHCDetail)
		added := 0
		for _, u := range links {
			if !known[u] {
				known[u] = true
				detailURLs = append(detailURLs, u)
				added++
			}
		}
		if added == 0 { // past the last page (site repeats last page or shows none)
			break
		}
	}
	logf(h.Name(), "%d listings on index", len(detailURLs))

	for _, u := range detailURLs {
		id := reHCDetail.FindStringSubmatch(u)[1]
		if seen(h.Name() + ":" + id) {
			continue
		}
		body, err := h.Client.Get(ctx, u, nil)
		if err != nil {
			logf(h.Name(), "detail %s: %v", u, err)
			continue
		}
		l := ParseHomeCompanyDetail(string(body))
		l.ID, l.URL = id, u
		emit(l)
	}
	return nil
}

var (
	reHCLocation  = regexp.MustCompile(`(\d{5})\s+Hamburg\s*[–—-]\s*([^\n:|]{2,40})`)
	reHCAvailable = regexp.MustCompile(`(?i)Verfügbar ab\s*:?\s*(\d{1,2}\.\d{1,2}\.\d{4}|sofort)`)
	reHCLage      = regexp.MustCompile(`(?i)(?:^|\n)\s*Lage\s*:?\s*([^\n]{3,400})`)
	reHCRoomsT    = regexp.MustCompile(`(?i)(\d(?:[.,]5)?)\s*-?\s*(?:Zi\b|Zi\.|Zimmer)`)
)

// ParseHomeCompanyDetail extracts a listing from a HomeCompany detail page.
func ParseHomeCompanyDetail(page string) *listing.Listing {
	text := web.HTMLToText(page)
	text = web.CutAt(text, "Ähnliche Immobilien", "Ähnliche Angebote", "Weitere Angebote", "Das könnte Sie auch interessieren", "Zuletzt angesehen")
	text = cleanText(text)
	l := &listing.Listing{Source: "HomeCompany", Title: web.Title(page), Text: text}
	r := analyze.FindWarmRent(text, `Miete`)
	l.WarmRent, l.RentNote = r.Warm, r.Note
	if l.WarmRent > 0 && l.RentNote == "" {
		l.RentNote = "Pauschalmiete, all-inclusive"
	}
	l.SizeM2 = analyze.FindArea(text)
	l.Rooms = analyze.FindRooms(text)
	if l.Rooms == 0 {
		if m := reHCRoomsT.FindStringSubmatch(l.Title); m != nil {
			l.Rooms, _ = analyze.ParseGermanNumber(m[1])
		}
	}
	if m := reHCLocation.FindStringSubmatch(text); m != nil {
		l.District = strings.TrimSpace(m[2])
		l.Address = m[1] + " Hamburg"
	}
	if m := reHCAvailable.FindStringSubmatch(text); m != nil {
		l.AvailableFrom = m[1]
	}
	if m := reHCLage.FindStringSubmatch(text); m != nil {
		l.TransitText = m[1]
	}
	furnishedAgencyDefaults(l, "HomeCompany")
	return l
}
