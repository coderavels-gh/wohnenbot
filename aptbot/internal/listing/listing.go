// Package listing holds the source-independent listing model.
package listing

import "time"

// Result of one criterion check.
type Result int

const (
	Unknown Result = iota
	Pass
	Fail
)

func (r Result) Symbol() string {
	switch r {
	case Pass:
		return "✓"
	case Fail:
		return "✗"
	}
	return "?"
}

type Verdict string

const (
	Match  Verdict = "MATCH"  // every criterion confirmed
	Review Verdict = "REVIEW" // nothing failed, some criteria couldn't be confirmed
	Reject Verdict = "REJECT" // at least one criterion failed
)

// Criterion names, used as keys for checks and source defaults.
const (
	CritRent      = "Warm rent"
	CritRooms     = "Rooms"
	CritSize      = "Size"
	CritBalcony   = "Balcony"
	CritWasher    = "Washer"
	CritKitchen   = "Kitchen"
	CritDuration  = "Duration"
	CritAnmeldung = "Anmeldung"
	CritTransit   = "Transit"
	CritMoveIn    = "Move-in"
)

type Check struct {
	Name   string
	Result Result
	Detail string // evidence or reason, shown in the sheet
}

type Default struct {
	Result Result
	Note   string
}

type Listing struct {
	Source string
	ID     string
	URL    string
	Title  string

	WarmRent float64 // 0 = unknown
	RentNote string
	SizeM2   float64
	Rooms    float64

	Address        string // best string for geocoding
	AddressPrecise bool   // includes a street, not just postcode/district
	District       string
	Lat, Lon       float64 // if the source provides coordinates

	AvailableFrom string // "01.12.2026", "sofort", "nach Vereinbarung" or ""
	// FlexibleStart: AvailableFrom opens a rental window and any later start
	// date inside it works (City-Wohnen's "ab 12.10.2026 bis 31.12.2027").
	FlexibleStart bool
	Furnished     bool   // selects the furnished or unfurnished rent limit
	Text          string // main detail text used for keyword analysis
	TransitText   string // dedicated "Lage"/transport text, if the source has one

	// What we know about a criterion from the site itself when the listing
	// text is silent (e.g. City-Wohnen guarantees Anmeldung for every flat).
	Defaults map[string]Default
	// Longest lease the site offers at all (0 = no limit). The duration
	// criterion is capped at this, so a site that rents for at most 12 months
	// isn't rejected by a longer minimum.
	MaxLeaseMonths int

	Checks    []Check
	Verdict   Verdict
	FirstSeen time.Time
}

func (l *Listing) Key() string { return l.Source + ":" + l.ID }

func (l *Listing) SetDefault(crit string, r Result, note string) {
	if l.Defaults == nil {
		l.Defaults = map[string]Default{}
	}
	l.Defaults[crit] = Default{Result: r, Note: note}
}

func (l *Listing) Check(name string) (Check, bool) {
	for _, c := range l.Checks {
		if c.Name == name {
			return c, true
		}
	}
	return Check{}, false
}
