# Decisions

Record of design decisions that are settled or still pending. Items that need a
call from John are collected under "Open decisions for John" near the end.

## Not a fork; licensing

- Status: settled.
- The PRD assumes a fork of eshaffer321/itemize ("a destination swap, not a rewrite"). This is a new Go module (`github.com/postalservice14/itemize-ynab`), Walmart-only, with no Monarch code and no telemetry.
- eshaffer321/itemize has no LICENSE file, so its source cannot be copied or adapted. It was used as a design reference only (provider/categorizer/storage/sync-loop shape); everything here is a clean reimplementation, and `AGENTS.md` forbids copying its source.
- The Walmart data layer is `github.com/eshaffer321/walmart-client-go/v2` v2.2.1 (cookie-based, unofficial API), consumed as a normal dependency. It carries its own LICENSE file.

## PRD section 5.1 correction: ledger charge dates

- The PRD says "the ledger does not provide a date for each charge, so the order date stands in for it". That is wrong for walmart-client-go v2.2.1: `PaymentMethodCharges.ChargedDates` is parallel to `FinalCharges` and holds a per-charge date and time.
- Built behavior: matching anchors on the charge's own date. The order date is a fallback only when the ledger date is missing, zero or unparseable (or the date slice is shorter than the charge slice). If both are missing the charge has no date, and nothing is invented: it cannot match, and it is not pre-staged (`no usable date for the charge`).
- Ledger dates carry no time zone (they are parsed as UTC from Walmart's local wall-clock text). They are treated as a wall-clock calendar day, never an instant, and the matcher compares calendar days.
- Still UNVERIFIED against a live session: the real format of `Order.OrderDate` and `OrderSummary.DeliveredDate` (the parser accepts RFC 3339, `2006-01-02`, `2006-01-02T15:04:05`, `Jan 2, 2006`, `January 2, 2006`), and how often `ChargedDates` is actually populated.

## Split-in-place probe

- Status: **NOT YET RUN - pending John.** `itemize-ynab ynab probe-split -yes <txn-id>` is built and unit-tested against httptest only; it has never been run against a real plan.
- Run it on one throwaway, non-split, non-transfer transaction: `itemize-ynab -config config.local.yaml ynab probe-split -yes <txn-id>`. Then replace this entry with the verdict line it prints and whether the revert succeeded.
- Verdicts and what each means for `split_in_place`:
  - `split-in-place WORKS`: YNAB converts a non-split transaction to a split with one PUT. `auto` and `always` will take the primary path. Expect that YNAB may refuse to un-split through the API; the command then tells you to fix that one transaction by hand.
  - `REJECTED (400)`: YNAB refuses. `auto` falls back to the sibling path after the first rejection each run, wasting one request per run; set `split_in_place: never` to skip the attempt.
  - `IGNORED (200 but no subtransactions saved)`: YNAB accepts and drops the split. The writer's reply check catches it, restores the memo and falls back to the sibling path (same advice: `never`).
- The result decides whether split-in-place or the sibling path is the primary path (PRD section 6.5). Until then the writer treats both as possible.

## Walmart ledger 429 retry

- Decision: keep walmart-client-go's built-in ledger retry on 429 (backoff 5s, 10s, 20s, 3 retries) enabled by default. `walmart.WithMaxRetries(n)` exposes the setting; `-1` disables it, `0` keeps the client default. The binary uses the default.
- Reason: the ledger endpoint is the strictest rate-limited call, and one transient 429 should not abort a whole run. The retry is bounded, honors context cancellation, and when it is exhausted the client returns `rate limited`, which the adapter classifies as `order.RateLimited` (a blocked session that stops the run, exit code 3). Disable it only to observe raw rate limiting, as the spike does.
- This differs from YNAB, which is never retried: the YNAB client does not sleep or retry, and a 429 stops the run (PRD section 6.7).
- The orchestrator, not the adapter, owns the 2s spacing between Walmart order fetches (`PacingDelay`); the adapter adds no sleeps of its own.
- Error strings for 403/418 and 429 are untyped in the client, so they are matched by substring in one function (`classifyError`), verified against walmart-client-go v2.2.1. Re-check them when upgrading the client.

## Walmart blocks all exit 3, including a stale session

- `docs/ARCHITECTURE.md` originally proposed exit 1 for stale cookies. As built, a bot challenge (456), a stale session (403/418) and a Walmart 429 all become a blocked-session stop: the run ends early with the partial summary and exit code 3, so a scheduled job has one signal ("refresh the cookies") instead of two. A missing or empty cookie store at startup is still exit 1.

## YNAB reads per run

- PRD section 6.7 budgets two reads (categories, transactions). As built there are up to three: categories, accounts (only when `ynab.accounts` is configured, to validate the mapped IDs as PRD section 6.2 requires) and transactions (lazily, on the first charge that needs a match). A rerun where every charge is already processed makes no LLM call and no YNAB write.
- Category overrides are validated against the eligible categories at the start of every run. The model is offered the eligible names plus the override keys, so an override such as "Pet Supplies" to "Pets" works.

## YNAB transaction cache and delta merge

- PRD section 6.7 says to store `server_knowledge` and send a delta request. A delta response returns only the transactions that CHANGED since that knowledge value, so using `server_knowledge` alone would hand the matcher a partial list and it would miss unchanged candidates.
- Decision: keep a local copy of the transactions in SQLite (`ynab_txn_cache`, with `ynab_txn_cache_meta` holding the earliest date covered) and merge each delta into it. The saved knowledge is only ever used together with the cached rows it describes. One YNAB list call per run: a delta when a usable cache covers the window, otherwise a full fetch. A delta failure other than a rate limit or cancellation falls back to one full fetch.
- The cache is keyed by the `plan_id` string as written in the config, and is used only when that string is a UUID (see the next entry).
- A dry run may refresh this cache (it reads YNAB the same way). It writes nothing to YNAB and records no charges.

## Transaction cache only for a UUID plan_id

- Status: settled (controller ruling R22).
- Problem: with `plan_id: last-used` the cache and knowledge were keyed by the alias. If the plan behind the alias changed, a delta request carried the old plan's knowledge to the new plan, the matcher saw the old plan's transactions, the same-amount check missed the new plan's own bank transactions, and recent charges could be pre-staged as duplicates (or written against foreign transaction IDs).
- Decision: `TransactionSource.Load` bypasses the cache entirely when `plan_id` is not a canonical UUID (`last-used`, `default`, anything else): one full transactions read since the window start, deleted rows dropped, rows before the window start dropped, and nothing read from or written to the cache or knowledge tables. Still exactly one YNAB read per run. A UUID `plan_id` keeps the delta cache unchanged. The mode is logged at Debug.
- Cost: `last-used` runs read the whole window every run (no delta). See open decision 10 for a way to restore the delta.

## Run lock

- Status: settled (controller ruling R23).
- Problem: two overlapping runs (cron plus a manual run) could both load transactions and pass the database check before either recorded a charge, writing duplicate staged transactions or siblings.
- Decision: a `walmart` run, dry runs included (they may refresh the cache), takes an exclusive non-blocking `flock(2)` on `<database.path>.lock` after the config loads and before anything is built or any network call is made, and holds it until the run ends. A held lock exits 1 with `another itemize-ynab run is in progress (lock: <path>)`; it never waits. The kernel drops the lock if the process dies, so there is no stale-lock cleanup, and the file is left in place. `walmart import-curl` and the `ynab` commands do not take it. Code: `internal/infrastructure/lock`, wired in `internal/cli`.
- Platforms: `flock` is used on macOS, Linux and the BSDs. Elsewhere (Windows, and AIX and Solaris, whose Go `syscall` package has no `Flock`) `Acquire` returns `lock.ErrUnsupported` with a no-op release and `walmart` refuses to run (exit 1), rather than run unguarded. A best-effort `O_EXCL` lock was rejected because a crash would leave a stale lock to delete by hand.

## Writer: split_in_place modes

- `never`: a multi-category match goes straight to the sibling path (flagged user-entered split plus a flag on the original, outcome `needs_manual_match`).
- `auto` (default): try split-in-place; after the first rejection in a run (HTTP 400, or a 200 whose reply does not hold the requested subtransactions) split-in-place is switched off for the rest of that run and later charges go straight to the sibling path. The next run tries again.
- `always`: try split-in-place on every charge and never switch it off; a rejected charge still falls back to the sibling path rather than failing.
- A 200 that silently drops the split has already written the marker memo, so the writer puts the original memo back (and the original category, if the reply shows it changed) before creating the sibling. If that restore fails, the charge fails with an error naming the transaction, because a marked original is invisible to the matcher and needs a human; no sibling is created.
- Any other split-in-place error (5xx, network, 429) fails the charge without a fallback: the write may or may not have landed, so guessing is unsafe.
- Cost if wrong: John may have wanted `always` to fail the charge instead of falling back.

## Writer: memo, clock and pre-staging age

- The parent memo is `memo.AppendMarker(existing memo, key)`; existing text is kept and never replaced. The one exception comes from `AppendMarker` itself: if memo plus marker would exceed YNAB's 200-character limit, the tail of the existing text is cut and ends in "…" so the marker always fits. When the existing memo is empty, a single-category charge uses its item names as the base. A multi-category `split_in_place` parent keeps its existing memo plus the marker (marker only if the memo was empty), since the item names are on the subtransactions. Only the sibling and a staged split carry the marker alone.
- The clock is injected (`Config.Now`); the writer never reads the system time.
- A no-match charge is pre-staged only if: the card maps to an account; the charge has a date; `now - charge date < 10 days` (the limit is fixed at 10, not a config key; measured from the charge date at midnight UTC, so it means strictly under 240 hours); the date is no more than one day in the future (time-zone slack); the charge's match window (charge date minus `days_before`) does not start before the first loaded transaction day (`Config.LoadedFrom`, set by the orchestrator to the transactions' lower bound; otherwise a same-amount transaction could exist unseen, note "charge date window starts before the loaded transaction range; widen -days or re-run"); and no transaction in that account has the same amount within the match window, whatever its payee, split state or marker. Otherwise the charge is `skipped` with a note naming the failed condition and is not recorded, so it is retried next run.
- Tool-created transactions (siblings and staged) are unapproved (see Open decisions).

