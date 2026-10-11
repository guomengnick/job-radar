# job-radar

A small cloud service that watches public job boards every morning and sends
the few postings worth reading to my phone.

Every day it checks ~16,000 company boards on Greenhouse, Lever and Ashby. It
keeps postings whose title, location and skills match my rules, and sends one
Telegram message. The message covers what each scan did and the new postings
above the notify threshold.

I built it because the local tool I used only ran while my laptop was awake,
gave no view of what it had done, and could not tell a dead board from a
throttled one.

```
EventBridge Scheduler ──► Lambda (Go) ──► Greenhouse / Lever / Ashby public APIs
   08:00 Asia/Taipei        scan          per-host rate limit, timeout, retry
                             │
                             ├─► DynamoDB  boards (ok / failing / dead)
                             ├─► DynamoDB  jobs   (conditional put = "is it new?")
                             ├─► DynamoDB  runs   (what every run did, 90-day TTL)
                             └─► CloudWatch EMF metrics ─► dashboard, alarms
   08:40                   digest ──► Telegram (token in SSM Parameter Store)
   Sunday                  sync-boards ──► add new boards from a public dataset
```

## Design points

| Problem | Decision |
|---|---|
| A board that no longer exists looks like an outage | Every request is classified (`ok`, `not_found`, `throttled`, `timeout`, …). Three 404s in a row mark a board dead; dead boards are rechecked weekly. In a 2026-10-11 sample of 1,300 boards per ATS, 36% of Greenhouse, 55% of Lever and 24% of Ashby boards returned 404. |
| "New" by publish date misses postings after a skipped day and drops undated ones | New means "posting ID never stored". A DynamoDB conditional put (`attribute_not_exists`) is the check and the dedupe in one step. A board's first successful fetch is a silent baseline. |
| Lambda stops at 15 minutes | Boards are scanned least-recently-checked first. A run cut short is marked `partial`, and the remaining boards go first next time, so no checkpoint file is needed. |
| One API host serves thousands of boards | Each host has its own token-bucket limiter (`x/time/rate`). Measured: 15 req/s with 16 in flight gave 0 throttled responses over 900 boards per ATS. A full Greenhouse pass then takes ~9 min. |
| Requests that send headers and then stall | `http.Client.Timeout` covers the body read; tested with a stalling `httptest` server. |
| Silent failure | A heartbeat alarm fires when no scan succeeded in 24 h, and the digest names any ATS without a run. |
| Cost | No VPC (so no NAT gateway), on-demand tables, metrics without dimensions, 14-day log retention, and a budget alarm. Expected to cost a few USD a month. |
| Secrets | The bot token is an SSM SecureString created by hand, so it is never in Terraform state. Only the digest function's IAM role can read it. |

Architecture and trade-offs are in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md); decision records are listed in [docs/adr](docs/adr/README.md).

## Run locally

```bash
go test ./...
go run ./cmd/radar sync-boards                 # all three ATS, from the public dataset
go run ./cmd/radar scan --ats lever --limit 200
go run ./cmd/radar digest --dry-run            # prints the message
```

State is kept in `.radar/state.json`. Set `TELEGRAM_BOT_TOKEN` and
`TELEGRAM_CHAT_ID` to send the digest to Telegram instead of printing it.

## Deploy

`make lambda`, then Terraform in `infra/`. Step by step (in Traditional Chinese): [docs/DEPLOY.zh-TW.md](docs/DEPLOY.zh-TW.md).

## Layout

```
cmd/radar        CLI (same use cases as the Lambda)
cmd/lambda       Lambda entry point: {"task": "scan" | "digest" | "sync-boards"}
config/          rules (title keywords, location policy, skills, thresholds), embedded at build
internal/ats     one adapter per ATS → a common Job
internal/fetch   HTTP: allowlist, per-host rate limit, timeout, retry with backoff, outcome classification
internal/filter  title filter, location classes A–D, skill score
internal/store   Store interface: JSON file (local) and DynamoDB (cloud)
internal/app     Scan, Digest, SyncBoards
internal/notify  Telegram
internal/metrics CloudWatch Embedded Metric Format
infra/           Terraform
```

## Data use

The radar uses only the public job-board APIs that these vendors offer for
embedding job listings. It sends one request per board per day under a rate
limit, and identifies itself in the User-Agent. It keeps titles, locations and
links for personal alerts and does not republish job descriptions. Board slugs
come from the public
[job-board-aggregator](https://github.com/Feashliaa/job-board-aggregator) dataset;
check its license before redistributing anything derived from it.

## License

MIT. See [LICENSE](LICENSE). The license covers this repository's code only;
the board dataset is fetched at run time and keeps its own license.
