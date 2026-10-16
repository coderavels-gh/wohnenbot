package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"aptbot/internal/analyze"
	"aptbot/internal/config"
	"aptbot/internal/listing"
	"aptbot/internal/web"
)

// Immowelt reads https://www.immowelt.de search results ("classified-search").
//
// Note: immowelt's robots.txt disallows classified-search pages. The user
// decided to read them anyway (personal use, a few requests per hour), since
// the allowed /suche/ landing pages ignore every filter and sort parameter.
// Exposé pages (/expose/…) are allowed.
//
// Results come sorted by "DateDesc", which mixes newly created listings with
// recently refreshed old ones, so each run reads pages while they still hold
// unseen listings or ones created within the last day. Every result carries
// warm/cold rent, size, rooms, address and availability: a flat that fails on
// those (Prefilter) is recorded from the search data alone, and only the rest
// get their exposé fetched.
type Immowelt struct {
	Client    *web.Client
	Cfg       config.ImmoweltConfig
	Prefilter func(*listing.Listing) bool
	// FullScan reads every result page (first run, criteria change).
	FullScan bool
	Now      func() time.Time
}

func (iw *Immowelt) Name() string          { return "Immowelt" }
func (iw *Immowelt) SetFullScan(full bool) { iw.FullScan = full }

const (
	iwPerPage    = 30
	iwFullPages  = 40 // cap for a full scan (~1,200 results)
	iwFreshAfter = 24 * time.Hour
)

// pageURL sets newest-first sorting and the page number on a search URL.
func iwPageURL(search string, page int) (string, error) {
	u, err := url.Parse(search)
	if err != nil {
		return "", err
	}
	if !strings.Contains(u.Path, "classified-search") {
		return "", fmt.Errorf("not an immowelt classified-search URL: %s", search)
	}
	q := u.Query()
	q.Set("order", "DateDesc")
	q.Del("page")
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (iw *Immowelt) Fetch(ctx context.Context, seen func(string) bool, emit func(*listing.Listing)) error {
	now := time.Now()
	if iw.Now != nil {
		now = iw.Now()
	}
	maxPages := iw.Cfg.MaxPages
	if iw.FullScan {
		maxPages = iwFullPages
	}
	var todo []*listing.Listing
	known := map[string]bool{}
	var firstErr error
	for _, search := range iw.Cfg.SearchURLs {
		for page := 1; page <= maxPages; page++ {
			u, err := iwPageURL(search, page)
			if err != nil {
				return err
			}
			body, err := iw.Client.Get(ctx, u, nil)
			if err != nil {
				logf(iw.Name(), "search page %d: %v", page, err)
				if page == 1 && firstErr == nil {
					firstErr = err
				}
				break
			}
			cards, total, err := ParseImmoweltSearch(string(body))
			if err != nil {
				logf(iw.Name(), "search page %d: %v", page, err)
				if page == 1 && firstErr == nil {
					firstErr = err
				}
				break
			}
			unseen, fresh := 0, 0
			for _, c := range cards {
				if known[c.ID] {
					continue
				}
				known[c.ID] = true
				if now.Sub(c.FirstSeen) < iwFreshAfter { // FirstSeen holds the creation date here
					fresh++
				}
				if seen(iw.Name() + ":" + c.ID) {
					continue
				}
				unseen++
				todo = append(todo, c)
			}
			if page == 1 {
				if iw.FullScan {
					logf(iw.Name(), "full scan (first run or criteria change): %d results", total)
				} else {
					logf(iw.Name(), "%d results", total)
				}
			}
			if len(cards) == 0 || page*iwPerPage >= total {
				break
			}
			if !iw.FullScan && unseen == 0 && fresh == 0 {
				break // nothing new on this page and nothing recent: deeper pages are older
			}
		}
	}

	skipped := 0
	for _, card := range todo {
		card.FirstSeen = time.Time{}
		if iw.Prefilter != nil && iw.Prefilter(card) {
			skipped++
			emit(card)
			continue
		}
		body, err := iw.Client.Get(ctx, card.URL, nil)
		if err != nil {
			logf(iw.Name(), "expose %s: %v", card.URL, err)
			continue
		}
		l := ParseImmoweltExpose(string(body))
		l.ID, l.URL = card.ID, card.URL
		if card.Title != "" {
			l.Title = card.Title // the exposé's <h1> holds the whole header block
		}
		if l.WarmRent == 0 {
			l.WarmRent, l.RentNote, l.RentLowerBound = card.WarmRent, card.RentNote, card.RentLowerBound
		}
		mergeCard(l, card)
		if !l.AddressPrecise && card.AddressPrecise {
			l.Address, l.AddressPrecise = card.Address, true
		}
		emit(l)
	}
	if skipped > 0 {
		logf(iw.Name(), "%d new listing(s) rejected from the search data alone (no exposé request)", skipped)
	}
	if len(todo) == 0 && firstErr != nil {
		return firstErr
	}
	return nil
}

var reIWFetcher = regexp.MustCompile(`(?s)window\["__UFRN_FETCHER__"\]=JSON\.parse\((".*?")\);?\s*</script>`)

type iwClassified struct {
	ID       string `json:"id"`
	URL      string `json:"url"`
	Metadata struct {
		CreationDate time.Time `json:"creationDate"`
	} `json:"metadata"`
	Location struct {
		Address struct {
			City     string `json:"city"`
			ZipCode  string `json:"zipCode"`
			Street   string `json:"street"`
			District string `json:"district"`
		} `json:"address"`
		IsAddressPublished bool `json:"isAddressPublished"`
	} `json:"location"`
	HardFacts struct {
		Title string `json:"title"`
		Facts []struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		} `json:"facts"`
		Prices []struct {
			Label     string `json:"label"`
			AriaLabel string `json:"ariaLabel"`
		} `json:"prices"`
	} `json:"hardFacts"`
	MainDescription struct {
		Headline string `json:"headline"`
	} `json:"mainDescription"`
	RawData struct {
		Surface struct {
			Main float64 `json:"main"`
		} `json:"surface"`
		NbRoom json.Number `json:"nbroom"`
	} `json:"rawData"`
}

