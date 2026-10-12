# aptbot – Hamburg flat scout

Every 30 minutes the bot checks ImmoScout24, HomeCompany and City-Wohnen for new listings. For each new listing it:

1. reads the listing page and checks it against your criteria,
2. adds the candidates to a shared Google Sheet,
3. sends you a WhatsApp alert, falling back to a push notification and then to email if WhatsApp fails.

It's written in Go using only the standard library. There are no API keys to buy and no paid services.

## How the filtering works

Each criterion gets one of three results: ✓ (confirmed), ✗ (ruled out) or ? (the listing doesn't say).

- **MATCH**: every criterion is ✓. You get an alert.
- **REVIEW**: nothing is ✗, but some criteria are ?. The listing goes to the sheet, and you also get an alert if `alert_on_review` is on.
- **REJECT**: at least one criterion is ✗. The listing is recorded in the `Seen` tab only.

Every cell in the sheet shows the text the decision was based on, for example `✓ …Sonniger Südbalkon…`, so you can spot a misread at a glance.

| Criterion | How it's checked |
|---|---|
| Warm rent ≤ 1600 € | Uses Warmmiete, Gesamtmiete or Pauschalmiete. If none is given, it adds up Kaltmiete + Nebenkosten + Heizkosten. |
| ≥ 2 rooms, ≥ 50 m² | Parsed from the listing. |
| Balcony, washing machine, kitchen | German keywords plus negations such as "kein Balkon" or "ohne EBK". A washing-machine hook-up (Waschmaschinenanschluss) counts as a pass; turn that off with `washing_machine_connection_ok`. A shared laundry room counts as ?. |
| Duration ≥ 12 months or unlimited | Looks for "unbefristet", "Maximale Mietzeit …", "befristet bis …" and date ranges. A normal ImmoScout lease with no time limit mentioned counts as unlimited. |
| Anmeldung possible | Explicit wording in the listing. A normal open-ended lease counts as a pass, and City-Wohnen states that every one of its flats allows Anmeldung. |
| ≥ 2 of Bus / U-Bahn / S-Bahn nearby | If the street address is known, the bot geocodes it and searches OpenStreetMap for stops: bus within 400 m, U/S-Bahn within 900 m. Otherwise it reads the listing's own location text (HomeCompany's "Lage: U2, U3, Bus 4 …" line, for example). |

## Setup (about 20 minutes, one time)

### 1. Google Sheet (the shared tracker)
1. Create an empty Google Sheet, for example "Flat hunt Hamburg", and share it with whoever you are searching with.
2. Go to <https://console.cloud.google.com>, create a project, and enable the **Google Sheets API** for it.
3. Under IAM & Admin → Service accounts, create a service account. Then open it, go to Keys → Add key → JSON, and save the file as `secrets/service-account.json`.
4. Share the sheet with the service account's email address (`…@….iam.gserviceaccount.com`) as an **Editor**.
5. Copy the ID from the sheet's URL (`docs.google.com/spreadsheets/d/<THIS PART>/edit`) into `config.json`.

The bot creates two tabs itself. **Listings** has a **Status** dropdown (New / Interested / Contacted / Viewing / Applied / Not interested / Gone) and a free **Notes** column. **Seen** is the bot's memory of which listings it has already processed. The bot only ever appends rows, so your edits are never overwritten. In the Google Sheets app on your iPhone, adding a filter view on Status makes it easy to triage.

### 2. WhatsApp via CallMeBot (each of you does this once)
1. Open <https://www.callmebot.com/blog/free-api-whatsapp-messages/> and save the WhatsApp number shown there as a contact. CallMeBot changes it occasionally, so take it from the page rather than an old guide.
2. Send it this WhatsApp message: `I allow callmebot to send me messages`
3. It replies with your **apikey**. Put it in an environment variable: `CALLMEBOT_KEY_person1` for you, `CALLMEBOT_KEY_2` for the second person.

### 3. Push fallback (recommended, free)
Install the **ntfy** app on your iPhone, and optionally on your Mac or at ntfy.sh in a browser. Subscribe to a long random topic name, for example `hh-flats-8f3k2q9x`, and put the same name in `notify.ntfy.topic`. Anyone who knows the topic can read it, so keep it unguessable. If you want push notifications as well as WhatsApp, set `"always": true`.

### 4. ImmoScout search
1. On immobilienscout24.de, logged in, set up a search: Hamburg, Wohnung mieten, rent up to 1600 € set to **Warmmiete (calculated total rent)**, 2+ rooms, 50+ m², balcony, sorted by **newest**.
2. Copy the results page URL into `sources.immoscout.search_urls`. You can add several, for example one per area.

Because these filters run on ImmoScout's side, you get fewer and better results.

### 5. Test, then install
```bash
cp config.example.json config.json   # then fill it in
export CALLMEBOT_KEY_person1=… CALLMEBOT_KEY_2=…

go run ./cmd/aptbot -test-alert                    # does WhatsApp arrive?
go run ./cmd/aptbot -once -dry-run -only citywohnen   # see what each scraper extracts
go run ./cmd/aptbot -once -dry-run -only homecompany
go run ./cmd/aptbot -once -dry-run -only immoscout

./deploy/install-mac.sh                            # every 30 min from now on
tail -f ~/Library/Logs/aptbot.log
```
On the **first real run**, the bot records every listing that is currently online and writes the candidates to the sheet, but sends **no alerts**, so you don't get a flood of messages. From then on you only get alerts for new listings.

## Where to run it (all free)

| Option | Good for | Caveat |
|---|---|---|
| **Mac LaunchAgent** (`deploy/install-mac.sh`) | Best for ImmoScout, because requests come from your home IP. | Pauses while the Mac sleeps and catches up when it wakes. To keep it awake while plugged in: `sudo pmset -c sleep 0` (display can still sleep). |
| **Docker on any always-on machine** (`docker compose up -d`) | 24/7 on an old laptop or Raspberry Pi. | Loops on its own; no cron needed. |
| **GitHub Actions** (`.github/workflows/aptbot.yml`, private repo) | 24/7 and free. A private repo gets 2,000 minutes per month and 48 runs a day uses most of that, so set the schedule to hourly if you go over. | Runs on datacenter IPs, which ImmoScout may block. The agency sites are fine. |

All three keep their state in the sheet, so you can run more than one, for example the Mac plus GitHub Actions. Listings are still never alerted twice.

## Known limits / next steps
- **Suchen+ early access.** The mobile API is used without logging in, so listings that are visible only to Suchen+ members during the first hours may show up later than in the app. A good way to cover that gap is to turn on ImmoScout's own instant push for the same saved search in the ImmoScout app.
- **HomeCompany and City-Wohnen only rent furnished flats for limited periods**, often 3–12 months. With your 12-month minimum most of their listings will be rejected on duration. The bot still flags the ones that fit.
- **Site changes.** If a site changes its layout, the bot logs that source as `FAILED` or as `0 listings on index`. Run `-dry-run -only <source>` to see what it extracts.
- **Landlord messaging** (next phase): this will reuse the same listing data and your Status column, for example "send the intro when Status = Interested".
