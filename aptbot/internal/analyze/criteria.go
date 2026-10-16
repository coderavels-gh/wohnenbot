package analyze

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"aptbot/internal/config"
	"aptbot/internal/listing"
)

func rx(patterns ...string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, len(patterns))
	for i, p := range patterns {
		out[i] = regexp.MustCompile(`(?i)` + p)
	}
	return out
}

// firstMatch returns evidence for the first regex that matches.
func firstMatch(text string, res []*regexp.Regexp) (string, bool) {
	for _, re := range res {
		if loc := re.FindStringIndex(text); loc != nil {
			return Snippet(text, loc[0], loc[1]), true
		}
	}
	return "", false
}

func strip(text string, res []*regexp.Regexp) string {
	for _, re := range res {
		text = re.ReplaceAllString(text, " ")
	}
	return text
}

// ---- Balcony ---------------------------------------------------------------

var (
	balconyNeg = rx(`\bkein(?:e|en)?\s+(?:balkon|terrasse|loggia)`, `\bohne\s+(?:balkon|terrasse|loggia)`, `balkon\s*:\s*nein`, `no balcony`)
	// "Balkon/Garten" is City-Wohnen's combined filter tag; it says nothing
	// about whether this flat has a balcony.
	balconyNoise = rx(`balkon\s*/\s*garten`, `wohnungen mit balkon[^\n]*`)
	balconyPos   = rx(`balkon\s*:\s*ja`, `balcony\s*:\s*ja`, `\bbalkon(?:e|en)?\b`, `\bloggia`, `terrasse`, `wintergarten`, `\bbalcony`, `\bterrace`, `südbalkon|westbalkon|ostbalkon|nordbalkon`)
)

func checkBalcony(text string) listing.Check {
	c := listing.Check{Name: listing.CritBalcony}
	if ev, ok := firstMatch(text, balconyNeg); ok {
		c.Result, c.Detail = listing.Fail, ev
		return c
	}
	clean := strip(text, balconyNoise)
	if ev, ok := firstMatch(clean, balconyPos); ok {
		c.Result, c.Detail = listing.Pass, ev
		return c
	}
	c.Detail = "not mentioned"
	return c
}

// ---- Washing machine -------------------------------------------------------

var (
	washerNeg    = rx(`\bkeine\s+waschmaschine`, `\bohne\s+waschmaschine`, `waschmaschine\s*:\s*nein`, `no washing machine`)
	washerShared = rx(`gemeinschafts-?waschmaschine`, `münzwaschmaschine`, `waschkeller`, `waschküche`, `waschsalon`, `gemeinschaftswaschraum`, `shared (?:washing machine|laundry)`)
	washerConn   = rx(`waschmaschinen-?anschl(?:uss|üsse)`, `wm-anschluss`, `anschluss für (?:die |eine )?waschmaschine`, `waschmaschinen-?stellplatz`, `washing machine connection`)
	washerPos    = rx(`waschmaschine`, `waschtrockner`, `washing machine`, `\bwasher\b`)
)

func checkWasher(text string, connectionOK bool) listing.Check {
	c := listing.Check{Name: listing.CritWasher}
	if ev, ok := firstMatch(text, washerNeg); ok {
		c.Result, c.Detail = listing.Fail, ev
		return c
	}
	sharedEv, shared := firstMatch(text, washerShared)
	connEv, conn := firstMatch(text, washerConn)
	rest := strip(strip(text, washerShared), washerConn)
	if ev, ok := firstMatch(rest, washerPos); ok {
		if shared {
			c.Detail = "mentions a washer but also shared laundry: " + sharedEv
			return c // Unknown → review
		}
		c.Result, c.Detail = listing.Pass, ev
		return c
	}
	if conn {
		if connectionOK {
			c.Result = listing.Pass
		}
		c.Detail = "hook-up only (bring own): " + connEv
		return c
	}
	if shared {
		c.Detail = "shared laundry only: " + sharedEv
		return c
	}
	c.Detail = "not mentioned"
	return c
}

// ---- Kitchen ---------------------------------------------------------------

