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

// Kleinanzeigen reads https://www.kleinanzeigen.de rental flat searches
// ("/s-wohnung-mieten/…"). robots.txt allows these pages with the size and
// room filters, and the ad pages (/s-anzeige/…); it disallows price filters
// and other sort orders, so the rent is filtered here.
//
// Results come newest first (by last change: a renewed old ad moves back to
// the top), ~25 per page plus a few paid "TOP" ads. Each run reads pages while
// they still hold unseen ads. A card shows the price (the cold rent), size,
// rooms and postcode/district: a flat that fails on those (Prefilter) is
// recorded from its card alone, and only the rest get their ad page fetched.
//
// Private ads are common, and so are ads that aren't flat offers at all
// ("Wohnung gesucht", rooms in a shared flat); those are dropped from the
// card title.
type Kleinanzeigen struct {
	Client    *web.Client
	Cfg       config.KleinanzeigenConfig
	Prefilter func(*listing.Listing) bool
	// FullScan reads more result pages (first run, criteria change).
	FullScan bool
}

func (k *Kleinanzeigen) Name() string          { return "Kleinanzeigen" }
func (k *Kleinanzeigen) SetFullScan(full bool) { k.FullScan = full }

const (
	kaBase      = "https://www.kleinanzeigen.de"
	kaFullPages = 10 // cap for a full scan (~250 ads, the last week or two)
)

// kaPageURL inserts the page number before the search's last path segment:
// /s-wohnung-mieten/hamburg/seite:2/c203l9409+… The URL is kept as written
// (its filters are %-encoded commas).
func kaPageURL(search string, page int) (string, error) {
	i := strings.LastIndex(search, "/")
	if !strings.HasPrefix(search, kaBase+"/s-") || i < len(kaBase) {
		return "", fmt.Errorf("not a kleinanzeigen search URL: %s", search)
	}
	if page <= 1 {
		return search, nil
	}
	return fmt.Sprintf("%s/seite:%d%s", search[:i], page, search[i:]), nil
}

func (k *Kleinanzeigen) Fetch(ctx context.Context, seen func(string) bool, emit func(*listing.Listing)) error {
	maxPages := k.Cfg.MaxPages
	if k.FullScan {
		maxPages = kaFullPages
	}
	var todo []*listing.Listing
	known := map[string]bool{}
	dropped := 0
	var firstErr error
	for _, search := range k.Cfg.SearchURLs {
		read := 0
		for page := 1; page <= maxPages; page++ {
			u, err := kaPageURL(search, page)
			if err != nil {
				return err
			}
			body, err := k.Client.Get(ctx, u, nil)
			if err != nil {
				logf(k.Name(), "search page %d: %v", page, err)
				if page == 1 && firstErr == nil {
					firstErr = err
				}
				break
			}
			cards := ParseKleinanzeigenSearch(string(body))
			if len(cards) == 0 {
				if page == 1 && firstErr == nil {
					firstErr = fmt.Errorf("no ads on the first result page (page layout changed?)")
				}
				break
			}
			read += len(cards)
			unseen := 0
			for _, c := range cards {
				if known[c.ID] {
					continue
				}
				known[c.ID] = true
				if seen(k.Name() + ":" + c.ID) {
					continue
				}
				if kaNotAnOffer(c.Title) {
					dropped++
					continue
				}
				unseen++
				todo = append(todo, c)
			}
			if !k.FullScan && unseen == 0 {
				break // nothing new on this page: deeper pages are older
			}
		}
		if k.FullScan {
			logf(k.Name(), "full scan (first run or criteria change): %d ads read", read)
		} else {
			logf(k.Name(), "%d results read", read)
		}
	}

	skipped := 0
	for _, card := range todo {
		if k.Prefilter != nil && k.Prefilter(card) {
			skipped++
			emit(card)
			continue
		}
		body, err := k.Client.Get(ctx, card.URL, nil)
		if err != nil {
			logf(k.Name(), "ad %s: %v", card.URL, err)
			continue
		}
		l := ParseKleinanzeigenAd(string(body))
		l.ID, l.URL = card.ID, card.URL
		if card.Title != "" {
			l.Title = card.Title
		}
		mergeCard(l, card)
		emit(l)
	}
	if dropped > 0 {
		logf(k.Name(), "%d ad(s) skipped: not a flat offer (wanted ads, rooms in shared flats)", dropped)
	}
	if skipped > 0 {
		logf(k.Name(), "%d new listing(s) rejected from the search data alone (no ad request)", skipped)
	}
	if len(todo) == 0 && firstErr != nil {
		return firstErr
	}
	return nil
}