## Writer: -force

- The writer still reads the SQLite record under `-force`.
  - Recorded `categorized`, `split_in_place` or `staged_for_import`: the record is ignored and the charge is processed again. Only the memo marker protects against a duplicate write: the matcher excludes marked transactions, and the same-amount check then blocks pre-staging. A forced rerun over those charges is therefore `skipped` unless the marker was removed by hand.
  - Recorded `needs_manual_match`: never written again. The charge is `skipped` (not re-recorded) with a note naming the existing sibling, because a rerun would match the flagged but unmarked original and create a duplicate sibling. Resolve or delete the sibling in YNAB first. Reprocessing such a charge then also needs its `ynab_charges` row removed, since `-force` alone keeps refusing.
  - Not protected: a `needs_manual_match` charge whose record is gone (lost or replaced database). See the known gap below.

## Other behavior the build settled

- Zero-cent splits are omitted: a category whose share of the charge rounds to zero cents gets no subtransaction (YNAB gains nothing from a zero amount). Side effect: its item names also vanish from the memo. Cost if wrong: a category with under one cent of weight disappears from the split.
- Gift-card and other non-card portions, refunds (negative amounts) and zero amounts never become charges; they are reported as `skipped` ledger rows. The adapter guarantees every charge amount is positive, because the matcher does not check.
- Ctrl-C and SIGTERM cancel the run: the partial summary is printed and the exit code is 1 (the PRD defines no signal exit code). A second Ctrl-C kills the process.
- `OPENAI_MODEL` has no default and is required with the OpenAI backend, because no current OpenAI model could be verified. The Anthropic default `claude-haiku-5-5` is a valid current model ID per Anthropic's model list, but this tool has not been exercised against the live API. Haiku 5.5 thinks adaptively by default (the request sends no `thinking` setting), and the thinking counts against the request's `max_tokens`; the client does not check `stop_reason`, so a reply cut off at `max_tokens` or a refusal arrives as unusable text and fails that order (exit 2) rather than being recognized.
- LLM failures (including an invalid reply after one repair attempt) fail the order's charges (exit 2); they do not stop the run. The LLM clients use `net/http` with a configurable base URL for httptest and add no dependencies.
- Toolchain and dependencies are at their latest releases as of 2026-10-09: Go 1.27.2 (`go` directive and `.tool-versions`), golangci-lint 2.14.0, `modernc.org/sqlite` v1.60.1, `github.com/pressly/goose/v3` v3.28.0, `github.com/eshaffer321/walmart-client-go/v2` v2.2.1, and the CI actions at their latest majors (checkout v7, setup-go v7, golangci-lint-action v9). The first build pinned Go 1.25.x to match the upstream design reference; that constraint was dropped on request.

