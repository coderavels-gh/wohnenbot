// Package config loads the bot's JSON configuration.
//
// ${VAR} / $VAR references anywhere in the file are expanded from the
// environment before parsing, so secrets (API keys, SMTP passwords) can live
// in env vars or CI secrets instead of the file.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type Config struct {
	IntervalMinutes int    `json:"interval_minutes"`
	RequestDelayMs  int    `json:"request_delay_ms"`
	ContactEmail    string `json:"contact_email"` // sent in the User-Agent to OpenStreetMap (their usage policy asks for it)
	StateFile       string `json:"state_file"`    // used only when Google Sheets is not configured
	// Local cache of OpenStreetMap lookups (stops near a point, geocoded
	// addresses), kept between runs. Not state: deleting it is harmless.
	TransitCacheFile string `json:"transit_cache_file"`

	Criteria Criteria      `json:"criteria"`
	Sources  Sources       `json:"sources"`
	Sheets   *SheetsConfig `json:"google_sheets"`
	Notify   NotifyConfig  `json:"notify"`
}

type Criteria struct {
	// Rent limit for listings not covered by warm_rent below.
	MaxWarmRent float64 `json:"max_warm_rent"`
	// Separate limits for furnished and unfurnished flats.
	WarmRent struct {
		Furnished   RentLimit `json:"furnished"`
		Unfurnished RentLimit `json:"unfurnished"`
	} `json:"warm_rent"`

	MinRooms          float64 `json:"min_rooms"`
	MinSizeM2         float64 `json:"min_size_m2"`
	MinDurationMonths int     `json:"min_duration_months"`

	// Move-in window, "YYYY-MM-DD". A listing must be available to move in
	// on some day between the two (inclusive). Empty = no move-in check.
	MoveInFrom string    `json:"move_in_from"`
	MoveInTo   string    `json:"move_in_to"`
	MoveFrom   time.Time `json:"-"`
	MoveTo     time.Time `json:"-"`

	RequireBalcony   bool `json:"require_balcony"`
	RequireWasher    bool `json:"require_washing_machine"`
	RequireKitchen   bool `json:"require_kitchen"`
	RequireAnmeldung bool `json:"require_anmeldung"`
	// For unfurnished flats a "Waschmaschinenanschluss" (hook-up, bring your
	// own machine) is normal. true = count it as a pass.
	WasherConnectionOK bool `json:"washing_machine_connection_ok"`

	MinTransitModes int `json:"min_transit_modes"` // distinct modes among Bus / U-Bahn / S-Bahn (when commute_hbf is off)
	// CommuteHbf: besides min_transit_modes different modes (bus, U-Bahn,
	// S-Bahn, ferry) near the flat, Hamburg Hbf (Nord) must be reachable
	// within an hour. Rule of thumb (no journey planner): a U/S-Bahn station
	// or ferry pier within rail_radius_m gets there (every Hamburg
	// rapid-transit station is ≤ ~40 min from Hbf).
	CommuteHbf bool `json:"commute_hbf_within_hour"`
	// City: the flat must be in this city ("Hamburg"); an address naming
	// another town is rejected. Empty = any.
	City        string `json:"city"`
	BusRadiusM  int    `json:"bus_radius_m"`
	RailRadiusM int    `json:"rail_radius_m"`
}

// RentLimit: up to Max passes; above Max but up to ReviewUpTo is REVIEW
// ("over budget, but worth a look"); above that is rejected.
type RentLimit struct {
	Max        float64 `json:"max"`
	ReviewUpTo float64 `json:"review_up_to"`
}

// RentLimit returns the limit for a furnished or unfurnished flat.
func (cr Criteria) RentLimit(furnished bool) RentLimit {
	r := cr.WarmRent.Unfurnished
	if furnished {
		r = cr.WarmRent.Furnished
	}
	if r.Max == 0 {
		r = RentLimit{Max: cr.MaxWarmRent}
	}
	if r.ReviewUpTo < r.Max {
		r.ReviewUpTo = r.Max
	}
	return r
}

