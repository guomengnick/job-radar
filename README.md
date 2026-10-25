# job-radar

A small cloud service that looks for jobs for me every morning, has Claude
judge the promising ones, and sends the result to my phone. The steps that
matter stay mine: a page is read when I press a button, and an application is
sent when I click 送出.

It has three sources:

- **Job boards.** Every day it checks ~16,000 company boards on Greenhouse,
  Lever and Ashby through their public APIs, and keeps the postings whose
  title, location and skills match my rules.
- **104 job alerts.** 104, Taiwan's largest job site, emails me its matches.
  The radar receives that mail (SES → S3 → Lambda) and reads the jobs out of it.
- **Pages I open.** A Chrome extension sends the posting I am reading, or all
  the 104 job tabs I have open, to be judged.

What comes back on Telegram: a morning digest of what each run did and the
new postings; Claude's judgement of a page (a score, reasons quoting the
posting, which résumé version to send, one cover-letter sentence); and, for a
104 job worth a look, a link that opens 104's own application form with the
résumé chosen and the letter written. The extension fills the form; I send it.

```
EventBridge Scheduler (Asia/Taipei)
  08:00         scan ×3 ──► Greenhouse / Lever / Ashby public APIs
                  │           per-host rate limit, timeout, retry
                  ├─► DynamoDB boards (ok / failing / dead)
                  ├─► DynamoDB jobs   (conditional put = "is it new?")
                  ├─► DynamoDB runs   (what every run did, 90-day TTL)
                  └─► S3 pages        (postings to be judged)
  08:40         digest ──► Telegram
  08:50         batch: the day's class A board jobs ──► Claude Message Batch
  every 10 min  batch: ended batches ──► judgements ──► Telegram summary
  Sunday        sync ──► new boards from a public dataset

104 alert mail ─► SES ─► S3 ─► ingest ─► DynamoDB jobs

Chrome extension ─► Lambda function URL (shared secret) ─► clip
                      POST /       one page   ─► Claude ─► Telegram
                      GET  /jobs   104 jobs to open in tabs
                      POST /batch  open tabs  ─► Claude Message Batch

CloudWatch logs, EMF metrics, alarms ─► SNS ─► email + Telegram
Secrets and the personal profile: SSM Parameter Store
```

One Go binary serves seven Lambda functions; each has its own trigger,
timeout and IAM role.

## Design points

| Problem | Decision |
|---|---|
| A board that no longer exists looks like an outage | Every request is classified (`ok`, `not_found`, `throttled`, `timeout`, …). Three 404s in a row mark a board dead; dead boards are rechecked weekly. In a 2026-10-11 sample of 1,300 boards per ATS, 36% of Greenhouse, 55% of Lever and 24% of Ashby boards returned 404. |
| "New" by publish date misses postings after a skipped day and drops undated ones | New means "posting ID never stored". A DynamoDB conditional put (`attribute_not_exists`) is the check and the dedupe in one step. A board's first successful fetch is a silent baseline. |
| Lambda stops at 15 minutes | Boards are scanned least-recently-checked first. A run cut short is marked `partial`, and the remaining boards go first next time, so no checkpoint file is needed. |
| One API host serves thousands of boards | Each host has its own token-bucket limiter (`x/time/rate`). Measured: 15 req/s with 16 in flight gave 0 throttled responses over 900 boards per ATS. A full Greenhouse pass then takes ~9 min. |
| Requests that send headers and then stall | `http.Client.Timeout` covers the body read; tested with a stalling `httptest` server. |
| Silent failure | A heartbeat alarm fires when no scan succeeded in 24 h, another when no 104 alert was parsed in 48 h, and the digest names any ATS without a run. Alarms reach Telegram through a small function, with email as the fallback. |
| 104 pages sit behind a bot check | No scraping, no headless browser, no evasion. The cloud reads only the mail 104 sends me; a page is read by my own browser when I press a button. |
| A posting is untrusted input to a model | The page goes in as data, and the model can answer only by calling one `record_assessment` tool with a fixed JSON schema; every value is checked and clamped, and a reply without the tool is an error. The tool only records. |
| Judging forty tabs at once | The Message Batches API at half price. The batch is recorded in DynamoDB, and a scheduled check sends the summary when it ends, since the API does not call back. |
| Is the judgement right? | Every judged page is kept for a year and linked from its judgement. `make eval` judges my labelled jobs again with the live or a draft profile and counts how many agree with me. |
| Which code is live? | The binary names its commit (Go's VCS stamp) on every log line, in the digest and in a response header. `make deployed` reads it from the deployed zips; `make apply` refuses unless the checkout is a clean, up-to-date `main`. |
| Cost | No VPC (so no NAT gateway), on-demand tables, metrics without dimensions, 14-day log retention, and a budget alarm: a few USD a month on AWS. Claude is about US$0.02–0.04 a page, half that in a batch, and each summary shows what it cost. |
| Secrets and personal data | The bot token, the API key and the profile the model judges against are SSM SecureStrings created by hand, never in Terraform state; each function's role reads only its own. Résumé names and the cover letter stay in the extension's local storage. None of it is in this repository. |

Architecture and trade-offs are in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md); decision records are listed in [docs/adr](docs/adr/README.md).