// ParseImmoweltSearch decodes one search result page: the listings in page
// order (as card-only listings) and the total number of results.
func ParseImmoweltSearch(page string) ([]*listing.Listing, int, error) {
	m := reIWFetcher.FindStringSubmatch(page)
	if m == nil {
		return nil, 0, fmt.Errorf("no embedded search data (page layout changed?)")
	}
	var inner string
	if err := json.Unmarshal([]byte(m[1]), &inner); err != nil {
		return nil, 0, fmt.Errorf("embedded data: %w", err)
	}
	var fetcher struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(inner), &fetcher); err != nil {
		return nil, 0, fmt.Errorf("embedded data: %w", err)
	}
	var blob string
	for k, v := range fetcher.Data {
		if strings.Contains(k, "serp-init") {
			json.Unmarshal(v, &blob)
		}
	}
	if blob == "" {
		return nil, 0, fmt.Errorf("no search results in embedded data")
	}
	raw, err := web.LZDecompressFromBase64(blob)
	if err != nil {
		return nil, 0, err
	}
	var serp struct {
		PageProps struct {
			TotalCount      int                     `json:"totalCount"`
			Classifieds     []string                `json:"classifieds"`
			ClassifiedsData map[string]iwClassified `json:"classifiedsData"`
		} `json:"pageProps"`
	}
	if err := json.Unmarshal([]byte(raw), &serp); err != nil {
		return nil, 0, fmt.Errorf("search results: %w", err)
	}
	pp := serp.PageProps
	var out []*listing.Listing
	for _, id := range pp.Classifieds {
		c, ok := pp.ClassifiedsData[id]
		if !ok {
			continue
		}
		out = append(out, iwCard(c))
	}
	return out, pp.TotalCount, nil
}

var reIWAmount = regexp.MustCompile(`[0-9]+(?:\.[0-9]+)?`)

func iwAmount(aria string) float64 {
	f, _ := strconv.ParseFloat(reIWAmount.FindString(aria), 64) // "1545.43 €"
	return f
}

func iwCard(c iwClassified) *listing.Listing {
	a := c.Location.Address
	l := &listing.Listing{Source: "Immowelt", ID: c.ID, URL: c.URL, Partial: true,
		Title: c.MainDescription.Headline, District: a.District, FirstSeen: c.Metadata.CreationDate}
	if l.Title == "" {
		l.Title = c.HardFacts.Title
	}
	var cold, warm float64
	for _, p := range c.HardFacts.Prices {
		switch p.Label {
		case "Kaltmiete":
			cold = iwAmount(p.AriaLabel)
		case "Warmmiete":
			warm = iwAmount(p.AriaLabel)
		}
	}
	switch {
	case warm > 0 && warm >= cold:
		l.WarmRent, l.RentNote = warm, "Warmmiete"
	case cold > 0:
		// No (or an implausible) warm rent: the cold rent is a lower bound, enough
		// to reject a flat that is too expensive; the exposé decides the rest.
		l.WarmRent, l.RentNote, l.RentLowerBound = cold, "Kaltmiete only", true
	}
	l.SizeM2 = c.RawData.Surface.Main
	l.Rooms, _ = strconv.ParseFloat(c.RawData.NbRoom.String(), 64)
	for _, f := range c.HardFacts.Facts {
		if f.Type == "availability" {
			l.AvailableFrom = analyze.FindAvailableFrom(f.Value) // "frei ab 01.12.2026"
		}
	}
	city := a.City
	if city == "" {
		city = "Hamburg"
	}
	if c.Location.IsAddressPublished && a.Street != "" {
		l.Address = strings.TrimSpace(a.Street + ", " + a.ZipCode + " " + city)
		l.AddressPrecise = true
	} else {
		l.Address = strings.TrimSpace(a.ZipCode + " " + city)
	}
	l.Furnished = reISFurnished.MatchString(l.Title) && !reISUnfurnished.MatchString(l.Title)
	l.StrictStart = true
	return l
}

// ParseImmoweltExpose extracts a listing from an exposé page. The text
// between "Zurück zur Suche" and the provider's "Impressum" is the listing
// itself; above it is the site navigation.
func ParseImmoweltExpose(page string) *listing.Listing {
	text := web.HTMLToText(page)
	if i := strings.Index(text, "Zurück zur Suche"); i >= 0 {
		text = text[i+len("Zurück zur Suche"):]
	}
	if i := strings.LastIndex(text, "Impressum"); i > 0 {
		text = text[:i]
	}
	text = cleanText(text)
	l := &listing.Listing{Source: "Immowelt", Title: web.Title(page), Text: text}
	r := analyze.FindWarmRent(text, "")
	l.WarmRent, l.RentNote = r.Warm, r.Note
	l.SizeM2 = analyze.FindArea(text)
	l.Rooms = analyze.FindRooms(text)
	l.AvailableFrom = analyze.FindAvailableFrom(text)
	l.Furnished = reISFurnished.MatchString(text) && !reISUnfurnished.MatchString(text)
	l.StrictStart = true
	if !reISTemporary.MatchString(text) {
		// A normal open-ended lease, as on ImmoScout.
		l.SetDefault(listing.CritDuration, listing.Pass, "regular lease, no time limit mentioned")
		l.SetDefault(listing.CritAnmeldung, listing.Pass, "regular lease (Anmeldung is standard)")
	}
	return l
}
