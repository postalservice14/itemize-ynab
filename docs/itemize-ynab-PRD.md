# PRD: itemize-ynab

A fork of [eshaffer321/itemize](https://github.com/eshaffer321/itemize) that splits Walmart purchases into budget categories in **YNAB** instead of Monarch Money.

---

## 1. Instructions for Claude Code

Read this whole document before writing code. Then:

1. Read the upstream repo's `CLAUDE.md`, `AGENTS.md`, `README.md`, and everything under `internal/` and `cmd/itemize/`. Before making changes, write a short summary of the existing architecture: provider interface, categorizer interface, Monarch client, storage layer, and sync loop.
2. Map each requirement below onto the existing structure. Reuse the Walmart provider, the LLM categorizer, the SQLite storage, and the CLI scaffolding wherever possible. **This is a destination swap, not a rewrite.**
3. Work in the milestone order in §10. Each milestone ends with passing `go test ./...`, passing `golangci-lint run`, and a commit.
4. A sketch package (`internal/ynab`: `client.go`, `allocate.go`, `match.go`, `writer.go`, `store.go`, `walmart_adapter.go`, `ynab_test.go`) may be supplied with this PRD. **Treat it as a reference design, not finished code.** It has never been compiled. Adapt it to upstream's interfaces, fix it, and test it.
5. Where this PRD conflicts with upstream conventions (logging, config loading, error style), follow upstream. Where it conflicts with YNAB API behavior you observe, follow the API and note the deviation in `docs/DECISIONS.md`.
6. Never make write calls against a real YNAB plan in tests. Use `httptest` servers.

---

## 2. Problem

YNAB imports a Walmart purchase as one transaction, such as "Walmart −$185.83". A single order usually spans groceries, household goods, personal care, and more. Splitting each one by hand means opening the order on walmart.com and doing arithmetic. In practice, most of these purchases end up lumped into one category, so category spending is wrong.

Upstream itemize already solves this for Monarch Money. It fetches itemized orders, categorizes items with an LLM, and splits the matching transaction. This fork does the same for YNAB.

## 3. Goals

- **G1.** For each Walmart card charge, find the matching YNAB transaction and split it by category. Tax, fees, and tip are spread proportionally across categories.
- **G2.** Handle Walmart's real charge behavior. One order can produce several card charges (substitutions, partial fulfillment, tips), and part of an order may be paid with Walmart Cash or a gift card.
- **G3.** Runs are safe to repeat. Running twice, or after a crash, never creates duplicate splits or transactions.
- **G4.** A dry run shows exactly what would change, and makes no YNAB write calls.
- **G5.** The tool can run unattended on a schedule (cron or launchd) and exits cleanly when YNAB's rate limit or Walmart's session expiry stops it.

## 4. Non-goals (v1)

- Monarch support. Remove it rather than maintaining two destinations.
- Costco and Amazon. Keep the provider abstraction intact so they can be added later, but v1 only has to work for Walmart.
- Refunds and returns. Detect them, report them, and leave them unchanged.
- Any hosted service, web UI, or OAuth app. This is a personal CLI using a YNAB Personal Access Token.
- Automating Walmart login. The user refreshes cookies by hand, as upstream does today.

## 5. Background and constraints

### 5.1 Walmart (via walmart-client-go)

- There is no official consumer API. The client replays a browser session (cookies captured from a copied cURL request) against Walmart's internal GraphQL persisted queries: `PurchaseHistoryV2` and `getOrder`.
- `GetOrderLedger(orderID)` returns the **actual card charges**, which can differ from the order total. One order can produce several charges, and Walmart Cash or gift card portions never reach the bank. **Matching must use ledger charges, not the order total.**
- The ledger does not provide a date for each charge, so the order date stands in for it. Pickup and delivery orders are charged at fulfillment, often 1–5 days after the order.
- Sessions expire, and bot challenges return `ErrBotChallenge`. The tool has to surface this clearly.

### 5.2 YNAB API

- Base URL: `https://api.ynab.com/v1`. Use the `/plans/{plan_id}` paths; `/budgets/` is the legacy form. `plan_id` may be `last-used`.
- Authenticate with `Authorization: Bearer <Personal Access Token>`.
- **Rate limit: 200 requests per rolling hour per token.** Exceeding it returns 429.
- Amounts are in **milliunits** (USD cents × 10). Outflows are negative.
- `GET /plans/{id}/transactions` supports `since_date` and delta requests via `last_knowledge_of_server` / `server_knowledge`. As of API v1.85, `since_date` defaults to one year ago.
- **Split limitations.** Updating subtransactions on a transaction that is *already* a split is not supported. It is unclear whether a *non-split* transaction can be converted into a split by sending `category_id: null` plus `subtransactions` on `PUT /transactions/{id}`; community tools report that it can't. Determine this empirically (see §6.5) and support both outcomes.
- Import matching: when a bank import arrives, YNAB matches it to an existing *user-entered* transaction in the same account with the same amount and a date within ±10 days. This makes it possible to create a split *before* the bank import arrives and let YNAB merge them.
- Subtransaction amounts must sum exactly to the parent amount. Otherwise the request fails with a 400.

## 6. Functional requirements

### 6.1 CLI

Keep upstream's command shape:

```
itemize-ynab walmart [-dry-run] [-days N] [-max N] [-force] [-verbose]
itemize-ynab ynab categories        # list YNAB categories (name, group, id)
itemize-ynab ynab accounts          # list YNAB accounts (name, id)
itemize-ynab ynab probe-split <txn-id>   # run the split-in-place experiment (§6.5) on one transaction
```

- `-days` (default 14): how far back to look for Walmart orders.
- `-max` (default 0, meaning no limit): maximum number of orders to process.
- `-force`: reprocess charges that are already in the store.
- Exit codes: `0` success; `1` a configuration or auth error; `2` the run finished with some charges failed; `3` stopped early because of the rate limit or a Walmart bot challenge.

### 6.2 Configuration

Use upstream's `config.yaml` plus environment variable loading. Remove the Monarch keys and add:

```yaml
ynab:
  token: "${YNAB_TOKEN}"
  plan_id: "last-used"
  flag_color: "purple"          # used for anything that needs a human
  accounts:                     # card last 4 -> YNAB account ID (enables pre-staging, §6.5)
    "0953": "<account-uuid>"
  category_overrides:           # categorizer output name -> YNAB category name
    "Pet Supplies": "Pets"
  match_window:
    days_before: 2
    days_after: 10
  split_in_place: auto          # auto | always | never
```

When the config loads, validate that every override target and every mapped account exists in YNAB. If one doesn't, fail with a message listing the bad entries.

### 6.3 Categorization

- Give the LLM categorizer the **YNAB plan's category names**, excluding hidden, deleted, and internal categories and the credit card payment groups. Upstream gives it Monarch's categories; change that. This makes the name→ID mapping mostly a straight lookup.
- Apply `category_overrides` after categorization.
- If the result is a category name with no YNAB ID, the charge **fails**, and the report names the unmapped category. Never fall back to a default category without saying so.
- Cache category IDs for the run. One `GET /categories` per run.

### 6.4 Matching

For each ledger charge, a YNAB transaction is a candidate only if **all** of these hold:

- It is not deleted, not a transfer, and not already a split.
- It doesn't carry an itemize memo marker (§6.6) and hasn't already been claimed by another charge in this run.
- Its amount equals `−charge_cents × 10` exactly.
- Its payee name or original import payee matches `(?i)wal[- ]?mart|wm supercenter|\bwmt\b`.
- Its date falls between `order_date − days_before` and `order_date + days_after`.
- If the card's last 4 digits map to an account, it is in that account.

Choosing among candidates: pick the one with the closest date. If several tie and they're all in the same account, pick the lowest transaction ID; they're interchangeable. If several tie across different accounts, the result is **Ambiguous**: skip the charge and report it.

When one order has two identical charges, each charge must claim a different transaction.

### 6.5 Writing to YNAB

For each charge, split the **charge amount** (not the order total) across categories, in proportion to each category's item subtotal. Use largest-remainder rounding so the parts sum exactly. Merge categories that map to the same YNAB category ID. Sort splits from largest to smallest. Each split's memo lists its item names, truncated to 200 characters (rune-safe).

Outcomes:

| Situation | Action | Outcome |
|---|---|---|
| Matched; all items fall in one category | `PUT` with `category_id` and memo | `categorized` |
| Matched; multiple categories; split-in-place allowed | `PUT` with `category_id: null`, `subtransactions`, and memo. **Check the response** to confirm the subtransactions were saved. | `split_in_place` |
| Split-in-place rejected (400) or silently ignored | Undo any memo change. Create a user-entered split transaction in the same account with the same date and amount, payee "Walmart", the marker memo, and the flag. Also flag the original. Set `split_in_place` to off for the rest of the run. | `needs_manual_match` |
| No match; the card maps to an account; order is under 10 days old; no transaction in that account has this amount within the window (any payee) | Create a user-entered split (or a single-category transaction). YNAB merges it when the bank import arrives. | `staged_for_import` |
| No match otherwise | Do nothing. Don't record the charge, so it retries next run. | `skipped` |
| Ambiguous match, refund, or Walmart Cash / gift card portion | Do nothing. Report it. | `skipped` |

`ynab probe-split <txn-id>` runs the split-in-place attempt on one transaction the user chooses. It prints the request and response, reverts the change when YNAB allows it, and states clearly whether split-in-place works. Record the result in `docs/DECISIONS.md`.

### 6.6 Idempotency

- Charge key: `walmart:<orderID>:<amountCents>:<occurrence>`, where occurrence is the nth charge of that amount within the order. Don't use the charge's position in the ledger; new charges added to the ledger later would renumber everything.
- Record finished outcomes (`categorized`, `split_in_place`, `needs_manual_match`, `staged_for_import`) in the existing SQLite database, in a new table: `ynab_charges(key PK, order_id, ynab_txn_id, outcome, created_at)`.
- Also write the marker `[itemize:<key>]` into the parent transaction's memo, appended to any existing memo. This is the second line of defense if the database is lost: the matcher never selects a marked transaction.
- Record the charge only after the YNAB write succeeds. If the write succeeded but recording failed, log a warning and carry on; the memo marker still prevents a duplicate.
- If a sibling split was created but flagging the original failed, still record the charge, so a rerun doesn't create a second sibling.

### 6.7 Rate limiting and efficiency

- Budget per run: 1 call for categories, 1 for transactions (a delta request when a saved `server_knowledge` exists), then 1–2 writes per charge.
- Store `server_knowledge` in SQLite between runs.
- On 429, stop processing, print a summary of what finished, and exit with code `3`. Don't sleep and retry inside the run.
- Keep upstream's 2-second delay between Walmart requests.

### 6.8 Output

At the end, print a summary table with one row per charge: order display ID, charge amount, outcome, YNAB transaction ID, and a note. Then print totals by outcome. With `-verbose`, also show the proposed splits (category → amount) for every charge. Dry run prints the same output, labeled `DRY RUN`.

### 6.9 Telemetry

**Remove the Sentry telemetry entirely.** It reports to the upstream author's project, which isn't appropriate for a personal fork. Delete the dependency and the code and docs that use it.

## 7. Non-functional requirements

- Go version: whatever upstream's `go.mod` specifies. Don't add new third-party dependencies for the YNAB client; use `net/http` and `encoding/json`.
- Secrets (the YNAB token, Walmart cookies, LLM keys) never appear in logs, errors, or dry-run output.
- Keep upstream's pre-commit hooks, lint config, and test layout.
- Every package has unit tests. Rounding and matching get table-driven tests covering the edge cases in §8.

## 8. Test plan

Unit tests:

- **Allocation:** parts sum exactly in every case, including total = 1 cent, many tiny categories, a zero-weight category, and an all-zero fallback.
- **BuildSplits:** categories that map to the same YNAB ID are merged; signs and milliunit conversion are correct; an unmapped category returns an error.
- **Matching:** the closest date wins; wrong payee, out-of-window, already-split, and marked transactions are excluded; duplicate charge amounts claim different transactions; a cross-account tie is Ambiguous; filtering by mapped account works.
- **Ledger adapter:** Walmart Cash / gift card portions are excluded; occurrence numbering for repeated amounts; float→cents rounding (for example 4.12, 178.96, 0.1 + 0.2).

Integration tests against an `httptest` server that imitates YNAB:

- Split-in-place accepted → `split_in_place`.
- Split-in-place returns 400 → sibling fallback; later charges in the same run skip straight to the sibling path.
- Split-in-place returns 200 but with no subtransactions → memo restored, then the sibling fallback.
- No match, eligible for pre-staging → a correct create payload.
- 429 in the middle of a run → partial results and exit code `3`.
- Rerun after success → every charge skipped, and zero write calls.
- Dry run → zero write calls.

Manual acceptance, against a real plan:

1. Run `ynab probe-split` on a throwaway transaction and record the result.
2. Run `walmart -dry-run -days 30` and confirm the proposed splits against 3 real orders.
3. Run without dry-run on `-max 1`, then check the transaction in the YNAB app.
4. Run the same command again and confirm no changes.

## 9. Acceptance criteria

- A real Walmart delivery order with a tip and two card charges produces two correctly split YNAB transactions whose amounts match the bank to the cent.
- A pickup order placed today, before the bank import arrives, is pre-staged. After the import arrives, YNAB shows a single split transaction with no manual step.
- Running the tool twice in a row makes no write calls the second time.
- No request ever leaves YNAB with a split that doesn't sum exactly to its parent.
- `go test ./...` and `golangci-lint run` pass.

## 10. Milestones

1. **Recon.** Write the architecture summary (§1, step 1) and a plan mapping it to this PRD. No code changes.
2. **Rip and rename.** Remove the Monarch client and Sentry. Rename the module and binary to `itemize-ynab`. Get the build green, with temporarily stubbed destination code.
3. **YNAB client and utilities.** Client, the `categories`/`accounts` commands, and `probe-split`. Run the probe manually and record the result.
4. **Core logic.** Allocation, split building, ledger adapter, matching. Unit tests only.
5. **Writer and store.** The outcome logic in §6.5, the SQLite table, delta-request knowledge stored between runs, and integration tests.
6. **Wire into the sync loop.** Categorizer fed with YNAB category names, overrides, summary output, exit codes.
7. **Docs.** Rewrite the README for YNAB setup (token, plan, account mapping, cron example). Add `docs/DECISIONS.md`.

## 11. Open questions

- Does split-in-place work on a non-split imported transaction? `probe-split` answers this, and the result decides whether the sibling fallback ever runs in practice.
- Should `needs_manual_match` siblings be created automatically, or should the tool just flag the original and put the proposed split in the memo? The sibling temporarily doubles the outflow in the account. The default here is to create the sibling; revisit after real use.
- Should refunds be handled in v1.1 by looking up which items were returned, or by mirroring the original order's split proportions?
- Should transactions this tool creates be approved or unapproved? The default is unapproved, so they show up in YNAB's review queue.
