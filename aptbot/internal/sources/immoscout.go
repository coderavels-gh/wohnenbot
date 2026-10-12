package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"aptbot/internal/analyze"
	"aptbot/internal/config"
	"aptbot/internal/listing"
	"aptbot/internal/web"
)

// ImmoScout uses ImmoScout24's mobile-app API (the same approach as the
// open-source flathunter project). The website itself sits behind bot
// protection; the app API returns plain JSON.
//
// You build the search on the website (filters, area, sort = newest) and
// paste the result URL into config; it is converted to API parameters.
type ImmoScout struct {
	Client *web.Client
	Cfg    config.ImmoScoutConfig
}

func (s *ImmoScout) Name() string { return "ImmoScout24" }

const (
	isAPIBase  = "https://api.mobile.immobilienscout24.de"
	isAppUA    = "ImmoScout_27.3_26.0_._"
	isExposeWS = "https://www.immobilienscout24.de/expose/"
)

var isHeaders = map[string]string{
	"User-Agent":   isAppUA,
	"Accept":       "application/json",
	"Content-Type": "application/json",
}

var isTypes = map[string]string{
	"wohnung-mieten": "apartmentrent",
	"haus-mieten":    "houserent",
	"wohnung-kaufen": "apartmentbuy",
	"haus-kaufen":    "housebuy",
}

