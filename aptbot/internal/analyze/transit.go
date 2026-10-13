package analyze

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"aptbot/internal/config"
	"aptbot/internal/listing"
	"aptbot/internal/web"
)

const (
	ModeBus   = "Bus"
	ModeUBahn = "U-Bahn"
	ModeSBahn = "S-Bahn"
)

// TransitChecker counts distinct transit modes near a listing, using
// OpenStreetMap (Nominatim geocoding + Overpass stop lookup) when the address
// is precise, and the listing's own "Lage" text otherwise.
type TransitChecker struct {
	client  *web.Client
	ua      string
	mu      sync.Mutex
	geo     map[string]geoResult
	nearby  map[string]map[string]string // "lat,lon" → mode → example stop
	Offline bool                         // tests: text-only
	server  int                          // index into overpassServers
	osmDown error                        // every server failed this run: stop asking
}

func NewTransitChecker(client *web.Client, contactEmail string) *TransitChecker {
	ua := "aptbot-hamburg/1.0 (personal flat search)"
	if contactEmail != "" {
		ua += " " + contactEmail
	}
	return &TransitChecker{client: client, ua: ua, geo: map[string]geoResult{}, nearby: map[string]map[string]string{}}
}

// ---- text-based ------------------------------------------------------------

var (
	reTextU   = regexp.MustCompile(`\bU\s?-?[1-5]\b|U-Bahn|U-Bhf|U-Station|\bU [A-ZÄÖÜ][a-zäöüß]+`)
	reTextS   = regexp.MustCompile(`\bS\s?-?[1-9]\d?\b|S-Bahn|S-Bhf|\bS [A-ZÄÖÜ][a-zäöüß]+`)
	reTextBus = regexp.MustCompile(`(?i)\bbus(?:se|linie|linien|haltestelle|station|stop)?\b|metrobus|schnellbus|\bbus\s*\d`)
)

func modesFromText(text string) map[string]string {
	out := map[string]string{}
	for mode, re := range map[string]*regexp.Regexp{ModeUBahn: reTextU, ModeSBahn: reTextS, ModeBus: reTextBus} {
		if loc := re.FindStringIndex(text); loc != nil {
			out[mode] = strings.TrimSpace(text[loc[0]:loc[1]])
		}
	}
	return out
}

func describe(modes map[string]string) string {
	var parts []string
	for m, ex := range modes {
		if ex != "" && ex != m {
			parts = append(parts, fmt.Sprintf("%s (%s)", m, ex))
		} else {
			parts = append(parts, m)
		}
	}
	sort.Strings(parts)
	if len(parts) == 0 {
		return "none found"
	}
	return strings.Join(parts, ", ")
}

// ---- OSM -------------------------------------------------------------------

type geoResult struct {
	Lat, Lon float64
	Precise  bool
	OK       bool
}

func (t *TransitChecker) geocode(ctx context.Context, q string) geoResult {
	t.mu.Lock()
	if g, ok := t.geo[q]; ok {
		t.mu.Unlock()
		return g
	}
	t.mu.Unlock()
	u := "https://nominatim.openstreetmap.org/search?format=jsonv2&limit=1&countrycodes=de&q=" + url.QueryEscape(q)
	body, err := t.client.Get(ctx, u, map[string]string{"User-Agent": t.ua, "Accept": "application/json"})
	res := geoResult{}
	if err != nil {
		log.Printf("transit: geocode %q: %v", q, err)
		return res // not cached, retried next time
	}
	var hits []struct {
		Lat, Lon    string
		Category    string `json:"category"`
		Type        string `json:"type"`
		AddressType string `json:"addresstype"`
	}
	if json.Unmarshal(body, &hits) == nil && len(hits) > 0 {
		h := hits[0]
		res.Lat, _ = strconv.ParseFloat(h.Lat, 64)
		res.Lon, _ = strconv.ParseFloat(h.Lon, 64)
		res.OK = res.Lat != 0
		switch h.AddressType {
		case "postcode", "suburb", "city_district", "quarter", "neighbourhood", "city", "town", "borough", "state", "county", "village":
		default:
			res.Precise = h.Category != "boundary"
		}
	}
	t.mu.Lock()
	t.geo[q] = res
	t.mu.Unlock()
	return res
}