type Sources struct {
	HomeCompany   HomeCompanyConfig   `json:"homecompany"`
	CityWohnen    CityWohnenConfig    `json:"citywohnen"`
	ImmoScout     ImmoScoutConfig     `json:"immoscout"`
	Immowelt      ImmoweltConfig      `json:"immowelt"`
	Kleinanzeigen KleinanzeigenConfig `json:"kleinanzeigen"`
}

type ImmoweltConfig struct {
	Enabled bool `json:"enabled"`
	// classified-search URLs copied from the website (filters, area); the bot
	// adds order=DateDesc and the page number.
	SearchURLs []string `json:"search_urls"`
	// Result pages read per run at most (more on a first run / criteria change).
	MaxPages int `json:"max_pages"`
}

type KleinanzeigenConfig struct {
	Enabled bool `json:"enabled"`
	// /s-wohnung-mieten/… search URLs copied from the website (area, size and
	// room filters; robots.txt disallows price filters); the bot adds the page.
	SearchURLs []string `json:"search_urls"`
	// Result pages read per run at most (more on a first run / criteria change).
	MaxPages int `json:"max_pages"`
	// With -pace (the Mac): check at most this often (default 120 minutes);
	// the other sources keep their own pace.
	EveryMinutes int `json:"every_minutes"`
}

type HomeCompanyConfig struct {
	Enabled  bool   `json:"enabled"`
	ListURL  string `json:"list_url"`
	MaxPages int    `json:"max_pages"`
}

type CityWohnenConfig struct {
	Enabled  bool     `json:"enabled"`
	ListURLs []string `json:"list_urls"`
}

type ImmoScoutConfig struct {
	Enabled bool `json:"enabled"`
	// Search result URLs copied from the ImmoScout website, e.g.
	// https://www.immobilienscout24.de/Suche/de/hamburg/hamburg/wohnung-mieten?price=-1600.0&...
	SearchURLs []string `json:"search_urls"`
	// Optional: fully-formed mobile-API URLs, used as-is (escape hatch if the
	// automatic conversion of search_urls stops working).
	APIURLs     []string `json:"api_urls"`
	FetchExpose bool     `json:"fetch_expose_details"`
}

type SheetsConfig struct {
	SpreadsheetID   string `json:"spreadsheet_id"`
	CredentialsFile string `json:"credentials_file"` // Google service-account JSON key
	ListingsTab     string `json:"listings_tab"`
	SeenTab         string `json:"seen_tab"`
}

type NotifyConfig struct {
	AlertOnReview bool                `json:"alert_on_review"` // also alert for listings with unknown criteria
	MaxPerRun     int                 `json:"max_per_run"`
	WhatsApp      []WhatsAppRecipient `json:"whatsapp"`
	Ntfy          *NtfyConfig         `json:"ntfy"`
	Email         *EmailConfig        `json:"email"`
}

type WhatsAppRecipient struct {
	Name   string `json:"name"`
	Phone  string `json:"phone"`  // international format, e.g. +491701234567
	APIKey string `json:"apikey"` // from CallMeBot's activation reply
}

type NtfyConfig struct {
	Server string `json:"server"` // default https://ntfy.sh
	Topic  string `json:"topic"`
	Always bool   `json:"always"` // send even when WhatsApp worked
}