## Known gap: needs_manual_match without the database

- On `needs_manual_match` the sibling carries the marker but the flagged original does not (the writer only sets its flag). The SQLite record is the only thing stopping a second sibling: with the record present, both a normal rerun (`already_processed`) and a `-force` rerun (`skipped`) write nothing. If the database is lost, a rerun (forced or not) matches the original again and creates a second sibling and a second flag.
- The same gap opens without losing the database:
  - When recording fails after a sibling was created. The row's note then names the sibling, says a rerun will create a second sibling and that one must be deleted by hand.
  - Ambiguous write timeouts: the YNAB client times out after 30 seconds, but a POST or PUT can time out after YNAB applied it. The charge fails and is not recorded. For a categorized, split or staged charge the applied marker still blocks a second write; a sibling POST applied after its timeout is unrecorded, its original is unmarked (and unflagged), and a rerun creates a second sibling.
- Recommended mitigation: 1(b) below, marking the original's memo when flagging it, closes all three cases. Open decision for John, not implemented; see item 1 below.

## Open decisions for John

Current defaults are what the code does today; none of these is implemented differently.

1. Lost database and `needs_manual_match`. Gap above. Mitigations: (a) exclude transactions that already carry the configured flag color from matching (cheap and restart-safe, but it also skips originals you flagged for other reasons); (b) append the marker to the original's memo when flagging it (the original becomes invisible to the matcher; your later manual match keeps or drops the marker). Default: neither; the database row is the only guard.
2. A 200 whose subtransactions differ from what was sent. Today the writer treats it as "ignored": restores the memo and creates a flagged sibling, leaving the original with an unverified split, so the amount can be counted twice until fixed by hand. The recommended alternative is: when the reply contains live subtransactions that differ from the request, restore the memo and return an error naming the transaction, WITHOUT creating a sibling (and recording nothing); keep the restore-and-sibling path for the "0 subtransactions = ignored" case. Default: restore and sibling.
3. Sibling versus flag-only for `needs_manual_match` (PRD section 11). The sibling temporarily doubles the outflow in the account; flag-only would put the proposed split in the memo and leave the original alone. Default: create the sibling; revisit after real use.
4. Refunds in v1.1: look up which items were returned, or mirror the original order's split proportions. Default: refunds are reported as `skipped` and never changed.
5. Approved or unapproved for tool-created transactions. Unapproved lands them in YNAB's review queue and is safer; approved saves a click. Default: unapproved. (Matched transactions keep whatever approval state they had.)
6. A reset command for a `needs_manual_match` database row, so a forced reprocess does not require editing SQLite by hand. Default: none; delete the `ynab_charges` row manually.
7. Ctrl-C exit code: 1 today; 130 is the shell convention. Default: 1.
8. The `claude-haiku-5-5` default (a valid current model ID, not yet exercised against the live API by this tool). Run once against the live API; if replies hit `max_tokens` because of adaptive thinking, raise the limit or check `stop_reason`. Default: `claude-haiku-5-5`.
9. Whether a dry run should avoid refreshing the local transaction cache. Avoiding it makes a dry run strictly read-only locally but costs a full fetch each time or a stale view. Default: it may refresh the cache.
10. `last-used` and the delta cache. Current: a non-UUID `plan_id` bypasses the cache (one full read per run). Alternative: fingerprint the plan from the categories already fetched in the preflight (for example the ID of the internal "Inflow: Ready to Assign" category) and key the cache by it, restoring the delta for `last-used`. Default: bypass.

