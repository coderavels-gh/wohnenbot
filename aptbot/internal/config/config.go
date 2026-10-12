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
)

type Config struct {
	IntervalMinutes int    `json:"interval_minutes"`
	RequestDelayMs  int    `json:"request_delay_ms"`
	ContactEmail    string `json:"contact_email"` // sent in the User-Agent to OpenStreetMap (their usage policy asks for it)
	StateFile       string `json:"state_file"`    // used only when Google Sheets is not configured

	Criteria Criteria      `json:"criteria"`
	Sources  Sources       `json:"sources"`
	Sheets   *SheetsConfig `json:"google_sheets"`
	Notify   NotifyConfig  `json:"notify"`
}

type Criteria struct {
	MaxWarmRent       float64 `json:"max_warm_rent"`
	MinRooms          float64 `json:"min_rooms"`
	MinSizeM2         float64 `json:"min_size_m2"`
	MinDurationMonths int     `json:"min_duration_months"`

	RequireBalcony   bool `json:"require_balcony"`
	RequireWasher    bool `json:"require_washing_machine"`
	RequireKitchen   bool `json:"require_kitchen"`
	RequireAnmeldung bool `json:"require_anmeldung"`
	// For unfurnished flats a "Waschmaschinenanschluss" (hook-up, bring your
	// own machine) is normal. true = count it as a pass.
	WasherConnectionOK bool `json:"washing_machine_connection_ok"`

	MinTransitModes int `json:"min_transit_modes"` // distinct modes among Bus / U-Bahn / S-Bahn
	BusRadiusM      int `json:"bus_radius_m"`
	RailRadiusM     int `json:"rail_radius_m"`
}

type Sources struct {
	HomeCompany HomeCompanyConfig `json:"homecompany"`
	CityWohnen  CityWohnenConfig  `json:"citywohnen"`
	ImmoScout   ImmoScoutConfig   `json:"immoscout"`
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