var (
	kitchenNeg    = rx(`\bohne\s+(?:einbau)?küche`, `\bkeine\s+(?:einbau)?küche`, `einbauküche\s*:\s*nein`, `\bküche\s*:\s*nein`, `küche (?:ist )?nicht vorhanden`, `no kitchen`)
	kitchenStrong = rx(`einbauküche\s*:\s*ja`, `builtinkitchen\s*:\s*ja`, `einbauküche`, `\bebk\b`, `küchenzeile`, `pantryküche`, `kochnische`, `voll ?ausgestattete küche`, `kochfeld`, `ceranfeld`, `induktion`, `\bherd\b`, `backofen`, `built-?in kitchen`, `fitted kitchen`, `fully equipped kitchen`, `kitchenette`)
	kitchenWeak   = rx(`wohnküche`, `offene küche`, `\bküche\b`, `\bkitchen\b`)
	kitchenAbloes = rx(`ablöse`, `abstand für (?:die )?küche`, `küche (?:kann|muss) übernommen`)
)

func checkKitchen(text string) listing.Check {
	c := listing.Check{Name: listing.CritKitchen}
	if ev, ok := firstMatch(text, kitchenNeg); ok {
		c.Result, c.Detail = listing.Fail, ev
		return c
	}
	if ev, ok := firstMatch(text, kitchenStrong); ok {
		c.Result, c.Detail = listing.Pass, ev
		if ab, ok := firstMatch(text, kitchenAbloes); ok {
			c.Detail = ev + " | NOTE possible Ablöse: " + ab
		}
		return c
	}
	if ev, ok := firstMatch(text, kitchenWeak); ok {
		c.Detail = "kitchen room mentioned, equipment unclear: " + ev
		return c
	}
	c.Detail = "not mentioned"
	return c
}

// ---- Anmeldung -------------------------------------------------------------

var (
	anmeldungNeg = rx(`\bkeine\s+(?:polizeiliche\s+)?anmeldung`, `anmeldung[^.\n]{0,50}nicht\s+(?:möglich|gestattet|erlaubt)`, `\bohne\s+anmeldung`, `anmeldung\s+ausgeschlossen`, `keine\s+meldeadresse`, `no (?:city )?registration`, `registration (?:is )?not possible`)
	anmeldungPos = rx(`anmeldung\s+(?:ist\s+)?(?:\w+\s+){0,6}(?:möglich|erlaubt|kein problem|gestattet)`, `anmeldbar`, `anmeldefähig`, `meldebescheinigung`, `wohnungsgeberbestätigung`, `registration (?:is )?possible`, `anmeldung\s*:\s*ja`, `wohnsitz\s+(?:kann\s+)?angemeldet`)
)

func checkAnmeldung(text string) listing.Check {
	c := listing.Check{Name: listing.CritAnmeldung}
	if ev, ok := firstMatch(text, anmeldungNeg); ok {
		c.Result, c.Detail = listing.Fail, ev
		return c
	}
	if ev, ok := firstMatch(text, anmeldungPos); ok {
		c.Result, c.Detail = listing.Pass, ev
		return c
	}
	c.Detail = "not mentioned"
	return c
}

// ---- Duration --------------------------------------------------------------

