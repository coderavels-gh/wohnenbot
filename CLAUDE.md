# wohnenbot – notes for working on this repo

The bot is a Go program in `aptbot/` (stdlib only). It polls City-Wohnen,
ImmoScout24, HomeCompany, immowelt and Kleinanzeigen for Hamburg flats, evaluates them against
criteria, writes candidates to a shared Google Sheet and sends ntfy push
alerts. See `aptbot/README.md` for how it works.

## Where it runs

- **GitHub Actions, every 15 minutes** (`.github/workflows/aptbot.yml`),
  started only by an external job on **cron-job.org** (the user's account)
  that POSTs to the `workflow_dispatch` API with a fine-grained token (this
  repo only, Actions read/write). There is no GitHub schedule (it dropped most
  runs). **The token expires 2027-01-12**; after that, cron-job.org reports
  failed executions and the user must create a new token and paste it into
  the job. HomeCompany (16 index pages per check) only runs in the first
  quarter of each hour; the workflow skips it otherwise.
- **The repo is public** (unlimited free Actions minutes). So:
  - Never commit personal data: no names, emails, phone numbers, sheet IDs,
    ntfy topics, tokens. They live in `config.json` / repo secrets.
  - Commit as `coderavels-gh <260004071+coderavels-gh@users.noreply.github.com>`
    (set in this repo's local git config), never a personal or work email.
  - Run logs are public: the workflow writes the bot's output to a file and
    prints only a redacted summary. Keep it that way.
  - Saved site pages (`aptbot/internal/sources/testdata/`) are gitignored:
    third-party content with landlords' names. Tests skip without them.
  The run reads its config from repo secrets, not from the checkout:
  - `APTBOT_CONFIG`: the whole `aptbot/config.json`
  - `GOOGLE_SA_JSON`: `aptbot/secrets/service-account.json`
- `aptbot/config.json` and `aptbot/secrets/` are gitignored. **After any change
  to `config.json` (criteria, sources, notify), update the secret**, or the
  cloud keeps running the old config:
  `python3 -c "import json;print(json.dumps(json.load(open('aptbot/config.json')),ensure_ascii=False,separators=(',',':')))" | gh secret set APTBOT_CONFIG -R coderavels-gh/wohnenbot`
  Store secrets as **single-line** JSON: GitHub masks every line of a
  multi-line secret in the logs, so a pretty-printed config turns every `{`
  and `}` in error messages into `***`.
- A manual run with "Only send a test notification" checks ntfy from the cloud.
- State (which listings were seen, with their extracted data) lives in the
  Google Sheet's `Seen` tab, so any runner (cloud, Mac, Docker) shares it.

## The Google Sheet

- The user styles the `Listings` tab with their own Apps Script (column order,
  colours, banding, filters; `€/m²` is an ARRAYFORMULA in its header cell).
  **Never send formatting requests to it.** The bot writes by header name
  (`orderRow`), sends `nil` for columns it doesn't own, and appends with
  `OVERWRITE` into the pre-formatted rows. The Title cell carries the link;
  there is no Link column.
- After adding rows the bot sorts `Listings` (whole rows) by `First seen`,
  newest first, then MATCH before REVIEW (`sortTracker`), so new finds are on
  top. That is a data sort, not a formatting change; the user's Status and
  Notes move with their rows.
- A new bot column means the user adds it to `ORDER`/`WIDTHS` in their script:
  tell them.
- `Seen` is the bot's memory: header-driven too, one row per evaluation (last
  row per key wins), with a compressed `Data` column (`listing.Encode`).

## Rules for changing evaluation code

- `analyze.RulesVersion` is part of the criteria fingerprint. Bump it **only**
  when a change could turn a rejection into a pass: a bump makes the next run
  re-check every rejected listing still online (and fetch those stored without
  `Data`). Stricter rules need no bump.
- Adding a field to `config.Criteria` changes the fingerprint too (same effect).
  A rule that only rejects can be built in instead (like the WBS rule).
- `listing.DataVersion`: bump when a source parser changes what it extracts,
  so stored `Data` is not reused.

## Being polite to the sites

- One request at a time per host: 1.5 s default, 3 s ImmoScout, 5 s Overpass.
- 403/429 → that host is left alone for the rest of the run; `Retry-After`
  is honoured. Don't add retries that hammer.
- Normal runs fetch only unseen listings. ImmoScout reads its newest page;
  full scans only on a source's first run or after a criteria change.
  City-Wohnen sorts by move-in date, so it reads all pages (3 requests) every run.
- **immowelt runs on the user's Mac, not in the cloud**: it answers GitHub's
  datacenter IPs with HTTP 403 (bot protection). The workflow passes
  `-skip immowelt`; the Mac LaunchAgent (`deploy/install-mac.sh`, default
  `ONLY=immowelt`) runs `-once -only immowelt -pace 15m-30m` while the Mac is awake: launchd
  starts it every minute, and it only runs once the random 15–30 minute gap
  picked by the previous run has passed, kept in `data/pace.json`. A host
  that answers 403/429 is left alone for 2 hours (per host: the other sites
  keep running). Both write to the same sheet.
- **Kleinanzeigen runs in the cloud, every 2 hours**: the workflow skips it
  except in the first quarter of even UTC hours (manual input
  `kleinanzeigen_now` forces it); the user doesn't need it fast. **Never run
  it from the user's Mac/home IP**: on Oct 27 a burst from there (tests plus a
  10-page first scan) got their IP range blocked while they browsed the site.
  If the cloud gets 403, ask the user (turn it off) rather than moving it.
