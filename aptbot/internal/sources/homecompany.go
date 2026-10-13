package sources

import (
	"context"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"

	"aptbot/internal/analyze"
	"aptbot/internal/config"
	"aptbot/internal/listing"
	"aptbot/internal/web"
)

// HomeCompany scrapes https://www.homecompany.de (furnished, "Wohnen auf Zeit").
// The Hamburg agency list is paginated with ?pg=N (~12 cards per page, ~16
// pages); detail pages are /immobilie/<slug>/.
//
// The list order is neither by age nor by move-in date, so every run reads
// all index pages. Each card already shows rent, rooms, size and move-in
// date: a flat that fails on those (Prefilter) is recorded from its card,
// and only the rest get their detail page fetched.
type HomeCompany struct {
	Client *web.Client
	Cfg    config.HomeCompanyConfig
	// Prefilter reports whether a card-only listing already fails; nil
	// fetches every detail page.
	Prefilter func(*listing.Listing) bool
}

func (h *HomeCompany) Name() string { return "HomeCompany" }

// HomeCompany rents furnished flats for limited periods, like City-Wohnen.
const homeCompanyMaxLeaseMonths = 12

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
	var cards []*listing.Listing
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
		added := 0
		for _, c := range ParseHomeCompanyIndex(string(body)) {
			if !known[c.ID] {
				known[c.ID] = true
				cards = append(cards, c)
				added++
			}
		}
		if added == 0 { // past the last page (site repeats last page or shows none)
			break
		}
	}
	logf(h.Name(), "%d listings on index", len(cards))

	skipped := 0
	for _, card := range cards {
		if seen(h.Name() + ":" + card.ID) {
			continue
		}
		if h.Prefilter != nil && h.Prefilter(card) {
			skipped++
			emit(card) // rejected on the card's facts; no detail request
			continue
		}
		body, err := h.Client.Get(ctx, card.URL, nil)
		if err != nil {
			logf(h.Name(), "detail %s: %v", card.URL, err)
			continue
		}
		l := ParseHomeCompanyDetail(string(body))
		l.ID, l.URL = card.ID, card.URL
		mergeCard(l, card)
		emit(l)
	}
	if skipped > 0 {
		logf(h.Name(), "%d new listing(s) rejected from the index card alone (no detail request)", skipped)
	}
	return nil
}

// mergeCard fills what the detail page didn't yield from the index card.
func mergeCard(l, card *listing.Listing) {
	if l.WarmRent == 0 {
		l.WarmRent, l.RentNote = card.WarmRent, card.RentNote
	}
	if l.SizeM2 == 0 {
		l.SizeM2 = card.SizeM2
	}
	if l.Rooms == 0 {
		l.Rooms = card.Rooms
	}
	if l.AvailableFrom == "" {
		l.AvailableFrom = card.AvailableFrom
	}
	if l.District == "" {
		l.District, l.Address = card.District, card.Address
	}
	if l.Lat == 0 && card.Lat != 0 {
		l.Lat, l.Lon = card.Lat, card.Lon
	}
	if l.Title == "" {
		l.Title = card.Title
	}
}

var (
	reHCCardHref  = regexp.MustCompile(`href="(/(?:en/)?immobilie/([a-z0-9-]+)/?)"`)
	reHCCardTitle = regexp.MustCompile(`<a title="([^"]+)"`)
	reHCCardRent  = regexp.MustCompile(`([0-9][0-9.]*)\s*€`)
	reHCCardNum   = func(label string) *regexp.Regexp {
		return regexp.MustCompile(`(?s)title="` + label + `".*?<p[^>]*>(.*?)</p>`)
	}
	reHCCardRooms = reHCCardNum("Zimmer")
	reHCCardArea  = reHCCardNum("Wohnfläche")
	reHCCardM2    = regexp.MustCompile(`([0-9][0-9.,]*)\s*m`)
	reHTMLComment = regexp.MustCompile(`<!--.*?-->`)
)