var (
	durUnlimited = rx(`\bunbefristet`, `\bunbegrenzt`, `ohne befristung`, `auf unbestimmte zeit`, `long[- ]term`, `unlimited (?:contract|lease|rental)`, `langfristig zu vermieten`, `langfristige vermietung`)
	durMax       = regexp.MustCompile(`(?i)(?:maximale\s+miet(?:zeit|dauer)|höchstmietdauer|max(?:imal|\.)?\s*miet(?:zeit|dauer)|mietdauer\s*(?:max(?:imal|\.)?|bis)|befristet\s+(?:auf|für))\s*:?\s*(?:ca\.?\s*)?(\d{1,3})\s*(kalendermonat|monat|jahr|woche|tag|month|year|week|day)`)
	// "Untermiete ab Nov. für 3-6 Monate", "Mietdauer: 6–12 Monate". The unit
	// must end the word, so "3 Monatsmieten Kaution" doesn't count.
	durSpan = regexp.MustCompile(`(?i)(?:\bfür|mietdauer|mietzeit|zeitraum|vermietung)\s*:?\s*(?:ca\.?\s*)?(\d{1,2})\s*(?:-|–|bis)\s*(\d{1,2})\s*(monat|jahr|woche)(?:e|en)?\b`)
	// "Zwischenmiete für 6 Monate": a single length, only next to a word that
	// says the rental is temporary.
	durTempFor = regexp.MustCompile(`(?i)(?:untermiete|zwischenmiete|befristet|zeitmiete|auf zeit)[^.\n]{0,80}?\bfür\s+(?:ca\.?\s*)?(\d{1,2})\s*(monat|jahr|woche)(?:e|en)?\b`)
	// "Der Mietvertrag wird auf 5 Jahre befristet"
	durMaxAfter  = regexp.MustCompile(`(?i)(?:auf|für)\s+(?:ca\.?\s*)?(\d{1,3})\s*(kalendermonat|monat|jahr|woche|tag)\w*\s+befristet`)
	durUntil     = regexp.MustCompile(`(?i)befristet\s+bis\s*(?:zum\s*)?(\d{1,2}\.\d{1,2}\.\d{4})`)
	durRange     = regexp.MustCompile(`(\d{1,2}\.\d{1,2}\.\d{4})\s*(?:bis|-|–)\s*(\d{1,2}\.\d{1,2}\.\d{4})`)
	durTemporary = rx(`\bbefristet`, `zeitmietvertrag`, `wohnen auf zeit`, `zwischenmiete`, `untermiete`, `vorübergehend`, `temporary`, `sublet`)
)

func unitToMonths(n float64, unit string) float64 {
	u := strings.ToLower(unit)
	switch {
	case strings.HasPrefix(u, "jahr"), strings.HasPrefix(u, "year"):
		return n * 12
	case strings.HasPrefix(u, "woche"), strings.HasPrefix(u, "week"):
		return n / 4.35
	case strings.HasPrefix(u, "tag"), strings.HasPrefix(u, "day"):
		return n / 30.44
	}
	return n
}

// checkDuration measures date-based limits from leaseStart (move-in day, or today).
func checkDuration(text string, minMonths int, leaseStart time.Time) listing.Check {
	now := leaseStart
	c := listing.Check{Name: listing.CritDuration}
	min := float64(minMonths)
	verdict := func(months float64, ev string) listing.Check {
		c.Detail = fmt.Sprintf("~%.0f months max (%s)", months, ev)
		if months+0.5 >= min {
			c.Result = listing.Pass
		} else {
			c.Result = listing.Fail
		}
		return c
	}
	if m := durMax.FindStringSubmatchIndex(text); m != nil {
		n, _ := ParseGermanNumber(text[m[2]:m[3]])
		return verdict(unitToMonths(n, text[m[4]:m[5]]), Snippet(text, m[0], m[1]))
	}
	if m := durSpan.FindStringSubmatchIndex(text); m != nil {
		n, _ := ParseGermanNumber(text[m[4]:m[5]]) // upper bound of "3-6 Monate"
		return verdict(unitToMonths(n, text[m[6]:m[7]]), Snippet(text, m[0], m[1]))
	}
	if m := durTempFor.FindStringSubmatchIndex(text); m != nil {
		n, _ := ParseGermanNumber(text[m[2]:m[3]])
		return verdict(unitToMonths(n, text[m[4]:m[5]]), Snippet(text, m[0], m[1]))
	}
	if m := durMaxAfter.FindStringSubmatchIndex(text); m != nil {
		n, _ := ParseGermanNumber(text[m[2]:m[3]])
		return verdict(unitToMonths(n, text[m[4]:m[5]]), Snippet(text, m[0], m[1]))
	}
	if m := durUntil.FindStringSubmatchIndex(text); m != nil {
		if end, ok := ParseDate(text[m[2]:m[3]]); ok {
			return verdict(MonthsBetween(now, end), Snippet(text, m[0], m[1]))
		}
	}
	if ev, ok := firstMatch(text, durUnlimited); ok {
		c.Result, c.Detail = listing.Pass, ev
		return c
	}
	if m := durRange.FindStringSubmatchIndex(text); m != nil {
		start, ok1 := ParseDate(text[m[2]:m[3]])
		end, ok2 := ParseDate(text[m[4]:m[5]])
		if ok1 && ok2 && end.After(start) {
			if start.Before(now) {
				start = now
			}
			return verdict(MonthsBetween(start, end), Snippet(text, m[0], m[1]))
		}
	}
	if ev, ok := firstMatch(text, durTemporary); ok {
		c.Detail = "temporary, length not stated: " + ev
		return c
	}
	c.Detail = "not mentioned"
	return c
}