- **Immowelt exception:** immowelt's robots.txt disallows `classified-search?…`
  pages, the only ones that filter and sort. The user knowingly chose to read
  them anyway (personal use). Keep it minimal: newest-first, stop paging when a
  page has nothing unseen or recent, prefilter on the search data, fetch
  `/expose/` pages (allowed) only for promising flats, 3 s gap.
- **Kleinanzeigen**: robots.txt allows `/s-wohnung-mieten/…` with the size
  and room filters and `/s-anzeige/…` ad pages; it disallows `preis:`,
  `sortierung:` and radius filters, so don't add them (the bot filters rent
  itself). 5 s gap, no retries; at most `max_requests_per_run` (10) requests per run, pages and ads together (the user asked for at most 10 per hour); reads up to `max_pages` (3) newest pages, stops at a page
  with nothing unseen; a full scan is capped at 10 pages.
  Ads that aren't flat offers are skipped by title (`kaNotAnOffer`). Keep it
  narrow: a title mentioning a tenant ("Suchen Nachmieter") is an offer;
  when in doubt, keep the ad.
  **Kleinanzeigen exception:** it refuses Go's HTTP client by its connection
  fingerprint (403 "IP-Bereich vorübergehend gesperrt") but answers curl with
  the same headers. The user knowingly chose to fetch it through the system
  curl (`client.HostCurl`, `internal/web/curl.go`; personal use).
  If curl gets 403 too, the normal 2-hour pause applies; don't escalate further
  without asking.
- **Transit uses a local stop list**, `aptbot/internal/analyze/hamburg_stops.json.gz`
  (embedded; ~7,700 bus/U/S/ferry stops from OpenStreetMap, ODbL). No live
  Overpass lookups inside Hamburg. Refresh it every few months with the
  "refresh stops" workflow (runs on GitHub: overpass-api.de doesn't answer the
  user's home IP since an Oct 13 burst of retries). Overpass stays only as a
  fallback outside the list's area, with persisted server cooldowns.
- Address geocoding (Nominatim) and any Overpass fallback are cached in
  `data/transit-cache.json` (carried between Actions runs with `actions/cache`).
- Test against the live sites sparingly; prefer fixtures in
  `internal/sources/testdata/`.

## Workflow

- Run `go vet ./... && go test ./...` in `aptbot/` before committing.
- Commit and push only when the user asks.