## Known limitations and deferred review notes

Behavior or safety items a user or maintainer should know; none blocks the manual acceptance run.

- The transaction cache is used only with an explicit plan UUID; with `last-used` every run makes one full transactions read (see open decision 10).
- Every run fetches every order in the `-days` window from Walmart (order plus ledger, 2s apart) before it can check the database, so a large window costs time and Walmart requests even when nothing is new.
- Order listing is capped at 25 pages of 20 and the cap is hit silently (no warning). Whether Walmart's `MinTimestamp` filter works is unverified; a second client-side filter drops summaries whose delivered date parses and is older than the window.
- A charge that hits a YNAB 429 before any write gets no row; only the "stopped early" block names it. The YNAB transactions are loaded before an order's items are categorized, so a failed load fails the order's pending charges without spending LLM tokens (those rows show no proposed split). A forced rerun of a `needs_manual_match` order still costs one LLM call.
- With `-force`, the refusal for a `needs_manual_match` charge persists after the sibling is deleted in YNAB, until its `ynab_charges` row is removed by hand.
- One unpriced item makes its whole order fail (safe; to be confirmed against real data). Refunded items are tracked only from the order's items, with no consumer yet.
- `import-curl` needs `YNAB_TOKEN` set (it loads the whole config), silently replaces an existing cookie store (that is the refresh workflow), and leaves an empty 0700 directory behind when the capture is bad.
- `.gitignore` covers cURL captures named `curl*.txt` or `*.curl`; a capture saved under any other name holds live cookies and would not be ignored.
- `ANTHROPIC_BASE_URL` and `OPENAI_BASE_URL` are honored from the environment (intended for testing): an ambient variable meant for another tool would receive the API key. The LLM clients never follow redirects, so the key is not forwarded to a redirect target.
- Two charges with identical amounts on the same day in the same account cannot be told apart; the matcher pairs them with transactions lowest ID first, so each may get the other's transaction.
- The categorizer's repair retry embeds the model's previous reply and quoted item names in the next prompt (bounded, only after a rejected reply, and output validation still applies).
- yaml type errors can echo the offending raw config text; `${VAR}` expansion has no escape for a literal `${`.
- Still UNVERIFIED against live Walmart (needs the spike run): date formats, which `OrderSummary.Type` values mean in-store, tax/tip/fee placement per fulfillment type, whether refunds or Walmart Cash show in `FinalCharges`, whether purchase history lists newest first (`-max` assumes it), and cookie lifetime.
