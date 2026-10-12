// aptbot polls flat listing sites, filters listings against your criteria,
// writes candidates to a shared Google Sheet and alerts you on WhatsApp/push.
//
//	aptbot -config config.json           # run forever, every interval_minutes
//	aptbot -config config.json -once     # one pass (for launchd / cron / CI)
//	aptbot -config config.json -once -dry-run   # print results, write/alert nothing
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"aptbot/internal/analyze"
	"aptbot/internal/config"
	"aptbot/internal/listing"
	"aptbot/internal/notify"
	"aptbot/internal/sheets"
	"aptbot/internal/sources"
	"aptbot/internal/store"
	"aptbot/internal/web"
)

var listingsHeader = []string{
	"First seen", "Status", "Verdict", "Source", "Title", "Warm rent €", "m²", "Rooms",
	"Available", "Location", "Transit", "Balcony", "Washer", "Kitchen", "Duration",
	"Anmeldung", "Needs checking", "Link", "Notes", "Key",
}

var statusOptions = []string{"New", "Interested", "Contacted", "Viewing", "Applied", "Not interested", "Gone"}

type app struct {
	cfg     *config.Config
	sources []sources.Source
	eval    *analyze.Evaluator
	store   store.Store
	sheet   *sheets.Client
	notify  *notify.Dispatcher
	dryRun  bool
	noAlert bool
	loc     *time.Location
}

func main() {
	cfgPath := flag.String("config", "config.json", "path to config file")
	once := flag.Bool("once", false, "run a single pass and exit")
	dryRun := flag.Bool("dry-run", false, "don't write to the sheet/state or send alerts; print results")
	noAlert := flag.Bool("no-alert", false, "record listings but send no alerts (e.g. first run)")
	testAlert := flag.Bool("test-alert", false, "send a test alert through the configured channels and exit")
	only := flag.String("only", "", "comma-separated source names to run (homecompany,citywohnen,immoscout)")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		loc = time.Local
	}
	a := &app{cfg: cfg, dryRun: *dryRun, noAlert: *noAlert, loc: loc, notify: notify.NewDispatcher(cfg.Notify)}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *testAlert {
		err := a.notify.Send(ctx, []notify.Message{{
			Title: "aptbot test", Body: "🏠 aptbot test alert – if you can read this, alerts work.",
			URL: "https://www.immobilienscout24.de",
		}})
		if err != nil {
			log.Fatalf("test alert failed: %v", err)
		}
		log.Println("test alert sent")
		return
	}

	client := web.NewClient(time.Duration(cfg.RequestDelayMs) * time.Millisecond)
	want := func(name string) bool {
		return *only == "" || strings.Contains(strings.ToLower(*only), name)
	}
	if cfg.Sources.HomeCompany.Enabled && want("homecompany") {
		a.sources = append(a.sources, &sources.HomeCompany{Client: client, Cfg: cfg.Sources.HomeCompany})
	}
	if cfg.Sources.CityWohnen.Enabled && want("citywohnen") {
		a.sources = append(a.sources, &sources.CityWohnen{Client: client, Cfg: cfg.Sources.CityWohnen})
	}
	if cfg.Sources.ImmoScout.Enabled && want("immoscout") {
		a.sources = append(a.sources, &sources.ImmoScout{Client: client, Cfg: cfg.Sources.ImmoScout})
	}
	if len(a.sources) == 0 {
		log.Fatal("no sources enabled")
	}
	a.eval = &analyze.Evaluator{Criteria: cfg.Criteria, Transit: analyze.NewTransitChecker(client, cfg.ContactEmail)}

	if cfg.Sheets != nil && cfg.Sheets.SpreadsheetID != "" {
		a.sheet, err = sheets.New(cfg.Sheets.CredentialsFile, cfg.Sheets.SpreadsheetID)
		if err != nil {
			log.Fatalf("google sheets: %v", err)
		}
		a.store = &store.Sheet{Client: a.sheet, Tab: cfg.Sheets.SeenTab}
	} else {
		a.store = &store.File{Path: cfg.StateFile}
	}

	if *once {
		if err := a.run(ctx); err != nil {
			log.Fatal(err)
		}
		return
	}
	for {
		if err := a.run(ctx); err != nil {
			log.Printf("run failed: %v", err)
		}
		// ±3 min jitter so requests don't land on an exact schedule.
		wait := time.Duration(cfg.IntervalMinutes)*time.Minute + time.Duration(rand.Intn(360)-180)*time.Second
		log.Printf("next run in %s", wait.Round(time.Second))
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return
		}
	}
}