type EmailConfig struct {
	SMTPHost string   `json:"smtp_host"`
	SMTPPort int      `json:"smtp_port"`
	Username string   `json:"username"`
	Password string   `json:"password"`
	From     string   `json:"from"`
	To       []string `json:"to"`
	Always   bool     `json:"always"`
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	expanded := os.ExpandEnv(string(raw))
	c := &Config{}
	if err := json.Unmarshal([]byte(expanded), c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	c.applyDefaults()
	if err := c.Criteria.parseMoveIn(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

func (c *Config) applyDefaults() {
	if c.IntervalMinutes <= 0 {
		c.IntervalMinutes = 30
	}
	if c.RequestDelayMs <= 0 {
		c.RequestDelayMs = 1500
	}
	if c.StateFile == "" {
		c.StateFile = "data/seen.json"
	}
	if c.TransitCacheFile == "" {
		c.TransitCacheFile = "data/transit-cache.json"
	}
	cr := &c.Criteria
	if cr.MinTransitModes == 0 {
		cr.MinTransitModes = 2
	}
	if cr.BusRadiusM == 0 {
		cr.BusRadiusM = 400
	}
	if cr.RailRadiusM == 0 {
		cr.RailRadiusM = 900
	}
	hc := &c.Sources.HomeCompany
	if hc.ListURL == "" {
		hc.ListURL = "https://www.homecompany.de/agenturen/hamburg/wohnungen/"
	}
	if hc.MaxPages == 0 {
		hc.MaxPages = 25
	}
	iw := &c.Sources.Immowelt
	if len(iw.SearchURLs) == 0 {
		// Hamburg, apartments to rent, up to the highest rent worth a look,
		// ≥ 2 rooms, ≥ 50 m², no flat swaps or shared flats.
		price := max(cr.RentLimit(true).ReviewUpTo, cr.RentLimit(false).ReviewUpTo)
		if price == 0 {
			price = 1800
		}
		iw.SearchURLs = []string{fmt.Sprintf("https://www.immowelt.de/classified-search?distributionTypes=Rent&estateTypes=Apartment&locations=AD08DE1113&priceMax=%.0f&numberOfRoomsMin=2&spaceMin=50&projectTypes=Stock,New_Build", price)}
	}
	if iw.MaxPages == 0 {
		iw.MaxPages = 11
	}
	ka := &c.Sources.Kleinanzeigen
	if len(ka.SearchURLs) == 0 {
		// Hamburg, flats to rent, with the room and size minimums.
		ka.SearchURLs = []string{fmt.Sprintf("https://www.kleinanzeigen.de/s-wohnung-mieten/hamburg/c203l9409+wohnung_mieten.qm_d:%.2f%%2C+wohnung_mieten.zimmer_d:%.1f%%2C", cr.MinSizeM2, cr.MinRooms)}
	}
	if ka.MaxPages == 0 {
		ka.MaxPages = 4
	}
	if ka.EveryMinutes == 0 {
		ka.EveryMinutes = 120
	}
	if len(c.Sources.CityWohnen.ListURLs) == 0 {
		c.Sources.CityWohnen.ListURLs = []string{"https://www.city-wohnen.de/moeblierte-wohnung-mieten-hamburg/"}
	}
	if c.Sheets != nil {
		if c.Sheets.ListingsTab == "" {
			c.Sheets.ListingsTab = "Listings"
		}
		if c.Sheets.SeenTab == "" {
			c.Sheets.SeenTab = "Seen"
		}
	}
	if c.Notify.MaxPerRun == 0 {
		c.Notify.MaxPerRun = 8
	}
	if c.Notify.Ntfy != nil && c.Notify.Ntfy.Server == "" {
		c.Notify.Ntfy.Server = "https://ntfy.sh"
	}
}

func (cr *Criteria) parseMoveIn() error {
	if cr.MoveInFrom == "" && cr.MoveInTo == "" {
		return nil
	}
	if cr.MoveInFrom == "" || cr.MoveInTo == "" {
		return fmt.Errorf("criteria: set both move_in_from and move_in_to")
	}
	var err error
	if cr.MoveFrom, err = time.Parse("2006-01-02", cr.MoveInFrom); err != nil {
		return fmt.Errorf("criteria.move_in_from: %w", err)
	}
	if cr.MoveTo, err = time.Parse("2006-01-02", cr.MoveInTo); err != nil {
		return fmt.Errorf("criteria.move_in_to: %w", err)
	}
	if cr.MoveTo.Before(cr.MoveFrom) {
		return fmt.Errorf("criteria: move_in_to is before move_in_from")
	}
	return nil
}
