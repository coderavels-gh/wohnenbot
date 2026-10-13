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

// listingsHeader is used only when the bot creates the Listings tab. After
// that, rows are written by column name (see orderRow), so the columns can be
// reordered, restyled, or extended with your own columns in the sheet.
var listingsHeader = []string{
	"Status", "Verdict", "Title", "Location", "Warm rent €", "m²", "Rooms",
	"Available", "Duration", "Transit", "Balcony", "Washer", "Kitchen", "Anmeldung",
	"Needs checking", "Notes", "Source", "First seen", "Key",
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

	criteria string // fingerprint stored with each Seen record
	transit  *analyze.TransitChecker
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
	// ImmoScout is the site most likely to block; give it a wider gap.
	client.HostDelay["api.mobile.immobilienscout24.de"] = 3 * time.Second
	// Overpass rations queries per IP; space them out (it answered 429 after
	// bursts of test runs).
	client.HostDelay["overpass-api.de"] = 5 * time.Second
	client.HostDelay["overpass.private.coffee"] = 5 * time.Second
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
	a.criteria = analyze.Fingerprint(cfg.Criteria)
	a.transit = analyze.NewTransitChecker(client, cfg.ContactEmail)
	a.transit.LoadCache(cfg.TransitCacheFile)
	a.eval = &analyze.Evaluator{Criteria: cfg.Criteria, Transit: a.transit}

	if cfg.Sheets != nil && cfg.Sheets.SpreadsheetID != "" {
		a.sheet, err = sheets.New(cfg.Sheets.CredentialsFile, cfg.Sheets.SpreadsheetID)
		if err != nil {
			log.Fatalf("google sheets: %v", err)
		}
		a.store = &store.Sheet{Client: a.sheet, Tab: cfg.Sheets.SeenTab, Criteria: a.criteria}
	} else {
		a.store = &store.File{Path: cfg.StateFile, Criteria: a.criteria}
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

// flushEvery: listings are written to the sheet, recorded and alerted in
// batches during the scan, so a long run delivers results as it goes and a
// crash loses at most one batch (which is simply re-evaluated next run).
const flushEvery = 10

// runState is the bookkeeping of one run.
type runState struct {
	now      time.Time
	header   []string        // Listings header, for writing by column name
	baseline map[string]bool // source → first run (record, don't alert)
	pending  []*listing.Listing
	counts   map[listing.Verdict]int
	alerted  int // alerts sent this run (capped at max_per_run)
	overflow int // alertable listings beyond the cap
	err      error
}

func (a *app) run(ctx context.Context) error {
	start := time.Now()
	if err := a.store.Load(ctx); err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	rs := &runState{now: time.Now().In(a.loc), baseline: map[string]bool{}, counts: map[listing.Verdict]int{}}
	if a.sheet != nil && !a.dryRun {
		if err := a.sheet.EnsureTab(ctx, a.cfg.Sheets.ListingsTab, listingsHeader, 0, statusOptions); err != nil {
			return fmt.Errorf("prepare sheet: %w", err)
		}
		h, err := a.sheet.Header(ctx, a.cfg.Sheets.ListingsTab)
		if err != nil {
			return fmt.Errorf("read tracker header: %w", err)
		}
		rs.header = h
		checkHeader(h)
	}
	for _, src := range a.sources {
		// A source's first run: the site has dozens of existing listings.
		// Record them (candidates still go to the sheet) as a baseline,
		// without alerting. Sources that can, read all their result pages.
		first := !a.store.HasSource(src.Name())
		rs.baseline[src.Name()] = first
		if fs, ok := src.(interface{ SetFullScan(bool) }); ok {
			// Also read everything after a criteria change, so rejected
			// listings that are still online get re-evaluated.
			recheck := !first && a.store.NeedsRecheck(src.Name())
			if recheck {
				log.Printf("[%s] criteria changed: reading all result pages to re-check rejected listings", src.Name())
			}
			fs.SetFullScan(first || recheck)
		}
		revived, err := a.fetch(ctx, src, func(l *listing.Listing) { a.handle(ctx, rs, l) })
		if err != nil {
			log.Printf("[%s] FAILED: %v", src.Name(), err)
		}
		if revived > 0 {
			log.Printf("[%s] re-checked %d rejected listing(s) from stored data, without fetching them", src.Name(), revived)
		}
		a.flush(ctx, rs)
		if first {
			log.Printf("[%s] first run: recorded its current listings as the baseline, no alerts", src.Name())
		}
	}
	if rs.overflow > 0 && rs.err == nil && !a.noAlert && !a.dryRun {
		msg := notify.Message{
			Title: fmt.Sprintf("+%d more flats", rs.overflow),
			Body:  fmt.Sprintf("🏠 +%d more new flats – see the tracker sheet.", rs.overflow),
		}
		if err := a.notify.Send(ctx, []notify.Message{msg}); err != nil {
			log.Printf("alerts: %v", err)
		}
	}
	if a.transit != nil {
		if err := a.transit.SaveCache(a.cfg.TransitCacheFile); err != nil {
			log.Printf("transit: saving cache: %v", err)
		}
	}
	n := rs.counts[listing.Match] + rs.counts[listing.Review] + rs.counts[listing.Reject]
	log.Printf("run: %d listings evaluated (new, or rejected under older criteria) → %d match, %d review, %d rejected (%s)",
		n, rs.counts[listing.Match], rs.counts[listing.Review], rs.counts[listing.Reject], time.Since(start).Round(time.Second))
	return rs.err
}

// handle evaluates one listing and queues it for the next flush.
func (a *app) handle(ctx context.Context, rs *runState, l *listing.Listing) {
	l.FirstSeen = rs.now
	a.eval.Evaluate(ctx, l)
	rs.counts[l.Verdict]++
	if a.dryRun {
		printListing(l)
		return
	}
	rs.pending = append(rs.pending, l)
	if len(rs.pending) >= flushEvery {
		a.flush(ctx, rs)
	}
}

// flush writes the pending listings: candidates to the tracker, every
// listing to Seen, then alerts. After a failed write nothing more is written
// this run; unwritten listings aren't recorded, so the next run retries them.
func (a *app) flush(ctx context.Context, rs *runState) {
	if rs.err != nil || len(rs.pending) == 0 {
		return
	}
	batch := rs.pending
	rs.pending = nil
	sort.SliceStable(batch, func(i, j int) bool { return rank(batch[i].Verdict) < rank(batch[j].Verdict) })

	var rows [][]any
	var links []sheets.Link // Title cells, linked to the listing
	var recs []store.Record
	for _, l := range batch {
		if l.Verdict != listing.Reject {
			rows = append(rows, orderRow(rs.header, sheetRow(l)))
			links = append(links, sheets.Link{Text: l.DisplayTitle(), URL: l.URL})
		}
		recs = append(recs, store.Record{Key: l.Key(), Source: l.Source, Verdict: string(l.Verdict),
			URL: l.URL, FirstSeen: rs.now.Format("2006-01-02 15:04"), Title: l.Title, Criteria: a.criteria,
			Cells: seenCells(l), Data: l.Encode()})
	}
	if a.sheet != nil && len(rows) > 0 {
		first, err := a.sheet.AppendAt(ctx, a.cfg.Sheets.ListingsTab, rows)
		if err != nil {
			rs.err = fmt.Errorf("write tracker: %w", err)
			return
		}
		if col := columnIndex(rs.header, "Title"); first > 0 && col >= 0 {
			if err := a.sheet.SetLinks(ctx, a.cfg.Sheets.ListingsTab, col, first, links); err != nil {
				log.Printf("tracker: linking titles: %v", err) // rows are written; only the links are missing
			}
		}
	}
	if err := a.store.Add(ctx, recs); err != nil {
		rs.err = fmt.Errorf("save state: %w", err)
		return
	}
	if a.noAlert {
		return
	}
	var msgs []notify.Message
	for _, l := range batch {
		if rs.baseline[l.Source] || !(l.Verdict == listing.Match || (l.Verdict == listing.Review && a.cfg.Notify.AlertOnReview)) {
			continue
		}
		if rs.alerted >= a.cfg.Notify.MaxPerRun {
			rs.overflow++
			continue
		}
		msgs = append(msgs, alertMessage(l))
		rs.alerted++
	}
	if len(msgs) == 0 {
		return
	}
	if err := a.notify.Send(ctx, msgs); err != nil {
		log.Printf("alerts: %v", err) // listings are recorded; a failed alert isn't retried
		return
	}
	log.Printf("alerted %d listing(s)", len(msgs))
}

// fetch runs one source, passing each listing to emit as soon as it is
// fetched. A listing rejected under older criteria that is still online and
// has stored data is re-evaluated from that data instead of being fetched
// again ("revived"); the source only asks about listings it currently sees,
// so gone listings are never re-checked.
func (a *app) fetch(ctx context.Context, src sources.Source, emit func(*listing.Listing)) (revived int, err error) {
	seen := func(key string) bool {
		if a.store.Seen(key) {
			return true
		}
		if !a.store.Known(key) {
			return false // new listing
		}
		l, derr := listing.Decode(a.store.Data(ctx, key))
		if derr != nil {
			return false // no usable stored data: fetch it again
		}
		revived++
		emit(l)
		return true
	}
	err = src.Fetch(ctx, seen, emit)
	return revived, err
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
	fmt.Fprintf(&b, "%s · %s\n%s\n", head, l.Source, l.DisplayTitle())
	if l.PlusOnly {
		b.WriteString("⭐ Plus-only: contact via your Plus account – fewer competitors\n")
	}
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
		Title: fmt.Sprintf("%s%s %s warm, %s, %s", plusPrefix(l), l.Source, fmtNum(l.WarmRent, "€"), fmtNum(l.SizeM2, "m2"), loc),
		Body:  strings.TrimSpace(b.String()),
		URL:   l.URL,
	}
}

// plusPrefix marks Plus-only listings in the push title (ASCII: ntfy headers).
func plusPrefix(l *listing.Listing) string {
	if l.PlusOnly {
		return "[Plus] "
	}
	return ""
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

func availableCell(l *listing.Listing) string {
	if c := cell(l, listing.CritMoveIn); c != "" {
		return c
	}
	return l.AvailableFrom
}

func numCell(v float64) any {
	if v == 0 {
		return nil
	}
	return v
}

// sheetRow returns the bot's values keyed by Listings column name.
func sheetRow(l *listing.Listing) map[string]any {
	loc := l.District
	if l.Address != "" && !strings.Contains(l.Address, loc) {
		loc = strings.TrimSpace(l.Address + " · " + l.District)
	} else if loc == "" {
		loc = l.Address
	}
	return map[string]any{
		"Status": "New", "Verdict": string(l.Verdict), "Title": l.DisplayTitle(), "Location": loc,
		"Warm rent €": numCell(l.WarmRent), "m²": numCell(l.SizeM2), "Rooms": numCell(l.Rooms),
		"Available": availableCell(l), "Duration": cell(l, listing.CritDuration),
		"Transit": cell(l, listing.CritTransit), "Balcony": cell(l, listing.CritBalcony),
		"Washer": cell(l, listing.CritWasher), "Kitchen": cell(l, listing.CritKitchen),
		"Anmeldung": cell(l, listing.CritAnmeldung), "Needs checking": unknowns(l),
		"Source": l.Source, "First seen": l.FirstSeen.Format("2006-01-02 15:04"), "Key": l.Key(),
	}
}

// seenCells: the readable columns of the Seen tab, the same values as in
// Listings (minus the user's Status/Notes).
func seenCells(l *listing.Listing) map[string]any {
	c := sheetRow(l)
	delete(c, "Status")
	delete(c, "Title") // Seen has its own plain Title
	return c
}

// orderRow lays values out under the sheet's own header. Columns the bot
// doesn't fill (Notes, formula columns like €/m², your own additions) get
// nil, which the Sheets API leaves untouched.
func orderRow(header []string, vals map[string]any) []any {
	if header == nil {
		header = listingsHeader
	}
	row := make([]any, len(header))
	for i, h := range header {
		if v, ok := vals[strings.TrimSpace(h)]; ok && v != "" {
			row[i] = v
		}
	}
	return row
}

func columnIndex(header []string, name string) int {
	if header == nil {
		header = listingsHeader
	}
	for i, h := range header {
		if strings.TrimSpace(h) == name {
			return i
		}
	}
	return -1
}

// checkHeader logs bot columns missing from the sheet (renamed or deleted),
// whose values would otherwise be dropped silently.
func checkHeader(header []string) {
	have := map[string]bool{}
	for _, h := range header {
		have[strings.TrimSpace(h)] = true
	}
	for _, h := range listingsHeader {
		if h != "Notes" && !have[h] {
			log.Printf("tracker: no %q column in the Listings header; its values are not written", h)
		}
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