func (a *app) run(ctx context.Context) error {
	start := time.Now()
	if err := a.store.Load(ctx); err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	if a.sheet != nil && !a.dryRun {
		if err := a.sheet.EnsureTab(ctx, a.cfg.Sheets.ListingsTab, listingsHeader, 1, statusOptions); err != nil {
			return fmt.Errorf("prepare sheet: %w", err)
		}
	}
	// First ever run: the sites have dozens of existing listings. Record them
	// but don't fire dozens of WhatsApp messages.
	bootstrap := a.store.Count() == 0

	var all []*listing.Listing
	for _, src := range a.sources {
		ls, err := src.Fetch(ctx, a.store.Seen)
		if err != nil {
			log.Printf("[%s] FAILED: %v", src.Name(), err)
			continue
		}
		all = append(all, ls...)
	}
	now := time.Now().In(a.loc)
	for _, l := range all {
		l.FirstSeen = now
		a.eval.Evaluate(ctx, l)
	}
	sort.SliceStable(all, func(i, j int) bool { return rank(all[i].Verdict) < rank(all[j].Verdict) })

	counts := map[listing.Verdict]int{}
	var rows [][]any
	var recs []store.Record
	for _, l := range all {
		counts[l.Verdict]++
		if l.Verdict != listing.Reject {
			rows = append(rows, sheetRow(l))
		}
		recs = append(recs, store.Record{Key: l.Key(), Source: l.Source, Verdict: string(l.Verdict),
			URL: l.URL, FirstSeen: now.Format("2006-01-02 15:04"), Title: l.Title})
	}
	log.Printf("run: %d new listings → %d match, %d review, %d rejected (%s)",
		len(all), counts[listing.Match], counts[listing.Review], counts[listing.Reject], time.Since(start).Round(time.Second))

	if a.dryRun {
		for _, l := range all {
			printListing(l)
		}
		return nil
	}
	if a.sheet != nil {
		if err := a.sheet.Append(ctx, a.cfg.Sheets.ListingsTab, rows); err != nil {
			return fmt.Errorf("write tracker: %w", err) // state not saved → retried next run
		}
	}
	if err := a.store.Add(ctx, recs); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	if bootstrap || a.noAlert {
		if bootstrap {
			log.Printf("first run: recorded %d existing listings without alerting", len(all))
		}
		return nil
	}
	return a.alert(ctx, all)
}

func rank(v listing.Verdict) int {
	switch v {
	case listing.Match:
		return 0
	case listing.Review:
		return 1
	}
	return 2
}

func (a *app) alert(ctx context.Context, all []*listing.Listing) error {
	var pick []*listing.Listing
	for _, l := range all {
		if l.Verdict == listing.Match || (l.Verdict == listing.Review && a.cfg.Notify.AlertOnReview) {
			pick = append(pick, l)
		}
	}
	if len(pick) == 0 {
		return nil
	}
	var msgs []notify.Message
	max := a.cfg.Notify.MaxPerRun
	for i, l := range pick {
		if i == max {
			msgs = append(msgs, notify.Message{
				Title: fmt.Sprintf("+%d more flats", len(pick)-max),
				Body:  fmt.Sprintf("🏠 +%d more new flats – see the tracker sheet.", len(pick)-max),
			})
			break
		}
		msgs = append(msgs, alertMessage(l))
	}
	if err := a.notify.Send(ctx, msgs); err != nil {
		return fmt.Errorf("alerts: %w", err)
	}
	log.Printf("alerted %d listing(s)", len(pick))
	return nil
}

