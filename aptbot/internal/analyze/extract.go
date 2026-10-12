// Package analyze extracts facts from German listing text and evaluates them
// against the search criteria.
package analyze

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

var reThousands = regexp.MustCompile(`^\d{1,3}(\.\d{3})+$`)

// ParseGermanNumber handles "1.550", "45,19", "1.550,50", "2.5", "2".
func ParseGermanNumber(s string) (float64, bool) {
	s = strings.TrimSpace(strings.Trim(s, ".,"))
	if s == "" {
		return 0, false
	}
	switch {
	case strings.Contains(s, ".") && strings.Contains(s, ","):
		s = strings.ReplaceAll(s, ".", "")
		s = strings.Replace(s, ",", ".", 1)
	case strings.Contains(s, ","):
		s = strings.Replace(s, ",", ".", 1)
	case reThousands.MatchString(s):
		s = strings.ReplaceAll(s, ".", "")
	}
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil
}

const num = `([0-9][0-9.,]*)`

// labeledNumber finds "<label> [:] [ca.] [€] <number> <unit>".
func labeledNumber(text, labels, unit string) (float64, bool) {
	re := regexp.MustCompile(`(?i)(?:^|[^\p{L}])(?:` + labels + `)(?:\s*(?:ca\.?|\([^)]{0,25}\)|[:\-–]))*\s*(?:€\s*|EUR\s*)?` + num + `\s*` + unit)
	for _, m := range re.FindAllStringSubmatch(text, -1) {
		if v, ok := ParseGermanNumber(m[1]); ok && v > 0 {
			return v, true
		}
	}
	return 0, false
}

const moneyUnit = `(?:€|EUR|Euro)?`

var (
	warmLabels = `Warmmiete|Gesamtmiete|Pauschalmiete|Inklusivmiete|Bruttowarmmiete|Gesamtkosten|Miete inkl\.? (?:NK|Nebenkosten)|Total rent|Warm rent`
	coldLabels = `Kaltmiete|Nettokaltmiete|Grundmiete`
	nkLabels   = `Nebenkosten|Betriebskosten`
	heatLabels = `Heizkosten`
)

// RentResult is the warm rent and how it was derived.
type RentResult struct {
	Warm float64
	Note string
}

// FindWarmRent looks for an explicit warm/all-inclusive rent; otherwise sums
// Kaltmiete + Nebenkosten (+ Heizkosten). extraWarmLabels lets a source add
// labels that mean "inclusive rent" on that site (e.g. City-Wohnen's "Miete").
func FindWarmRent(text string, extraWarmLabels string) RentResult {
	labels := warmLabels
	if extraWarmLabels != "" {
		labels = labels + "|" + extraWarmLabels
	}
	if v, ok := labeledNumber(text, labels, moneyUnit); ok && v >= 100 && v < 20000 {
		return RentResult{Warm: v}
	}
	cold, okC := labeledNumber(text, coldLabels, moneyUnit)
	if !okC || cold < 100 {
		return RentResult{}
	}
	nk, okN := labeledNumber(text, nkLabels, moneyUnit)
	heat, okH := labeledNumber(text, heatLabels, moneyUnit)
	if !okN {
		return RentResult{Warm: 0, Note: "only Kaltmiete stated"}
	}
	total := cold + nk
	note := "Kalt + NK"
	if okH && heat < cold && !regexp.MustCompile(`(?i)Heizkosten\s*(?:sind\s*)?in\s*(?:den\s*)?Nebenkosten\s*enthalten`).MatchString(text) {
		total += heat
		note = "Kalt + NK + Heizkosten"
	}
	return RentResult{Warm: total, Note: note}
}

var (
	reAreaAny  = regexp.MustCompile(`(?i)` + num + `\s*(?:m²|m2|qm)\b`)
	reRoomsPre = regexp.MustCompile(`(?i)` + num + `\s*(?:-\s*)?(?:Zimmer|Zi\.|Zi\b|Raum|Räume)`)
)

func FindArea(text string) float64 {
	if v, ok := labeledNumber(text, `Wohnfläche|Wohnflaeche|Größe|Groesse|Fläche|Living space|Size`, `(?:m²|m2|qm)`); ok && v >= 10 && v < 1000 {
		return v
	}
	for _, m := range reAreaAny.FindAllStringSubmatch(text, -1) {
		if v, ok := ParseGermanNumber(m[1]); ok && v >= 15 && v < 1000 {
			return v
		}
	}
	return 0
}

func FindRooms(text string) float64 {
	if v, ok := labeledNumber(text, `Zimmeranzahl|Anzahl Zimmer|Zimmer|Rooms`, ``); ok && v >= 1 && v <= 12 {
		return v
	}
	for _, m := range reRoomsPre.FindAllStringSubmatch(text, -1) {
		if v, ok := ParseGermanNumber(m[1]); ok && v >= 1 && v <= 12 {
			return v
		}
	}
	return 0
}

var reDate = regexp.MustCompile(`(\d{1,2})\.(\d{1,2})\.(\d{4})`)

func ParseDate(s string) (time.Time, bool) {
	m := reDate.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, false
	}
	d, _ := strconv.Atoi(m[1])
	mo, _ := strconv.Atoi(m[2])
	y, _ := strconv.Atoi(m[3])
	if mo < 1 || mo > 12 || d < 1 || d > 31 {
		return time.Time{}, false
	}
	return time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC), true
}

// MonthsBetween returns whole-ish months (days/30.44) between a and b.
func MonthsBetween(a, b time.Time) float64 {
	return b.Sub(a).Hours() / 24 / 30.44
}

// Snippet returns text around [start,end) for evidence, on one line.
func Snippet(text string, start, end int) string {
	from := start - 35
	if from < 0 {
		from = 0
	}
	to := end + 35
	if to > len(text) {
		to = len(text)
	}
	// keep valid UTF-8 boundaries
	for from > 0 && !utf8Start(text[from]) {
		from--
	}
	for to < len(text) && !utf8Start(text[to]) {
		to++
	}
	s := strings.Join(strings.Fields(text[from:to]), " ")
	if from > 0 {
		s = "…" + s
	}
	if to < len(text) {
		s += "…"
	}
	return s
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

var reAvailable = regexp.MustCompile(`(?i)(?:bezugsfrei\s+ab|bezugsfertig\s+ab|frei\s+ab|verfügbar\s+ab|einzug\s+ab|einzugstermin|bezugstermin|available\s+from)\s*:?\s*(?:dem\s+|zum\s+)?(\d{1,2}\.\d{1,2}\.\d{4}|sofort|nach\s+vereinbarung)`)

// FindAvailableFrom returns the move-in date as written ("01.12.2026",
// "sofort", "nach Vereinbarung"), or "" if the text doesn't say.
func FindAvailableFrom(text string) string {
	if m := reAvailable.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	return ""
}
