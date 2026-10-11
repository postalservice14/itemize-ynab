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
- Built behavior: matching anchors on the charge's own date. The order date is a fallback only when the ledger date is missing, zero or unparseable (or the date slice is shorter than the charge slice). If both are missing the charge has no date, and nothing is invented: it cannot match and is `skipped` (`no usable date for the charge`).
- Ledger dates carry no time zone (they are parsed as UTC from Walmart's local wall-clock text). They are treated as a wall-clock calendar day, never an instant, and the matcher compares calendar days.
- Verified live on 2026-10-09 (one delivery order, 1 charge): `Order.OrderDate` and `OrderSummary.DeliveredDate` are RFC 3339 with a UTC offset (`2026-07-23T09:20:33-05:00`), and `ChargedDates` is populated (`2026-07-23T14:27:00Z`). The `Z` is only a label: the charge came 2 minutes after the 14:25 Central delivery, so the value is Central wall-clock time, which matches treating it as a calendar day. The client's built-in default `getOrder` hash still worked, so no `getOrder` capture was needed. Orders with several charges: see "Charges the bank posts together". Still unverified: how often `ChargedDates` is empty.

## Split-in-place probe

- Status: **run 2026-10-09, verdict `split-in-place WORKS`.** One PUT with `category_id: null`, a marker memo and two subtransactions converted a non-split, uncategorized card transaction into a split; the follow-up GET showed `split=true` with both subtransactions saved. YNAB cannot revert it through the API (the command said so), so the throwaway transaction has to be un-split by hand in the app. The command is `itemize-ynab ynab probe-split -yes [-categories "A,B"] <txn-id>`.
- Verdicts and what each means for `split_in_place`:
  - `split-in-place WORKS`: YNAB converts a non-split transaction to a split with one PUT. `auto` and `always` will take the primary path. Expect that YNAB may refuse to un-split through the API; the command then tells you to fix that one transaction by hand.
  - `REJECTED (400)`: YNAB refuses. `auto` flags the transaction for a manual split after the first rejection each run, wasting one request per run.
  - `IGNORED (200 but no subtransactions saved)`: YNAB accepts and drops the split. The writer's reply check catches it, restores the memo and flags the transaction for a manual split.
- Consequence: split-in-place is the primary path (PRD section 6.5) and `auto`, the default, uses it. Flagging for a manual split is the fallback for a rejection or an ignored split (see "Update only: no transaction creation"). Not tested live: a reconciled or already-split transaction, and the 400/ignored branches (httptest only).

## Walmart ledger 429 retry

- Decision: keep walmart-client-go's built-in ledger retry on 429 (backoff 5s, 10s, 20s, 3 retries) enabled by default. `walmart.WithMaxRetries(n)` exposes the setting; `-1` disables it, `0` keeps the client default. The binary uses the default.
- Reason: the ledger endpoint is the strictest rate-limited call, and one transient 429 should not abort a whole run. The retry is bounded, honors context cancellation, and when it is exhausted the client returns `rate limited`, which the adapter classifies as `order.RateLimited` (a blocked session that stops the run, exit code 3). Disable it only to observe raw rate limiting, as the spike does.
- This differs from YNAB, which is never retried: the YNAB client does not sleep or retry, and a 429 stops the run (PRD section 6.7).
- The orchestrator, not the adapter, owns the 2s spacing between Walmart order fetches (`PacingDelay`); the adapter adds no sleeps of its own.
- Error strings for 403/418 and 429 are untyped in the client, so they are matched by substring in one function (`classifyError`), verified against walmart-client-go v2.2.1. Re-check them when upgrading the client.

## Walmart blocks all exit 3, including a stale session

- `docs/ARCHITECTURE.md` originally proposed exit 1 for stale cookies. As built, a bot challenge (456), a stale session (403/418) and a Walmart 429 all become a blocked-session stop: the run ends early with the partial summary and exit code 3, so a scheduled job has one signal ("refresh the cookies") instead of two. A missing or empty cookie store at startup is still exit 1.
- Seen live on 2026-10-09: a purchase-history request minutes after a successful dry run came back as HTTP 412 with a PerimeterX `/blocked` redirect in the body (the client only knows 456). The adapter now treats a 412 whose body holds that redirect as a bot challenge too, so the run stops with exit 3. A plain 412 without the redirect is still an ordinary failure.

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
- Problem: two overlapping runs (cron plus a manual run) could both load transactions and pass the database check before either recorded a charge, writing the same transaction twice (at the time, also duplicate staged transactions or siblings).
- Decision: a `walmart` run, dry runs included (they may refresh the cache), takes an exclusive non-blocking `flock(2)` on `<database.path>.lock` after the config loads and before anything is built or any network call is made, and holds it until the run ends. A held lock exits 1 with `another itemize-ynab run is in progress (lock: <path>)`; it never waits. The kernel drops the lock if the process dies, so there is no stale-lock cleanup, and the file is left in place. `walmart import-curl` and the `ynab` commands do not take it. Code: `internal/infrastructure/lock`, wired in `internal/cli`.
- Platforms: `flock` is used on macOS, Linux and the BSDs. Elsewhere (Windows, and AIX and Solaris, whose Go `syscall` package has no `Flock`) `Acquire` returns `lock.ErrUnsupported` with a no-op release and `walmart` refuses to run (exit 1), rather than run unguarded. A best-effort `O_EXCL` lock was rejected because a crash would leave a stale lock to delete by hand.

## Update only: no transaction creation

- Status: settled 2026-10-10 (John: "I don't want this app to make new transactions. Only categorize and split existing transactions that have been imported from my bank"). Supersedes "Fallback shape and approval of tool-created transactions" and the pre-staging rules.
- The YNAB client has no create method, and `ynab.SaveTransaction` carries only category, memo, flag and subtransactions, so the tool cannot create a transaction or change an imported transaction's account, date, amount, payee, cleared or approved state. A unit test fails if a `Create*` method is added to the client.
- No match: the charge is `skipped` with the note "no matching bank transaction yet; retried next run" and is not recorded, so a later run picks it up once the bank import arrives. (A charge with no usable date says so instead.) Pre-staging, its 10-day age limit, the same-amount and loaded-range checks, and the payee name for created transactions are gone.
- Split not taken (a 400, or a 200 that ignored the split; after the memo restore): one PUT flags the matched transaction with `flag_color` and appends the `[itemize:...]` marker to its memo. Outcome `needs_manual_match`, recorded with the matched transaction's ID. The summary always lists the planned split under that row, so it can be typed in by hand. If the flag PUT fails, the charge fails and nothing is recorded; nothing was created, so a retry is safe. John chose this over leaving the transaction untouched and retrying, and over categorizing the whole amount to the largest category.
- `split_in_place: never` is removed (it would only mean "flag every multi-category charge") and is now a config error.
- Charges recorded as `staged_for_import` by earlier versions still count as processed and show as `already_processed` ("recorded as staged_for_import").

## Writer: split_in_place modes

- `auto` (default): try split-in-place; after the first rejection in a run (HTTP 400, or a 200 whose reply does not hold the requested subtransactions) split-in-place is switched off for the rest of that run and later multi-category charges are flagged for a manual split. The next run tries again.
- `always`: try split-in-place on every charge and never switch it off; a rejected charge is still flagged for a manual split rather than failing.
- A 200 that silently drops the split has already written the marker memo, so the writer puts the original memo back (and the original category, if the reply shows it changed) before flagging. If that restore fails, the charge fails with an error naming the transaction, because a marked original is invisible to the matcher and needs a human.
- Any other split-in-place error (5xx, network, 429) fails the charge without a fallback: the write may or may not have landed, so guessing is unsafe.
- Cost if wrong: John may have wanted `always` to fail the charge instead of flagging it.

## Writer: memo and clock

- The parent memo is `memo.AppendMarker(existing memo, key)`; existing text is kept and never replaced. The one exception comes from `AppendMarker` itself: if memo plus marker would exceed YNAB's 200-character limit, the tail of the existing text is cut and ends in "…" so the marker always fits. When the existing memo is empty, a single-category charge uses its item names as the base. A multi-category `split_in_place` parent keeps its existing memo plus the marker (marker only if the memo was empty), since the item names are on the subtransactions. A transaction flagged for a manual split keeps its existing memo plus the marker.
- Item names in memos are short names (decided 2026-10-11, at John's request: long Walmart names were hard to read in YNAB). The categorizer asks for a 1 to 3 word `short` name for each item in the same LLM call that picks its category, so there is no extra call. Short names are checked softly: one that is missing, not a string, over 30 characters, or holds a control character or an `[itemize:` marker (any case) falls back to the full Walmart name. A bad short name never costs a repair call or fails a charge. There is no config switch. The summary and the database don't store item names, so nothing else changes.
- Each memo item carries its amount in parentheses, "Milk ($3.77)" (decided 2026-10-11, at John's request, so a recategorized item shows how much to move). The amount is the item's share of its category's total including tax, fees and tip (John chose this over the pre-tax Walmart price): the category's cents are split over its items by subtotal with the same largest-remainder allocator, so the amounts in a memo add up to the category total. With `BuildOrderSplits` that total is the order-level one, so a category spread over several charges shows the same amounts on each split. Nameless items keep their share but are not listed. A category holding a negative item (a coupon) lists names only. A list over 200 characters keeps the whole entries that fit and ends with ", …", so an amount is never cut in half.
- The clock is injected (`Config.Now`); the writer never reads the system time.

## Writer: -force

- The writer still reads the SQLite record under `-force`, and ignores it: a recorded charge is processed again. Only the memo marker protects against a duplicate write: the matcher excludes marked transactions, so a forced rerun over written charges is `skipped` unless the marker was removed by hand. Removing the marker from a `needs_manual_match` transaction and forcing a rerun retries its split.

## Other behavior the build settled

- Zero-cent splits are omitted: a category whose share of the charge rounds to zero cents gets no subtransaction (YNAB gains nothing from a zero amount). Side effect: its item names also vanish from the memo. Cost if wrong: a category with under one cent of weight disappears from the split.
- Gift-card and other non-card portions, refunds (negative amounts) and zero amounts never become charges; they are reported as `skipped` ledger rows. The adapter guarantees every charge amount is positive, because the matcher does not check.
- Ctrl-C and SIGTERM cancel the run: the partial summary is printed and the exit code is 1 (the PRD defines no signal exit code). A second Ctrl-C kills the process.
- `OPENAI_MODEL` has no default and is required with the OpenAI backend, because no current OpenAI model could be verified. The Anthropic default `claude-haiku-5-5` is a valid current model ID per Anthropic's model list, but this tool has not been exercised against the live API. Haiku 5.5 thinks adaptively by default (the request sends no `thinking` setting), and the thinking counts against the request's `max_tokens`; the client does not check `stop_reason`, so a reply cut off at `max_tokens` or a refusal arrives as unusable text and fails that order (exit 2) rather than being recognized.
- LLM failures (including an invalid reply after one repair attempt) fail the order's charges (exit 2); they do not stop the run. The LLM clients use `net/http` with a configurable base URL for httptest and add no dependencies.
- Toolchain and dependencies are at their latest releases as of 2026-10-09: Go 1.27.2 (`go` directive and `.tool-versions`), golangci-lint 2.14.0, `modernc.org/sqlite` v1.60.1, `github.com/pressly/goose/v3` v3.28.0, `github.com/eshaffer321/walmart-client-go/v2` v2.2.1, and the CI actions at their latest majors (checkout v7, setup-go v7, golangci-lint-action v9). The first build pinned Go 1.25.x to match the upstream design reference; that constraint was dropped on request.

## needs_manual_match without the database

- Status: settled (was open decision 1, option b). The flag PUT also appends the `[itemize:...]` marker to the memo, so the matcher no longer sees the transaction even without the database. Since 2026-10-10 there is no sibling, so the earlier gaps (a sibling POST that timed out after YNAB applied it, a sibling recorded without a flagged original) no longer exist.

## A 200 with a different split than requested

- Status: settled (was open decision 2). When YNAB answers a split-in-place update with live subtransactions that differ from the request, the writer restores the memo (and category if changed), does not flag it, records nothing, and returns an error naming the transaction. The original may hold an unverified split, so check it by hand. Split-in-place is switched off for the rest of the run in `auto` mode, as after any rejection.
- A 200 with no live subtransactions still counts as "ignored": restore the memo, then flag it for a manual split.

## First live run and the LLM default

- Status: settled (was open decision 8). On 2026-10-09 a dry run and then a real run, both with `-days 90`, exercised the whole pipeline against live YNAB, Walmart and Anthropic. The `claude-haiku-5-5` default returned a valid single-category answer in about 2 seconds with no `max_tokens` problem, so it stays the default.
- The real run matched the one delivery order in the window to its YNAB transaction (same amount, YNAB date 3 days after the Walmart charge date) and wrote outcome `categorized`. Read back from YNAB: category Misc, memo = the item name plus the `[itemize:...]` marker (the memo was empty, so the item name is the base), still cleared, no flag, no subtransactions. The tool never sends `approved` for a matched transaction, so the approval state is whatever it was before.
- The 90-day window returned only that one order. The other Walmart charges in YNAB over the same weeks are purchases made on a different Walmart account (John's spouse), so they are correctly absent from this account's purchase history. The tool is per Walmart account: one cookie store, one order history. Covering a second account would need a second cookie store and run, which is not built.
- The exclude-list work came from this run: without `ynab.exclude_categories` the eligible set included income, bill and savings categories.

## Second live run: 14 days, several orders

- On 2026-10-09 a `-days 14` run covered 8 delivery orders with 24 ledger entries, the first run with orders paid by several charges.
- Purchase history lists a multi-shipment order once per fulfillment group (13 summaries for 8 orders; one order appeared 3 times). Every group's detail and ledger cover the whole order, so the duplicates fetched and processed the same charges again. `Orders` now returns each order ID once, with its first group.
- First real run: 1 `categorized`, 5 `split_in_place` (all accepted by YNAB), 11 `staged_for_import`, 6 `skipped` (one refund, five too old to pre-stage). An immediate rerun reported all 17 written charges as `already_processed` with no writes: idempotency holds on a live run.
- The YNAB register then showed the bank had posted some of an order's charges as one amount, so 6 staged transactions duplicated two cleared bank transactions ($212.18 and $97.72) and 3 more were at risk. That led to "Charges the bank posts together". Cleanup: John deleted the 9 staged transactions in YNAB and their 9 records were removed from the database (a backup was kept); deleting only one side would leave either the duplicates or charges the tool believes are done.
- After the fix a real run split three bank transactions in place, each paying several charges: $212.18 (4 charges), $97.72 (2) and $48.03 (2), recording 8 charges. The $48.03 pair had shown as "too old to pre-stage" before the fix: the bank transaction was in YNAB all along, just not as a single amount. YNAB accepted every split; no sibling fallback was needed. Read back in the YNAB register: the $212.18 and $97.72 bank transactions are cleared splits with the first member's marker in the memo, no duplicates remain, and the account's uncleared balance ($181.64) is exactly the two staged transactions left on purpose.
- Still open from this run: two staged single-charge orders ($9.84, $171.80) should merge with their bank imports; one 3-charge order waits for the bank, by design; and one older 3-charge order matches nothing even though two of its charges add up to the order total, so it may sit outside the window, in another account, or under a payee the pattern misses.

## Charges the bank posts together

- Seen live on 2026-10-09: Walmart's ledger can list an order as several card charges that the bank then posts as one amount, unpredictably. One order's 7 charges came through as 5 single bank transactions plus one $97.72 transaction paying the other two; another order's 4 charges came through as a single $212.18. The one-to-one matcher found no single amounts, so the tool pre-staged every piece, and YNAB can never merge a staged piece with a combined bank transaction: the pieces became duplicates.
- Matching: after single matches, `matcher.MatchCombined` looks for a transaction equal to the sum of two or more of the order's still-unmatched charges, with the same candidate rules as a single match and the transaction date inside every member's window. Only unambiguous fits count: the transaction fits exactly one subset, that subset fits no other transaction, and no charge is in two fits; anything else is left unmatched. At most 12 charges are searched (every subset is tried).
- Writing: the transaction is split (or categorized) by the categories for the summed amount, exactly like a single match; the memo marker uses the first member's key, and every member charge is recorded with that transaction ID and outcome, so a rerun reports each one as `already_processed`. Each summary row keeps its own charge's share and notes that it was paid together with the others.
- Unmatched members wait for the bank (`skipped`, not recorded, retried next run); since 2026-10-10 no charge is ever pre-staged.

## Splitting an order charged in pieces

- Seen live on 2026-10-09: one grocery delivery ($118.63 of items, tax and a fee, plus a $7.51 driver tip) was charged as seven ledger entries: six pieces summing to exactly $118.63, all at one timestamp, and the tip a day later. The order had a single fulfillment group, the ledger rows carry only an amount and a time, and no consistent tax-rate grouping of the items explains the pieces (every exact partition needs five or more rates). Which items a piece paid for is not knowable from Walmart's data.
- Before: each charge was split on its own by the whole order's category mix, so an $8.39 piece showed $2.40 of camp chairs. Category totals per order were right; each transaction's split was not believable.
- Built behavior (John chose this over keeping the proportional split): `splitter.BuildOrderSplits` gives each category its proportional share of the sum of the order's charges (largest remainder), then fills the charges from those totals, largest charge first and largest category first, ties in input order. Category totals per order stay exact to the cent; most small charges get one category. A charge's split memo is still the item names of its categories, with each item's order-level share. One charge gives exactly what `BuildSplits` gives.
- The plan always covers every charge of the order, recorded or not, so reruns and charges written in different runs agree. It changes only if Walmart's charges or the item categories change between runs; then charges written earlier keep their old split.
- Tip: `order.TipCharge` recognizes the tip charge only when the order has a tip, exactly one charge equals it, and the other charges sum to the item subtotals plus tax and fees. That charge is left out of the fill and split on its own in proportion to the items' categories (John's choice: a tip spread over what was delivered, not a one-category tip that looks like cat food); category totals per order then match a one-shot proportional split to within a cent per category. With `ynab.tip_category` set (optional, an eligible category, checked at startup) it goes to that category with the memo "Driver tip" instead. When the check fails, the tip is filled like any other charge.
- A combined bank transaction ("Charges the bank posts together") gets its members' planned splits merged by category.
- Transactions written before this change keep their proportional splits; re-splitting them is a separate decision.

## Small orders split per item

- Asked for 2026-10-11 so a wrong category is a one-line fix: an order with at most `ynab.itemize_max_items` items (default 10, 0 off) gets one split per item instead of one per category. 4 groceries and 2 misc items give 6 splits.
- The count is the order's Walmart line items, not units: "Bananas x3" is one item and one split.
- Each item's share of the charges is the same largest-remainder allocation by subtotal (tax and fees included), and multi-charge orders are filled largest charge first, largest item first, so an item can straddle two charges and show on both. The memo is the item's name alone; the split amount is its price.
- Items sharing a category stay separate lines, so an all-grocery order of three items is a three-line split. A one-item order is still categorized, not split.
- A separately charged tip stays split by category (or goes to `tip_category`): spreading it over up to ten items would only add lines of cents.
- An order with a negative item (a coupon) falls back to category splits, since a negative amount cannot be its own split.
- A combined bank transaction merges its members' splits per item (`MergeSplits` keys by category and item), so item lines are not folded back into categories.
- Transactions written before this change keep their category splits.

## Fallback shape and approval of tool-created transactions

- Status: superseded 2026-10-10 by "Update only: no transaction creation". Kept for history: the tool no longer creates siblings or pre-staged transactions.
- Was settled 2026-10-09 (were open decisions 3 and 5).
- Fallback: when a charge cannot be split in place (`split_in_place: never`, or a 400 or ignored split), the writer creates a flagged, unapproved sibling split and flags the original (and marks its memo, see "needs_manual_match without the database"). Flag-only was rejected: it would make you type the split by hand. The sibling doubles the outflow until you merge or delete it. This path is rare now that split-in-place is verified to work.
- Approval: transactions the tool creates (siblings and pre-staged charges) are unapproved, so they land in YNAB's review queue. Matched transactions keep their existing approval state.

## Open decisions for John

Current defaults are what the code does today; none of these is implemented differently.

4. Refunds in v1.1: look up which items were returned, or mirror the original order's split proportions. Default: refunds are reported as `skipped` and never changed.
7. Ctrl-C exit code: 1 today; 130 is the shell convention. Default: 1.
9. Whether a dry run should avoid refreshing the local transaction cache. Avoiding it makes a dry run strictly read-only locally but costs a full fetch each time or a stale view. Default: it may refresh the cache.
10. `last-used` and the delta cache. Current: a non-UUID `plan_id` bypasses the cache (one full read per run). Alternative: fingerprint the plan from the categories already fetched in the preflight (for example the ID of the internal "Inflow: Ready to Assign" category) and key the cache by it, restoring the delta for `last-used`. Default: bypass.

## Known limitations and deferred review notes

Behavior or safety items a user or maintainer should know; none blocks the manual acceptance run.

- The transaction cache is used only with an explicit plan UUID; with `last-used` every run makes one full transactions read (see open decision 10).
- Every run fetches every order in the `-days` window from Walmart (order plus ledger, 2s apart) before it can check the database, so a large window costs time and Walmart requests even when nothing is new.
- Order listing is capped at 25 pages of 20 and the cap is hit silently (no warning). Whether Walmart's `MinTimestamp` filter works is unverified; a second client-side filter drops summaries whose delivered date parses and is older than the window.
- A charge that hits a YNAB 429 before any write gets no row; only the "stopped early" block names it. The YNAB transactions are loaded before an order's items are categorized, so a failed load fails the order's pending charges without spending LLM tokens (those rows show no proposed split). A forced rerun of a written order still costs one LLM call.
- One unpriced item makes its whole order fail (safe; to be confirmed against real data). Refunded items are tracked only from the order's items, with no consumer yet.
- `import-curl` reads only the `walmart:` section (no `YNAB_TOKEN` needed, and the rest of the config is not validated), silently replaces an existing cookie store (that is the refresh workflow), and leaves an empty 0700 directory behind when the capture is bad.
- `.gitignore` covers cURL captures named `curl*.txt` or `*.curl`; a capture saved under any other name holds live cookies and would not be ignored.
- `ANTHROPIC_BASE_URL` and `OPENAI_BASE_URL` are honored from the environment (intended for testing): an ambient variable meant for another tool would receive the API key. The LLM clients never follow redirects, so the key is not forwarded to a redirect target.
- Two charges with identical amounts on the same day in the same account cannot be told apart; the matcher pairs them with transactions lowest ID first, so each may get the other's transaction.
- The categorizer's repair retry embeds the model's previous reply and quoted item names in the next prompt (bounded, only after a rejected reply, and output validation still applies).
- yaml type errors can echo the offending raw config text; `${VAR}` expansion has no escape for a literal `${`.
- Still UNVERIFIED against live Walmart (only delivery orders seen so far, 9 of them): which `OrderSummary.Type` values mean in-store, tax/tip/fee placement per fulfillment type, whether refunds or Walmart Cash show in `FinalCharges`, whether purchase history lists newest first (`-max` assumes it), and cookie lifetime.
