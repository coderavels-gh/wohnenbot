# wohnenbot – notes for working on this repo

The bot is a Go program in `aptbot/` (stdlib only). It polls City-Wohnen,
ImmoScout24 and HomeCompany for Hamburg flats, evaluates them against
criteria, writes candidates to a shared Google Sheet and sends ntfy push
alerts. See `aptbot/README.md` for how it works.

## Where it runs

- **GitHub Actions, hourly** (`.github/workflows/aptbot.yml`, cron `7 * * * *`).
  The run reads its config from repo secrets, not from the checkout:
  - `APTBOT_CONFIG`: the whole `aptbot/config.json`
  - `GOOGLE_SA_JSON`: `aptbot/secrets/service-account.json`
- `aptbot/config.json` and `aptbot/secrets/` are gitignored. **After any change
  to `config.json` (criteria, sources, notify), update the secret**, or the
  cloud keeps running the old config:
  `gh secret set APTBOT_CONFIG -R coderavels-gh/wohnenbot < aptbot/config.json`
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
- OpenStreetMap lookups are cached in `data/transit-cache.json` (and carried
  between Actions runs with `actions/cache`).
- Test against the live sites sparingly; prefer fixtures in
  `internal/sources/testdata/`.

## Workflow

- Run `go vet ./... && go test ./...` in `aptbot/` before committing.
- Commit and push only when the user asks.
