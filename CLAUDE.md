# wohnenbot – notes for working on this repo

The bot is a Go program in `aptbot/` (stdlib only). It polls City-Wohnen,
ImmoScout24 and HomeCompany for Hamburg flats, evaluates them against
criteria, writes candidates to a shared Google Sheet and sends ntfy push
alerts. See `aptbot/README.md` for how it works.

## Where it runs

- **GitHub Actions, hourly** (`.github/workflows/aptbot.yml`), started only by
  an external job on **cron-job.org** (the user's account, "aptbot hourly")
  that POSTs to the `workflow_dispatch` API with a fine-grained token (this
  repo only, Actions read/write). There is no GitHub schedule (it dropped most
  runs and extra runs cost minutes). **The token expires 2027-01-12**; after
  that, cron-job.org reports failed executions and the user must create a new
  token and paste it into the job.
- **Minutes budget:** private repo, 2,000 free minutes/month, each run rounded
  up to a whole minute. Keep a run under 60 s: the compiled bot is cached
  (rebuilt only when Go code changes), so a normal run is ~40 s = 1 minute,
  ~750 minutes/month. Don't add steps or GitHub schedules without checking this.
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
- **Immowelt exception:** immowelt's robots.txt disallows `classified-search?…`
  pages, the only ones that filter and sort. The user knowingly chose to read
  them anyway (personal use). Keep it minimal: newest-first, stop paging when a
  page has nothing unseen or recent, prefilter on the search data, fetch
  `/expose/` pages (allowed) only for promising flats, 3 s gap.
- OpenStreetMap lookups are cached in `data/transit-cache.json` (and carried
  between Actions runs with `actions/cache`).
- Test against the live sites sparingly; prefer fixtures in
  `internal/sources/testdata/`.

## Workflow

- Run `go vet ./... && go test ./...` in `aptbot/` before committing.
- Commit and push only when the user asks.