// ParseHomeCompanyIndex reads the cards of one index page. A card runs from
// its first link (the photo) to the next card's: the "Verfügbar ab" badge
// sits on the photo, before the card body.
func ParseHomeCompanyIndex(page string) []*listing.Listing {
	type start struct {
		pos      int
		path, id string
	}
	var starts []start
	seen := map[string]bool{}
	for _, m := range reHCCardHref.FindAllStringSubmatchIndex(page, -1) {
		id := page[m[4]:m[5]]
		if !seen[id] {
			seen[id] = true
			starts = append(starts, start{m[0], page[m[2]:m[3]], id})
		}
	}
	var out []*listing.Listing
	for i, st := range starts {
		end := len(page)
		if i+1 < len(starts) {
			end = starts[i+1].pos
		}
		seg := page[st.pos:end]
		text := web.HTMLToText(reHTMLComment.ReplaceAllString(seg, ""))
		l := &listing.Listing{Source: "HomeCompany", ID: st.id, URL: "https://www.homecompany.de" + st.path, Partial: true}
		if m := reHCCardTitle.FindStringSubmatch(seg); m != nil {
			l.Title = html.UnescapeString(m[1])
		}
		if m := reHCCardRent.FindStringSubmatch(text); m != nil {
			l.WarmRent, _ = analyze.ParseGermanNumber(m[1])
			l.RentNote = "Pauschalmiete, all-inclusive"
		}
		if m := reHCCardRooms.FindStringSubmatch(seg); m != nil {
			l.Rooms, _ = analyze.ParseGermanNumber(strings.TrimSpace(web.HTMLToText(reHTMLComment.ReplaceAllString(m[1], ""))))
		}
		if m := reHCCardArea.FindStringSubmatch(seg); m != nil {
			if n := reHCCardM2.FindStringSubmatch(web.HTMLToText(reHTMLComment.ReplaceAllString(m[1], ""))); n != nil {
				l.SizeM2, _ = analyze.ParseGermanNumber(n[1]) // "ca. 46 m²"
			}
		}
		if m := reHCAvailable.FindStringSubmatch(text); m != nil {
			l.AvailableFrom = m[1]
		}
		if m := reHCLocation.FindStringSubmatch(text); m != nil {
			l.District = strings.TrimSpace(m[2])
			l.Address = m[1] + " Hamburg"
		}
		// Coordinates, when the page's embedded data has them for this flat.
		if m := regexp.MustCompile(`/immobilie/` + regexp.QuoteMeta(st.id) + `/?\\?",\[\d+\],(5[34]\.\d+),(\d{1,2}\.\d+)`).FindStringSubmatch(page); m != nil {
			l.Lat, _ = strconv.ParseFloat(m[1], 64) // plain decimals, not German numbers
			l.Lon, _ = strconv.ParseFloat(m[2], 64)
		}
		homeCompanyDefaults(l)
		out = append(out, l)
	}
	return out
}

var (
	reHCLocation  = regexp.MustCompile(`(\d{5})\s+Hamburg\s*[–—-]\s*([^\n:|]{2,40})`)
	reHCAvailable = regexp.MustCompile(`(?i)Verfügbar ab\s*:?\s*(\d{1,2}\.\d{1,2}\.\d{4}|sofort)`)
	reHCLage      = regexp.MustCompile(`(?i)(?:^|\n)\s*Lage\s*:?\s*([^\n]{3,400})`)
	reHCRoomsT    = regexp.MustCompile(`(?i)(\d(?:[.,]5)?)\s*-?\s*(?:Zi\b|Zi\.|Zimmer)`)
)

// homeCompanyDefaults: what HomeCompany guarantees for all its flats.
func homeCompanyDefaults(l *listing.Listing) {
	furnishedAgencyDefaults(l, "HomeCompany")
	l.StrictStart = true // flats free before the move-in window are rejected
	l.MaxLeaseMonths = homeCompanyMaxLeaseMonths
	l.SetDefault(listing.CritDuration, listing.Pass, "HomeCompany rents for limited periods; no end date stated")
}

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
	homeCompanyDefaults(l)
	return l
}