// ---- WBS -------------------------------------------------------------------
// Social housing reserved for holders of a Wohnberechtigungsschein. Always
// checked; a listing that doesn't mention it passes (never "?").

var (
	wbsNeg = rx(`\b(?:kein|ohne)(?:en)?\s+(?:wbs|wohnberechtigungsschein)`, `(?:wbs|wohnberechtigungsschein)\s+(?:ist\s+)?nicht\s+(?:erforderlich|notwendig|nötig|benötigt)`)
	wbsReq = rx(`\bnur\s+(?:mit|für|gegen)\s+(?:einem\s+|gültigem\s+|gültigen\s+)?(?:wbs|wohnberechtigungsschein)`,
		`(?:wbs|wohnberechtigungsschein)[^.\n]{0,30}(?:erforderlich|notwendig|benötigt|zwingend|vorausgesetzt|voraussetzung|muss vorliegen|vorhanden sein)`,
		`(?:wbs|wohnberechtigungsschein)[- ]?(?:pflichtig|gebunden)`, `\bwbs\s*(?:§|para)`, `\bwbs\s*:\s*(?:ja|erforderlich)`,
		`(?:mit|gegen)\s+(?:wbs|wohnberechtigungsschein)\b`, `öffentlich\s+geförderte?\s+wohnung`, `sozialwohnung`)
)

func checkWBS(text string) listing.Check {
	c := listing.Check{Name: listing.CritWBS, Result: listing.Pass, Detail: "no WBS requirement"}
	if _, ok := firstMatch(text, wbsNeg); ok {
		return c
	}
	if ev, ok := firstMatch(text, wbsReq); ok {
		c.Result, c.Detail = listing.Fail, "needs a WBS (social housing): "+ev
	}
	return c
}

// ---- City ------------------------------------------------------------------

// "22846 Norderstedt", "20357 Hamburg–Eimsbüttel", "22045 Hamburg, Tonndorf"
var rePostcodeTown = regexp.MustCompile(`\b(\d{5})\s+([A-ZÄÖÜ][\pL.]+(?:[ -][A-ZÄÖÜ][\pL.]+)?)`)

// checkCity rejects a flat whose address names another town. An address
// without a town (or none at all) passes: the sites are searched for the city.
func checkCity(l *listing.Listing, city string) listing.Check {
	c := listing.Check{Name: listing.CritCity, Result: listing.Pass, Detail: city}
	for _, s := range []string{l.Address, l.District} {
		if m := rePostcodeTown.FindStringSubmatch(s); m != nil {
			town := m[2]
			if !strings.EqualFold(town, city) && !strings.HasPrefix(strings.ToLower(town), strings.ToLower(city)+"-") {
				c.Result, c.Detail = listing.Fail, m[1]+" "+town+" is not "+city
			}
			return c
		}
	}
	return c
}

// ---- Rent ------------------------------------------------------------------