// kaNotAnOffer: titles that clearly aren't a flat on offer: "Wohnung
// gesucht", "Suche 2-Zimmer-Wohnung", "WG-Zimmer", "Zimmer zu vermieten".
// Any title that talks about a tenant ("Suchen Nachmieter", "Mieter
// gesucht", "vermiete") is an offer and stays, unless it opens with the
// request ("Wohnung gesucht – Suchst du einen Nachmieter?"); when in doubt,
// keep the ad.
var (
	reKAWanted = regexp.MustCompile(`(?i)` + kaWantedFlat + `|` + kaSearchFlat)
	// The title opens with the request.
	reKAWantedLead = regexp.MustCompile(`(?i)^\W*(?:` + kaWantedFlat + `|(?:ich\s+|wir\s+)?` + kaSearchFlat + `)`)
	reKARoom       = regexp.MustCompile(`(?i)` +
		`\bwg-?zimmer\b|\bzimmer\s+in\s+(?:einer\s+|unserer\s+)?(?:\d+er[- ]?)?wg\b|^\W*(?:möbliertes\s+|schönes\s+|helles\s+)?zimmer\s+(?:zu\s+vermieten|frei)\b`)
	reKATenant = regexp.MustCompile(`(?i)mieter|vermiet|zu\s+vergeben`)
)

const (
	kaWantedFlat = `\b(?:wohnung|mietwohnung|whg\.?|bleibe|zuhause|wohnraum)\s+(?:dringend\s+|in\s+hamburg\s+)?gesucht\b`
	kaSearchFlat = `\bsuch(?:e|en|t)\s+(?:(?:eine?n?|dringend|ab\s+sofort|bezahlbare?n?|kleine?n?|neue?n?|ruhige?n?)\s+)*` +
		`(?:\d+(?:[.,]\d)?[- ]?(?:zi\.?|zimmer)[- ]?)?(?:wohnung|mietwohnung|whg|bleibe|zuhause|wohnraum)`
)

func kaNotAnOffer(title string) bool {
	if reKARoom.MatchString(title) || reKAWantedLead.MatchString(title) {
		return true
	}
	return !reKATenant.MatchString(title) && reKAWanted.MatchString(title)
}

var (
	reKAArticle  = regexp.MustCompile(`<article[^>]*\bdata-adid="(\d+)"[^>]*\bdata-href="([^"]+)"`)
	reKACardLoc  = regexp.MustCompile(`(?s)text-onSurfaceNonessential">.*?<span>([^<]+)</span>`)
	reKACardH3   = regexp.MustCompile(`(?s)<h3[^>]*>\s*<a[^>]*>(.*?)</a>`)
	reKACardFact = regexp.MustCompile(`([0-9][0-9.,]*)\s*m²\s*·\s*([0-9][0-9.,]*)\s*Zi\.`)
	reKACardM2   = regexp.MustCompile(`([0-9][0-9.,]*)\s*m²`)
	reKACardZi   = regexp.MustCompile(`([0-9][0-9.,]*)\s*Zi\.`)
	reKAPrice    = regexp.MustCompile(`([0-9][0-9.]*)\s*€`)
	reKAPriceTag = regexp.MustCompile(`(?s)<p class="[^"]*text-title3[^"]*">(.*?)</p>`)
)

// ParseKleinanzeigenSearch reads the ad cards of one result page, in page order.
func ParseKleinanzeigenSearch(page string) []*listing.Listing {
	if i := strings.Index(page, `id="srchrslt-adtable"`); i >= 0 {
		page = page[i:]
	}
	if i := strings.Index(page, `id="srchrslt-pagination"`); i >= 0 {
		page = page[:i]
	}
	idx := reKAArticle.FindAllStringSubmatchIndex(page, -1)
	var out []*listing.Listing
	for n, m := range idx {
		end := len(page)
		if n+1 < len(idx) {
			end = idx[n+1][0]
		}
		seg := page[m[0]:end]
		l := &listing.Listing{Source: "Kleinanzeigen", ID: page[m[2]:m[3]], URL: kaBase + html.UnescapeString(page[m[4]:m[5]]), Partial: true}
		if t := reKACardH3.FindStringSubmatch(seg); t != nil {
			l.Title = strings.TrimSpace(html.UnescapeString(reTagKA.ReplaceAllString(t[1], "")))
		}
		if loc := reKACardLoc.FindStringSubmatch(seg); loc != nil {
			// Cards name the district, sometimes without the city ("21029
			// Bergedorf"); the search is for Hamburg, and the ad page has the
			// full locality.
			l.Address, l.District = kaLocality(html.UnescapeString(loc[1]), "")
			if l.District == "" && !strings.Contains(l.Address, "Hamburg") {
				if plz, place, ok := strings.Cut(l.Address, " "); ok {
					l.Address, l.District = plz+" Hamburg", place
				}
			}
		}
		text := web.HTMLToText(seg)
		if f := reKACardFact.FindStringSubmatch(text); f != nil {
			l.SizeM2, _ = analyze.ParseGermanNumber(f[1])
			l.Rooms, _ = analyze.ParseGermanNumber(f[2])
		} else {
			if f := reKACardM2.FindStringSubmatch(text); f != nil {
				l.SizeM2, _ = analyze.ParseGermanNumber(f[1])
			}
			if f := reKACardZi.FindStringSubmatch(text); f != nil {
				l.Rooms, _ = analyze.ParseGermanNumber(f[1])
			}
		}
		if p := reKAPriceTag.FindStringSubmatch(seg); p != nil {
			if cold := kaPrice(p[1]); cold > 0 {
				// The price is the cold rent: enough to reject a flat that is too
				// expensive; the ad page decides the rest.
				l.WarmRent, l.RentNote, l.RentLowerBound = cold, "Kaltmiete only", true
			}
		}
		l.Furnished = reISFurnished.MatchString(l.Title) && !reISUnfurnished.MatchString(l.Title)
		l.StrictStart = true
		out = append(out, l)
	}
	return out
}