func (t *TransitChecker) stopsNear(ctx context.Context, lat, lon float64, busR, railR int) (map[string]string, error) {
	key := fmt.Sprintf("%.4f,%.4f,%d,%d", lat, lon, busR, railR)
	t.mu.Lock()
	if m, ok := t.nearby[key]; ok {
		t.mu.Unlock()
		return m, nil
	}
	t.mu.Unlock()
	q := fmt.Sprintf(`[out:json][timeout:25];(
node(around:%[1]d,%[3]f,%[4]f)[highway=bus_stop];
node(around:%[1]d,%[3]f,%[4]f)[public_transport=platform][bus=yes];
node(around:%[2]d,%[3]f,%[4]f)[railway~"^(station|halt|stop)$"];
node(around:%[2]d,%[3]f,%[4]f)[public_transport=station];
);out tags;`, busR, railR, lat, lon)
	body, err := t.overpass(ctx, q)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Elements []struct {
			Tags map[string]string `json:"tags"`
		} `json:"elements"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("overpass: %w", err)
	}
	modes := map[string]string{}
	for _, e := range resp.Elements {
		if notInService(e.Tags) {
			continue
		}
		for _, m := range classifyStop(e.Tags) {
			if _, ok := modes[m]; !ok {
				modes[m] = e.Tags["name"]
			}
		}
	}
	t.mu.Lock()
	t.nearby[key] = modes
	t.mu.Unlock()
	return modes, nil
}

// notInService: stations under construction, planned or closed, such as the
// new U5 stops tagged "U Steilshoop (in Bau)".
func notInService(tags map[string]string) bool {
	name := strings.ToLower(tags["name"])
	if strings.Contains(name, "in bau") || strings.Contains(name, "geplant") {
		return true
	}
	for _, k := range []string{"construction", "proposed", "disused", "abandoned"} {
		if tags[k] != "" && tags[k] != "no" || tags["railway"] == k || tags["public_transport"] == k {
			return true
		}
	}
	return false
}

// Public Overpass servers; the main one often answers 504 when busy.
var overpassServers = []string{
	"https://overpass-api.de/api/interpreter",
	"https://overpass.private.coffee/api/interpreter",
}

// overpass runs a query, moving on to the next server when one fails and
// staying there for the rest of the run.
func (t *TransitChecker) overpass(ctx context.Context, q string) ([]byte, error) {
	t.mu.Lock()
	down := t.osmDown
	t.mu.Unlock()
	if down != nil {
		return nil, down
	}
	var lastErr error
	for tries := 0; tries < len(overpassServers); tries++ {
		t.mu.Lock()
		server := overpassServers[t.server%len(overpassServers)]
		t.mu.Unlock()
		body, err := t.client.Do(ctx, "POST", server, []byte("data="+url.QueryEscape(q)),
			map[string]string{"User-Agent": t.ua, "Content-Type": "application/x-www-form-urlencoded", "Accept": "application/json"})
		if err == nil && json.Valid(body) {
			return body, nil
		}
		if err == nil {
			err = fmt.Errorf("non-JSON answer from %s", server)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		lastErr = err
		log.Printf("transit: %s failed, switching server: %.120v", server, err)
		t.mu.Lock()
		t.server++
		t.mu.Unlock()
	}
	t.mu.Lock()
	t.osmDown = fmt.Errorf("all Overpass servers failed this run, using listing text: %w", lastErr)
	t.mu.Unlock()
	log.Printf("transit: %v", t.osmDown)
	return nil, lastErr
}

func classifyStop(tags map[string]string) []string {
	var out []string
	name := tags["name"]
	network := strings.ToLower(tags["network"] + " " + tags["operator"])
	if tags["highway"] == "bus_stop" || tags["bus"] == "yes" {
		out = append(out, ModeBus)
	}
	if tags["station"] == "subway" || tags["subway"] == "yes" || strings.HasPrefix(name, "U ") {
		out = append(out, ModeUBahn)
	}
	if tags["station"] == "light_rail" || tags["light_rail"] == "yes" || strings.Contains(network, "s-bahn") ||
		strings.HasPrefix(name, "S ") || (tags["railway"] == "station" && tags["train"] == "yes") ||
		(tags["railway"] == "halt" && tags["station"] != "subway") {
		out = append(out, ModeSBahn)
	}
	return out
}

// Check decides the transit criterion for one listing.
func (t *TransitChecker) Check(ctx context.Context, l *listing.Listing, cr config.Criteria) listing.Check {
	c := listing.Check{Name: listing.CritTransit}
	need := cr.MinTransitModes

	if !t.Offline {
		lat, lon, precise := l.Lat, l.Lon, l.Lat != 0
		if !precise && l.AddressPrecise && l.Address != "" {
			g := t.geocode(ctx, l.Address)
			lat, lon, precise = g.Lat, g.Lon, g.OK && g.Precise
			if precise {
				l.Lat, l.Lon = lat, lon // stored with the listing; a re-check needs no geocoding
			}
		}
		if precise {
			modes, err := t.stopsNear(ctx, lat, lon, cr.BusRadiusM, cr.RailRadiusM)
			if err == nil {
				c.Detail = fmt.Sprintf("%s within %dm bus / %dm rail (OSM)", describe(modes), cr.BusRadiusM, cr.RailRadiusM)
				if len(modes) >= need {
					c.Result = listing.Pass
				} else {
					c.Result = listing.Fail
				}
				return c
			}
			log.Printf("transit: overpass for %s: %v", l.Key(), err)
		}
	}
	src := l.TransitText
	if src == "" {
		src = l.Title + "\n" + l.Text
	}
	modes := modesFromText(src)
	c.Detail = describe(modes) + " (from listing text"
	if !l.AddressPrecise {
		c.Detail += "; no street address"
	}
	c.Detail += ")"
	if len(modes) >= need {
		c.Result = listing.Pass
	}
	return c
}