func fmtNum(v float64, unit string) string {
	if v == 0 {
		return "?"
	}
	s := fmt.Sprintf("%g", v)
	if v >= 1000 {
		s = fmt.Sprintf("%.0f", v)
		s = s[:len(s)-3] + "." + s[len(s)-3:]
	}
	return s + unit
}

func alertMessage(l *listing.Listing) notify.Message {
	head := "🏠 *MATCH*"
	if l.Verdict == listing.Review {
		head = "🔎 *Check this one*"
	}
	loc := l.District
	if loc == "" {
		loc = l.Address
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s · %s\n%s\n", head, l.Source, l.Title)
	fmt.Fprintf(&b, "💶 %s warm · %s · %s Zi\n", fmtNum(l.WarmRent, " €"), fmtNum(l.SizeM2, " m²"), fmtNum(l.Rooms, ""))
	if c, ok := l.Check(listing.CritTransit); ok {
		fmt.Fprintf(&b, "📍 %s · 🚇 %s\n", loc, shorten(c.Detail, 70))
	} else if loc != "" {
		fmt.Fprintf(&b, "📍 %s\n", loc)
	}
	if l.AvailableFrom != "" {
		fmt.Fprintf(&b, "📅 ab %s\n", l.AvailableFrom)
	}
	if u := unknowns(l); u != "" {
		fmt.Fprintf(&b, "⚠️ unconfirmed: %s\n", u)
	}
	return notify.Message{
		Title: fmt.Sprintf("%s %s warm, %s, %s", l.Source, fmtNum(l.WarmRent, "€"), fmtNum(l.SizeM2, "m2"), loc),
		Body:  strings.TrimSpace(b.String()),
		URL:   l.URL,
	}
}

func shorten(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func unknowns(l *listing.Listing) string {
	var u []string
	for _, c := range l.Checks {
		if c.Result == listing.Unknown {
			u = append(u, strings.ToLower(c.Name))
		}
	}
	return strings.Join(u, ", ")
}

func cell(l *listing.Listing, name string) string {
	c, ok := l.Check(name)
	if !ok {
		return ""
	}
	return c.Result.Symbol() + " " + c.Detail
}

func numCell(v float64) any {
	if v == 0 {
		return ""
	}
	return v
}

func sheetRow(l *listing.Listing) []any {
	loc := l.District
	if l.Address != "" && !strings.Contains(l.Address, loc) {
		loc = strings.TrimSpace(l.Address + " · " + l.District)
	} else if loc == "" {
		loc = l.Address
	}
	link := l.URL // plain URL: Sheets turns it into a link; avoids locale-dependent formula separators
	return []any{
		l.FirstSeen.Format("2006-01-02 15:04"), "New", string(l.Verdict), l.Source, l.Title,
		numCell(l.WarmRent), numCell(l.SizeM2), numCell(l.Rooms), l.AvailableFrom, loc,
		cell(l, listing.CritTransit), cell(l, listing.CritBalcony), cell(l, listing.CritWasher),
		cell(l, listing.CritKitchen), cell(l, listing.CritDuration), cell(l, listing.CritAnmeldung),
		unknowns(l), link, "", l.Key(),
	}
}

func printListing(l *listing.Listing) {
	fmt.Printf("\n[%s] %s — %s\n  %s\n  rent=%s (%s) size=%s rooms=%s avail=%s addr=%q district=%q precise=%v\n",
		l.Verdict, l.Source, l.Title, l.URL, fmtNum(l.WarmRent, "€"), l.RentNote, fmtNum(l.SizeM2, "m²"),
		fmtNum(l.Rooms, ""), l.AvailableFrom, l.Address, l.District, l.AddressPrecise)
	for _, c := range l.Checks {
		fmt.Printf("    %s %-10s %s\n", c.Result.Symbol(), c.Name, c.Detail)
	}
}