var reTagKA = regexp.MustCompile(`<[^>]+>`)

// kaPrice reads "1.860 €" or "950 € VB"; 0 if there's no amount.
func kaPrice(s string) float64 {
	m := reKAPrice.FindStringSubmatch(html.UnescapeString(reTagKA.ReplaceAllString(s, " ")))
	if m == nil {
		return 0
	}
	v, _ := analyze.ParseGermanNumber(m[1])
	return v
}

var reKAPostcode = regexp.MustCompile(`^(\d{5})\s+(.+)$`)

// kaLocality turns "21149 Harburg - Hamburg Neugraben" (ad page) or
// "22081 Hamburg Barmbek" (card) into an address for geocoding
// ("Ulenweg 4, 21149 Hamburg") and the district ("Neugraben"). A town other
// than Hamburg stays in the address, so the city check can reject it.
func kaLocality(loc, street string) (address, district string) {
	loc = strings.TrimSpace(strings.ReplaceAll(loc, " ", " "))
	m := reKAPostcode.FindStringSubmatch(loc)
	if m == nil {
		return loc, ""
	}
	plz, rest := m[1], strings.TrimSpace(m[2])
	if i := strings.LastIndex(rest, " - "); i >= 0 {
		rest = strings.TrimSpace(rest[i+3:]) // drop the borough ("Harburg - ")
	}
	town := rest
	if rest == "Hamburg" || strings.HasPrefix(rest, "Hamburg ") {
		town, district = "Hamburg", strings.TrimSpace(strings.TrimPrefix(rest, "Hamburg"))
	}
	address = plz + " " + town
	if street != "" {
		address = street + ", " + address
	}
	return address, district
}

var (
	reKATitle   = regexp.MustCompile(`(?s)<h1[^>]*id="viewad-title"[^>]*>(.*?)</h1>`)
	reKALoc     = regexp.MustCompile(`(?s)id="viewad-locality"[^>]*>(.*?)<`)
	reKAStreet  = regexp.MustCompile(`(?s)id="street-address"[^>]*>(.*?)<`)
	reKAPriceAd = regexp.MustCompile(`(?s)id="viewad-price"[^>]*>(.*?)</`)
	reKADetail  = regexp.MustCompile(`(?s)<li class="addetailslist--detail">(.*?)<span[^>]*>(.*?)</span>`)
	reKAConfig  = regexp.MustCompile(`(?s)id="viewad-configuration".*?</ul>`)
	reKAConfigL = regexp.MustCompile(`(?s)<li[^>]*>(.*?)</li>`)
	reKADesc    = regexp.MustCompile(`(?s)id="viewad-description-text"[^>]*>(.*?)</p>`)
	reKABr      = regexp.MustCompile(`(?i)<br\s*/?>`)
	reKALat     = regexp.MustCompile(`property="og:latitude" content="([0-9.]+)"`)
	reKALon     = regexp.MustCompile(`property="og:longitude" content="([0-9.]+)"`)
	reKAMonth   = regexp.MustCompile(`(?i)^(januar|februar|märz|april|mai|juni|juli|august|september|oktober|november|dezember)\s+(\d{4})$`)
)

var kaMonths = map[string]int{"januar": 1, "februar": 2, "märz": 3, "april": 4, "mai": 5, "juni": 6,
	"juli": 7, "august": 8, "september": 9, "oktober": 10, "november": 11, "dezember": 12}

func kaClean(s string) string {
	s = reKABr.ReplaceAllString(s, "\n")
	s = html.UnescapeString(reTagKA.ReplaceAllString(s, " "))
	s = strings.ReplaceAll(s, " ", " ")
	return strings.TrimSpace(reSpacesKA.ReplaceAllString(s, " "))
}