func checkRent(l *listing.Listing, lim config.RentLimit) listing.Check {
	kind := "unfurnished"
	if l.Furnished {
		kind = "furnished"
	}
	if l.RentLowerBound && l.WarmRent > 0 {
		c := listing.Check{Name: listing.CritRent}
		if l.WarmRent > lim.ReviewUpTo {
			c.Result = listing.Fail
			c.Detail = fmt.Sprintf("Kaltmiete alone is %g € > %g € %s limit", l.WarmRent, lim.ReviewUpTo, kind)
		} else {
			c.Detail = fmt.Sprintf("only Kaltmiete known (%g €); warm rent not stated", l.WarmRent)
		}
		return c
	}
	c := numCheck(listing.CritRent, l.WarmRent, func(v float64) bool { return v <= lim.Max }, " €", l.RentNote)
	if c.Result == listing.Fail && l.WarmRent <= lim.ReviewUpTo {
		c.Result = listing.Unknown
		c.Detail = fmt.Sprintf("%g € is over the %g € %s limit, within the %g € stretch", l.WarmRent, lim.Max, kind, lim.ReviewUpTo)
	} else if c.Result == listing.Fail {
		c.Detail += fmt.Sprintf(" > %g € %s limit", lim.ReviewUpTo, kind)
	}
	return c
}

// ---- Move-in ---------------------------------------------------------------

func checkMoveIn(l *listing.Listing, from, to, now time.Time) listing.Check {
	c := listing.Check{Name: listing.CritMoveIn}
	window := from.Format("02.01.2006") + "–" + to.Format("02.01.2006")
	av := strings.TrimSpace(l.AvailableFrom)
	var date time.Time
	switch lav := strings.ToLower(av); {
	case av == "":
		c.Detail = "move-in date not stated"
		return c
	case strings.Contains(lav, "vereinbarung"), strings.Contains(lav, "absprache"):
		c.Detail = "available by arrangement – ask about " + window
		return c
	case strings.Contains(lav, "sofort"):
		date = now
	default:
		d, ok := ParseDate(av)
		if !ok {
			c.Detail = "unreadable date: " + av
			return c
		}
		date = d
	}
	switch {
	case date.After(to):
		c.Result, c.Detail = listing.Fail, "available from "+av+", after "+to.Format("02.01.2006")
	case !date.Before(from):
		c.Result, c.Detail = listing.Pass, "available from "+av
	case l.StrictStart:
		c.Result, c.Detail = listing.Fail, "available from "+av+", before "+from.Format("02.01.2006")
	default:
		c.Detail = "available from " + av + " (earlier) – ask if a start in " + window + " works"
	}
	return c
}

// ---- Evaluation ------------------------------------------------------------

// RulesVersion is part of the criteria fingerprint. Bump it only when a change
// to the evaluation code could turn a rejection into a pass: a bump makes the
// next run re-check every rejected listing that is still online (re-reading
// all result pages, and fetching any listing stored without Data). A change
// that only makes the rules stricter needs no bump.
const RulesVersion = 4

// Fingerprint identifies a set of criteria plus the rules applying them.
func Fingerprint(cr config.Criteria) string {
	b, _ := json.Marshal(cr)
	sum := sha256.Sum256(append(b, byte(RulesVersion)))
	return hex.EncodeToString(sum[:4])
}

type Evaluator struct {
	Criteria config.Criteria
	Transit  *TransitChecker // may be nil (transit then stays Unknown)
	Now      func() time.Time
}

func numCheck(name string, v float64, ok func(float64) bool, unit, note string) listing.Check {
	c := listing.Check{Name: name}
	if v == 0 {
		c.Detail = "not found"
		if note != "" {
			c.Detail = note
		}
		return c
	}
	c.Detail = strings.TrimSuffix(fmt.Sprintf("%g%s", v, unit), " ")
	if note != "" {
		c.Detail += " (" + note + ")"
	}
	if ok(v) {
		c.Result = listing.Pass
	} else {
		c.Result = listing.Fail
	}
	return c
}