// SearchURLToAPI converts a website search URL to a mobile-API list URL.
//
//	https://www.immobilienscout24.de/Suche/de/hamburg/hamburg/wohnung-mieten?price=-1600.0&sorting=2
//	→ https://api.mobile.immobilienscout24.de/search/list?geocodes=%2Fde%2Fhamburg%2Fhamburg&...
func SearchURLToAPI(webURL string) (string, error) {
	u, err := url.Parse(webURL)
	if err != nil {
		return "", err
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 3 || !strings.EqualFold(parts[0], "Suche") {
		return "", fmt.Errorf("not an ImmoScout search URL (expected /Suche/...): %s", webURL)
	}
	typ := strings.ToLower(parts[len(parts)-1])
	apiType, ok := isTypes[typ]
	if !ok {
		return "", fmt.Errorf("unsupported listing type %q in %s", typ, webURL)
	}
	q := u.Query()
	q.Del("enteredFrom")
	q.Set("realestatetype", apiType)
	if parts[1] == "radius" {
		q.Set("searchType", "radius")
	} else {
		q.Set("searchType", "region")
		q.Set("geocodes", "/"+strings.Join(parts[1:len(parts)-1], "/"))
	}
	q.Set("pagesize", "50")
	if q.Get("sorting") == "" {
		q.Set("sorting", "2") // newest first
	}
	return isAPIBase + "/search/list?" + q.Encode(), nil
}

func (s *ImmoScout) apiURLs() []string {
	urls := append([]string{}, s.Cfg.APIURLs...)
	for _, w := range s.Cfg.SearchURLs {
		a, err := SearchURLToAPI(w)
		if err != nil {
			logf(s.Name(), "%v", err)
			continue
		}
		urls = append(urls, a)
	}
	return urls
}

type isItem struct {
	ID         string
	Title      string
	Address    string
	Lat, Lon   float64
	Attributes []string
	Raw        map[string]any
}

func (s *ImmoScout) list(ctx context.Context, apiURL string) ([]isItem, error) {
	body := []byte(`{"supportedResultListType":[],"userData":{}}`)
	data, err := s.Client.Do(ctx, "POST", apiURL, body, isHeaders)
	if err != nil {
		return nil, err
	}
	return ParseImmoScoutList(data)
}

// ParseImmoScoutList parses the mobile-API search response.
func ParseImmoScoutList(data []byte) ([]isItem, error) {
	var resp struct {
		ResultListItems []struct {
			Type string         `json:"type"`
			Item map[string]any `json:"item"`
		} `json:"resultListItems"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("decode search response: %w", err)
	}
	var out []isItem
	for _, e := range resp.ResultListItems {
		if e.Type != "EXPOSE_RESULT" || e.Item == nil {
			continue
		}
		it := isItem{Raw: e.Item}
		it.ID = fmt.Sprint(e.Item["id"])
		it.Title, _ = e.Item["title"].(string)
		if addr, ok := e.Item["address"].(map[string]any); ok {
			it.Address, _ = addr["line"].(string)
			it.Lat = numField(addr, "lat", "latitude")
			it.Lon = numField(addr, "lon", "lng", "longitude")
		}
		if attrs, ok := e.Item["attributes"].([]any); ok {
			for _, a := range attrs {
				if m, ok := a.(map[string]any); ok {
					if v, ok := m["value"].(string); ok {
						it.Attributes = append(it.Attributes, v)
					}
				}
			}
		}
		if it.ID != "" && it.ID != "<nil>" {
			out = append(out, it)
		}
	}
	return out, nil
}

func numField(m map[string]any, keys ...string) float64 {
	for _, k := range keys {
		switch v := m[k].(type) {
		case float64:
			return v
		case string:
			if f, ok := analyze.ParseGermanNumber(v); ok {
				return f
			}
		}
	}
	return 0
}

// FlattenJSON turns an arbitrary JSON document into "label: value" lines so
// the same German keyword analysis works on API responses. Objects with a
// label/title plus text/value become one line; other strings are emitted as is.
func FlattenJSON(v any) string {
	var b strings.Builder
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			label := firstString(t, "label", "title", "name", "headline")
			value := firstString(t, "text", "value", "content", "description")
			if label != "" && value != "" {
				b.WriteString(label + ": " + value + "\n")
			}
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if label != "" && value != "" && isOneOf(k, "label", "title", "name", "headline", "text", "value", "content", "description") {
					continue
				}
				switch child := t[k].(type) {
				case string:
					if strings.HasPrefix(child, "http") || len(child) < 2 {
						continue
					}
					b.WriteString(child + "\n")
				case bool:
					if child {
						b.WriteString(k + ": ja\n")
					}
				default:
					walk(child)
				}
			}
		case []any:
			for _, c := range t {
				walk(c)
			}
		case string:
			if !strings.HasPrefix(t, "http") {
				b.WriteString(t + "\n")
			}
		}
	}
	walk(v)
	return b.String()
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func isOneOf(s string, opts ...string) bool {
	for _, o := range opts {
		if s == o {
			return true
		}
	}
	return false
}

var (
	reISStreet   = regexp.MustCompile(`^[^,\d]*[A-Za-zÄÖÜäöüß.]\s*\d+[a-zA-Z]?\s*,`) // "Hoheluftchaussee 12, ..."
	reISDistrict = regexp.MustCompile(`\d{5}\s+Hamburg,?\s*([^,\n]+)?`)
	// "teilmöbliert" (partly furnished) counts as unfurnished for the rent limit.
	reISFurnished   = regexp.MustCompile(`(?i)\bmöbliert|vollmöbliert|voll möbliert|\bfurnished\b|möbliert\s*:\s*ja`)
	reISUnfurnished = regexp.MustCompile(`(?i)unmöbliert|nicht möbliert|teilmöbliert|teilweise möbliert|unfurnished|möbliert\s*:\s*nein`)
	reISTemporary   = regexp.MustCompile(`(?i)möbliert|furnished|auf zeit|befristet|zeitmiet|zwischenmiete|untermiete|wg-zimmer`)
)

// BuildImmoScoutListing combines list data and (optional) expose JSON.
func BuildImmoScoutListing(it isItem, expose []byte) *listing.Listing {
	l := &listing.Listing{
		Source: "ImmoScout24", ID: it.ID, URL: isExposeWS + it.ID,
		Title: it.Title, Address: it.Address, Lat: it.Lat, Lon: it.Lon,
	}
	l.AddressPrecise = reISStreet.MatchString(it.Address) || it.Lat != 0
	if m := reISDistrict.FindStringSubmatch(it.Address); m != nil && len(m) > 1 {
		l.District = strings.TrimSpace(m[1])
	}
	var listPrice float64
	for _, a := range it.Attributes {
		switch {
		case strings.Contains(a, "€"):
			listPrice, _ = analyze.ParseGermanNumber(strings.TrimSpace(strings.ReplaceAll(a, "€", "")))
		case strings.Contains(a, "m²"):
			l.SizeM2, _ = analyze.ParseGermanNumber(strings.TrimSpace(strings.ReplaceAll(a, "m²", "")))
		case strings.Contains(a, "Zi"):
			l.Rooms, _ = analyze.ParseGermanNumber(strings.TrimSpace(strings.Split(a, "Zi")[0]))
		}
	}
	text := it.Title + "\n" + it.Address + "\n" + strings.Join(it.Attributes, "\n") + "\n" + FlattenJSON(it.Raw)
	if len(expose) > 0 {
		var doc any
		if json.Unmarshal(expose, &doc) == nil {
			text += "\n" + FlattenJSON(doc)
		}
	}
	l.Text = cleanText(text)
	r := analyze.FindWarmRent(l.Text, "")
	switch {
	case r.Warm > 0:
		l.WarmRent, l.RentNote = r.Warm, r.Note
	case listPrice > 0:
		// With pricetype=calculatedtotalrent in the search, the list price is warm rent.
		l.WarmRent, l.RentNote = listPrice, "list price (set pricetype=calculatedtotalrent in your search)"
	}
	if size := analyze.FindArea(l.Text); l.SizeM2 == 0 {
		l.SizeM2 = size
	}
	if l.Rooms == 0 {
		l.Rooms = analyze.FindRooms(l.Text)
	}
	l.AvailableFrom = analyze.FindAvailableFrom(l.Text)
	l.Furnished = reISFurnished.MatchString(l.Text) && !reISUnfurnished.MatchString(l.Text)
	if !reISTemporary.MatchString(l.Text) {
		// A normal (unfurnished, open-ended) German lease: Anmeldung is a legal
		// right and the contract is unlimited unless the ad says otherwise.
		l.SetDefault(listing.CritDuration, listing.Pass, "regular lease, no time limit mentioned")
		l.SetDefault(listing.CritAnmeldung, listing.Pass, "regular lease (Anmeldung is standard)")
	}
	return l
}

func (s *ImmoScout) Fetch(ctx context.Context, seen func(string) bool) ([]*listing.Listing, error) {
	urls := s.apiURLs()
	if len(urls) == 0 {
		return nil, fmt.Errorf("no search_urls configured")
	}
	var out []*listing.Listing
	known := map[string]bool{}
	var firstErr error
	for _, u := range urls {
		items, err := s.list(ctx, u)
		if err != nil {
			logf(s.Name(), "search: %v", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		logf(s.Name(), "%d listings in search", len(items))
		for _, it := range items {
			if known[it.ID] || seen(s.Name()+":"+it.ID) {
				continue
			}
			known[it.ID] = true
			var expose []byte
			if s.Cfg.FetchExpose {
				expose, err = s.Client.Get(ctx, isAPIBase+"/expose/"+it.ID, isHeaders)
				if err != nil {
					logf(s.Name(), "expose %s: %v (using list data only)", it.ID, err)
					expose = nil
				}
			}
			out = append(out, BuildImmoScoutListing(it, expose))
		}
	}
	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}