var reSpacesKA = regexp.MustCompile(`[ \t\r]+`)

// ParseKleinanzeigenAd extracts a listing from an ad page. The ad's fact list
// ("Wohnfläche", "Warmmiete", "Verfügbar ab", …) and its features ("Balkon",
// "Einbauküche") are put in front of the description as "Label: value" lines,
// so the usual text checks find them.
func ParseKleinanzeigenAd(page string) *listing.Listing {
	l := &listing.Listing{Source: "Kleinanzeigen", StrictStart: true}
	if m := reKATitle.FindStringSubmatch(page); m != nil {
		l.Title = kaClean(m[1])
	}
	var facts []string
	cold := 0.0
	if m := reKAPriceAd.FindStringSubmatch(page); m != nil {
		if cold = kaPrice(m[1]); cold > 0 {
			facts = append(facts, fmt.Sprintf("Kaltmiete: %g €", cold))
		}
	}
	avail := ""
	for _, m := range reKADetail.FindAllStringSubmatch(page, -1) {
		label, value := kaClean(m[1]), kaClean(m[2])
		if label == "" || value == "" {
			continue
		}
		facts = append(facts, label+": "+value)
		if label == "Verfügbar ab" {
			avail = kaAvailable(value)
		}
	}
	if m := reKAConfig.FindString(page); m != "" {
		var feats []string
		for _, li := range reKAConfigL.FindAllStringSubmatch(m, -1) {
			if f := kaClean(li[1]); f != "" {
				feats = append(feats, f)
			}
		}
		if len(feats) > 0 {
			facts = append(facts, "Ausstattung: "+strings.Join(feats, ", "))
		}
	}
	factText := strings.Join(facts, "\n")
	desc := ""
	if m := reKADesc.FindStringSubmatch(page); m != nil {
		var lines []string
		for _, line := range strings.Split(kaClean(m[1]), "\n") {
			lines = append(lines, strings.TrimSpace(line))
		}
		desc = cleanText(strings.Join(lines, "\n"))
	}
	l.Text = strings.TrimSpace(factText + "\n\n" + desc)

	// Rent: the stated warm rent, else cold + Nebenkosten (+ Heizkosten) from
	// the fact list. Private ads often fill these in carelessly ("Warmmiete
	// 1 €"), so a warm rent below the price is ignored; with nothing better,
	// the price is a lower bound.
	r := analyze.FindWarmRent(factText, "")
	switch {
	case r.Warm > 0 && r.Warm >= cold:
		l.WarmRent, l.RentNote = r.Warm, r.Note
	case cold > 0:
		l.WarmRent, l.RentNote, l.RentLowerBound = cold, "Kaltmiete only", true
	}
	l.SizeM2 = analyze.FindArea(factText)
	l.Rooms = analyze.FindRooms(factText)
	if avail == "" {
		avail = analyze.FindAvailableFrom(desc)
	}
	l.AvailableFrom = avail

	street := ""
	if m := reKAStreet.FindStringSubmatch(page); m != nil {
		street = strings.Trim(kaClean(m[1]), " ,")
	}
	if m := reKALoc.FindStringSubmatch(page); m != nil {
		l.Address, l.District = kaLocality(kaClean(m[1]), street)
	}
	if street != "" {
		// The map pin is the house only when the street is published (else
		// it's somewhere in the postcode area).
		l.AddressPrecise = true
		lat, _ := strconv.ParseFloat(firstSub(reKALat, page), 64)
		lon, _ := strconv.ParseFloat(firstSub(reKALon, page), 64)
		if lat != 0 && lon != 0 {
			l.Lat, l.Lon = lat, lon
		}
	}

	about := l.Title + "\n" + desc
	l.Furnished = reISFurnished.MatchString(about) && !reISUnfurnished.MatchString(about)
	if !reISTemporary.MatchString(about) {
		// A normal open-ended lease, as on ImmoScout.
		l.SetDefault(listing.CritDuration, listing.Pass, "regular lease, no time limit mentioned")
		l.SetDefault(listing.CritAnmeldung, listing.Pass, "regular lease (Anmeldung is standard)")
	}
	return l
}

func firstSub(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

// kaAvailable reads the "Verfügbar ab" fact: "Dezember 2026" → "01.12.2026";
// a date or "sofort" as written.
func kaAvailable(v string) string {
	if m := reKAMonth.FindStringSubmatch(strings.TrimSpace(v)); m != nil {
		return fmt.Sprintf("01.%02d.%s", kaMonths[strings.ToLower(m[1])], m[2])
	}
	if a := analyze.FindAvailableFrom("verfügbar ab " + v); a != "" {
		return a
	}
	return ""
}