// Evaluate fills l.Checks and l.Verdict. Transit lookups (network) are only
// done when no other criterion has already failed.
func (e *Evaluator) Evaluate(ctx context.Context, l *listing.Listing) {
	cr := e.Criteria
	now := time.Now()
	if e.Now != nil {
		now = e.Now()
	}
	text := l.Title + "\n" + l.Text
	var checks []listing.Check
	if lim := cr.RentLimit(l.Furnished); lim.Max > 0 {
		checks = append(checks, checkRent(l, lim))
	}
	if cr.MinRooms > 0 {
		checks = append(checks, numCheck(listing.CritRooms, l.Rooms, func(v float64) bool { return v >= cr.MinRooms }, "", ""))
	}
	if cr.MinSizeM2 > 0 {
		checks = append(checks, numCheck(listing.CritSize, l.SizeM2, func(v float64) bool { return v >= cr.MinSizeM2 }, " m²", ""))
	}
	if cr.RequireBalcony {
		checks = append(checks, checkBalcony(text))
	}
	if cr.RequireWasher {
		checks = append(checks, checkWasher(text, cr.WasherConnectionOK))
	}
	if cr.RequireKitchen {
		checks = append(checks, checkKitchen(text))
	}
	moveIn := cr.MoveInFrom != ""
	if moveIn {
		checks = append(checks, checkMoveIn(l, cr.MoveFrom, cr.MoveTo, now))
	}
	if cr.MinDurationMonths > 0 {
		minMonths := cr.MinDurationMonths
		if l.MaxLeaseMonths > 0 && l.MaxLeaseMonths < minMonths {
			minMonths = l.MaxLeaseMonths
		}
		// The lease has to run long enough from the day we move in, not from today.
		leaseStart := now
		if moveIn && cr.MoveFrom.After(now) {
			leaseStart = cr.MoveFrom
		}
		checks = append(checks, checkDuration(text, minMonths, leaseStart))
	}
	if cr.RequireAnmeldung {
		checks = append(checks, checkAnmeldung(text))
	}
	checks = append(checks, checkWBS(text))
	if cr.City != "" {
		checks = append(checks, checkCity(l, cr.City))
	}
	// Apply site-level knowledge where the listing text was silent.
	for i, c := range checks {
		if c.Result != listing.Unknown {
			continue
		}
		if d, ok := l.Defaults[c.Name]; ok {
			checks[i].Result = d.Result
			checks[i].Detail = d.Note
		}
	}
	failed := false
	for _, c := range checks {
		if c.Result == listing.Fail {
			failed = true
		}
	}
	if cr.MinTransitModes > 0 || cr.CommuteHbf {
		if failed || e.Transit == nil {
			checks = append(checks, listing.Check{Name: listing.CritTransit, Detail: "not checked"})
		} else {
			checks = append(checks, e.Transit.Check(ctx, l, cr))
		}
	}
	l.Checks = checks
	l.Verdict = verdictOf(checks)
}

// QuickReject reports whether a listing fails on the facts an index card
// shows (rent, rooms, size, move-in), so its detail page needn't be fetched.
// Unknown facts never reject.
func (e *Evaluator) QuickReject(l *listing.Listing) bool {
	cr := e.Criteria
	now := time.Now()
	if e.Now != nil {
		now = e.Now()
	}
	var checks []listing.Check
	if lim := cr.RentLimit(l.Furnished); lim.Max > 0 {
		checks = append(checks, checkRent(l, lim))
	}
	if cr.MinRooms > 0 {
		checks = append(checks, numCheck(listing.CritRooms, l.Rooms, func(v float64) bool { return v >= cr.MinRooms }, "", ""))
	}
	if cr.MinSizeM2 > 0 {
		checks = append(checks, numCheck(listing.CritSize, l.SizeM2, func(v float64) bool { return v >= cr.MinSizeM2 }, " m²", ""))
	}
	if cr.MoveInFrom != "" {
		checks = append(checks, checkMoveIn(l, cr.MoveFrom, cr.MoveTo, now))
	}
	for _, c := range checks {
		if c.Result == listing.Fail {
			return true
		}
	}
	return false
}

func verdictOf(checks []listing.Check) listing.Verdict {
	v := listing.Match
	for _, c := range checks {
		switch c.Result {
		case listing.Fail:
			return listing.Reject
		case listing.Unknown:
			v = listing.Review
		}
	}
	return v
}