## Run locally

```bash
go test ./...
go run ./cmd/radar sync-boards                 # all three ATS, from the public dataset
go run ./cmd/radar scan --ats lever --limit 200
go run ./cmd/radar digest --dry-run            # prints the message
go run ./cmd/radar ingest-file alert.eml       # read a saved 104 alert
make eval LABELS=labels.csv                    # against AWS: judge labelled jobs again
```

State is kept in `.radar/state.json`. Set `TELEGRAM_BOT_TOKEN` and
`TELEGRAM_CHAT_ID` to send the digest to Telegram instead of printing it.

## Deploy

`make lambda`, then Terraform in `infra/`. Step by step (in Traditional Chinese): [docs/DEPLOY.zh-TW.md](docs/DEPLOY.zh-TW.md).

## Layout

```
cmd/radar          CLI: the same use cases on a laptop, plus eval and backfill-jd
cmd/lambda         Lambda entry point: schedule, S3, SNS and function-URL events
config/            rules (titles, locations, skills, thresholds, model), embedded at build
internal/ats       one adapter per ATS → a common Job
internal/fetch     HTTP: allowlist, per-host rate limit, timeout, retry with backoff, outcome classification
internal/filter    title filter, location classes A–D, skill score, salary floor
internal/mailin    raw mail from S3: headers, the sender check
internal/alert104  104 alert and contact mail → jobs
internal/assess    Claude: the rubric, the record_assessment tool, single and batch calls
internal/store     Store interface: JSON file (local) and DynamoDB (cloud)
internal/pages     kept pages in S3
internal/app       the use cases: Scan, Digest, IngestMail, Clip, batches, Evaluate
internal/notify    Telegram
internal/metrics   CloudWatch Embedded Metric Format
internal/version   which commit is running
extension/         the 送到雷達 Chrome extension (Manifest V3)
infra/             Terraform
```

## Data use

The radar uses only the public job-board APIs that these vendors offer for
embedding job listings. It sends one request per board per day under a rate
limit, and identifies itself in the User-Agent. It keeps titles, locations and
links for personal alerts, and privately the descriptions it judged; it does
not republish job descriptions. Board slugs
come from the public
[job-board-aggregator](https://github.com/Feashliaa/job-board-aggregator) dataset;
check its license before redistributing anything derived from it.

104 mail is my own alert mail, forwarded by my mailbox; 104 pages are read in
my browser, one at a time or the tabs I opened, when I press a button. Nothing
from 104 is republished.

## License

MIT. See [LICENSE](LICENSE). The license covers this repository's code only;
the board dataset is fetched at run time and keeps its own license.
