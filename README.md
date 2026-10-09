# itemize-ynab

A personal command-line tool that splits Walmart card charges in YNAB by item
category. It reads your Walmart orders (and the card charges Walmart actually
made for them), asks an LLM to put each item in one of your YNAB categories,
finds the matching YNAB transaction, and categorizes or splits it, spreading tax,
fees and tip in proportion to each category's item subtotal. Runs are repeatable:
a charge already processed is guarded against a second write by its record in
the local database and by a marker in the transaction's memo. The known gaps (a
lost database while `needs_manual_match` siblings are unresolved, a write that
times out after YNAB applied it, and `-force`) are described in section 8.

What it deliberately does not do (v1):

- Monarch Money, Costco, Amazon. Walmart and YNAB only.
- Refunds. A refund is detected and reported as `skipped`, never changed.
- Run as a hosted service, web UI or OAuth app. It uses a YNAB Personal Access Token.
- Log in to Walmart for you. You refresh the Walmart cookies by hand (section 3).

Design is based on [eshaffer321/itemize](https://github.com/eshaffer321/itemize)
and the Walmart client is
[eshaffer321/walmart-client-go](https://github.com/eshaffer321/walmart-client-go) v2.
Nothing has been run against a live Walmart session or a real YNAB plan yet; see
`docs/DECISIONS.md` for what is still open.

## 1. Requirements and install

- Go 1.27.2 and golangci-lint 2.14.0, both pinned in `.tool-versions` (asdf). The `go` directive in `go.mod` matches the Go pin.
- A YNAB account with a Personal Access Token, a Walmart account, and an LLM API
  key (Anthropic or OpenAI).

```
make build            # writes bin/itemize-ynab
bin/itemize-ynab version
```

Other targets: `test`, `lint`, `vet`, `cover`, `tidy`, `check` (vet, lint, tests).

## 2. Configuration

Copy the example and edit the copy. `config.local.yaml` is git-ignored.

```
cp config.yaml config.local.yaml
bin/itemize-ynab -config config.local.yaml ynab accounts
```

The `-config` flag defaults to `config.yaml` in the current directory and must
come BEFORE the command. The shipped `config.yaml` is an example with a
placeholder account ID, so using it as-is fails the account check. Its
`category_overrides` example (`"Pet Supplies": "Pets"`) fails the same live check
unless your plan has a category named "Pets": change or remove it.

| Key | Default | Meaning |
|---|---|---|
| `ynab.token` | required | Set to `"${YNAB_TOKEN}"`. Secrets stay in the environment. |
| `ynab.plan_id` | `last-used` | YNAB plan (budget) UUID, or `last-used`. See the note below. |
| `ynab.flag_color` | `purple` | Flag for anything that needs a human. One of red, orange, yellow, green, blue, purple. |
| `ynab.accounts` | none | Map of card last 4 digits (quoted) to a YNAB account ID. Enables pre-staging and restricts matching to that account. |
| `ynab.category_overrides` | none | Map of categorizer output name to an existing YNAB category name, applied after categorization. |
| `ynab.match_window.days_before` | 2 | How many days before the charge date a YNAB transaction may sit. |
| `ynab.match_window.days_after` | 10 | How many days after. |
| `ynab.split_in_place` | `auto` | `auto`, `always` or `never`; see below. |
| `database.path` | `itemize-ynab.db` | SQLite file. A relative path resolves against the config file's directory, not the working directory. |
| `walmart.cookie_file` | `~/.walmart-api/cookies.json` | Walmart cookie store. A leading `~` is the home directory. |

Rules that apply to the whole file:

- `${VAR}` references are expanded from the environment anywhere in a value. An
  unset or empty variable is an error naming the variable.
- Unknown keys inside `ynab`, `database` and `walmart` are rejected, so a typo
  does not silently drop a setting. Unknown top-level sections are ignored.
- Every problem is reported at once, and every override target and mapped
  account is checked against your live YNAB plan at the start of each run.
  Failures exit with code 1.

`plan_id`: there is no command that lists plans. With `last-used`, `default`, or
any other value that is not a UUID, the plan behind the name can change between
runs, so the local cache of YNAB transactions (section 8) is bypassed: every run
makes one full transactions read for its window and nothing is cached. Put the
explicit plan UUID (the one in the app.ynab.com URL while the plan is open;
unverified here) in `plan_id` to enable the cache, after which runs fetch only
the changes since the last run.

`split_in_place`: whether a charge spanning several categories is split on the
matched transaction itself.

- `auto`: try it; after the first rejection in a run, stop trying for the rest
  of that run and use the sibling path. The next run tries again.
- `always`: try it on every charge; a rejected charge still falls back to the sibling path.
- `never`: always use the sibling path (a flagged, user-entered split next to the original).

Whether YNAB accepts an in-place split has not been tested (section 4, `probe-split`).

Find the IDs and names to put in the file:

```
bin/itemize-ynab -config config.local.yaml ynab accounts           # ACCOUNT  ID  TYPE
bin/itemize-ynab -config config.local.yaml ynab categories         # CATEGORY GROUP ID
bin/itemize-ynab -config config.local.yaml ynab categories -eligible   # only what the LLM may use
```

Hidden and closed entries are marked. Eligible categories exclude hidden and
deleted ones, YNAB's internal categories and the Credit Card Payments group. A
category name that exists in more than one group is ambiguous: an override target like that is rejected, and a charge the model assigns to it fails.

### Environment variables

| Variable | Meaning |
|---|---|
| `YNAB_TOKEN` | YNAB Personal Access Token, referenced from the config. |
| `CATEGORIZER_PROVIDER` | `anthropic` or `openai` to force a backend (case-insensitive). Unset: auto-detect from the keys. Anything else exits 1. |
| `ANTHROPIC_API_KEY` (alias `CLAUDE_API_KEY`) | Anthropic key. |
| `ANTHROPIC_MODEL` | Anthropic model. Default `claude-haiku-5-5`, which has not been verified against the live API. Set it explicitly if the default is rejected. |
| `OPENAI_API_KEY` (alias `OPENAI_APIKEY`) | OpenAI key. |
| `OPENAI_MODEL` | OpenAI model. Required when OpenAI is used; there is no default. |

With both an Anthropic and an OpenAI key set and `CATEGORIZER_PROVIDER` unset,
Anthropic is used.

`ANTHROPIC_BASE_URL` and `OPENAI_BASE_URL` are honored when set in the
environment; they are intended for testing. An ambient variable meant for
another tool would receive your API key, so unset them before running. The LLM
clients never follow an HTTP redirect, so the key is not forwarded to a
redirect target.

Create a YNAB token in the YNAB web app: Account Settings, Developer Settings,
New Token. YNAB limits each token to 200 requests per rolling hour. The LLM
provider receives item names and the list of your category names; it does not
receive amounts, card numbers or account IDs.

## 3. Cookies are the only Walmart credential

There is no Walmart API. The tool replays a browser session, so it needs a
captured request from your own logged-in browser.

Create or refresh the cookie store. The numbered browser steps come from the
client library's docs and are unverified here:

1. Log in to walmart.com in Chrome or Firefox and open `https://www.walmart.com/orders`.
2. Click "View details" on any order.
3. Open DevTools (Cmd+Option+I or F12), Network tab, and refresh the page.
4. In the Network filter type `getOrder`. Right-click that request, Copy, Copy as cURL.
5. Import it, from the clipboard on macOS (nothing is written to disk):

   ```
   pbpaste | bin/itemize-ynab -config config.local.yaml walmart import-curl -
   ```

   or from a file (`import-curl <file>`). If you saved a file, delete it
   afterwards: it holds live session cookies. Only the name `curl.txt` is git-ignored.

The command prints `cookie store written to <path>` and makes no network call.

- The capture must contain the `CID`, `SPID` and `auth` cookies; otherwise it
  fails with a message naming the missing one. Walmart's other cookies and a
  few request headers (including the `getOrder` query hash) are stored too.
- `import-curl` loads the whole config, so `YNAB_TOKEN` must be set even though
  the token is not used. Any non-empty value works for this one command.
- Running it again REPLACES the existing store. That is the refresh workflow:
  there is no separate refresh command.
- The file is created with permission 0600 and its directory 0700. Never commit
  it. The tool rewrites the file during runs as Walmart refreshes cookies, so it
  must stay writable.
- The request name and the cookie lifetime come from the client library's docs; unverified live.

A stale session or bot challenge shows up as a stopped run: the summary ends
with a `STOPPED EARLY: Walmart blocked: stale session` (or `bot challenge`,
`rate limited`) block that tells you to refresh the cookies, stderr says
`error: stopped early: ...`, and the exit code is 3. Charges finished before
the stop keep their results. Refresh the cookies and run again.

## 4. Usage

```
itemize-ynab [-config path] <command>
```

| Command | What it does |
|---|---|
| `version` | Print the version (`itemize-ynab dev` for a local build). |
| `ynab accounts` | List accounts: name, ID, type. |
| `ynab categories [-eligible]` | List categories: name, group, ID. |
| `ynab probe-split [-yes] <txn-id>` | Try an in-place split on ONE real transaction. Without `-yes` it only shows what it would do and exits 1. |
| `walmart import-curl <file\|->` | Write the cookie store from a cURL capture (section 3). Takes no flags. |
| `walmart [-dry-run] [-days N] [-max N] [-force] [-verbose]` | The main run. |

`walmart` flags:

| Flag | Default | Meaning |
|---|---|---|
| `-days N` | 14 | Look at orders from the last N days. Must be at least 1. |
| `-max N` | 0 (no limit) | Process at most N orders, taking the newest first (assumes Walmart lists newest first; unverified live). Counts orders, not charges. |
| `-dry-run` | off | Decide and print; see below. |
| `-force` | off | Reprocess charges already recorded; see below. |
| `-verbose` | off | Debug logs, and the proposed split (category and amount) under each row. |

Logs go to stderr; the summary goes to stdout. A usage mistake prints the usage text and exits 1.

### Recommended first run

Run these in order, each with the binary and your config file. Set up
scheduling (section 7) only after the whole sequence succeeded.

1. Pick a throwaway, non-split, non-transfer transaction and run the probe
   WITHOUT `-yes` first:

   ```
   bin/itemize-ynab -config config.local.yaml ynab probe-split <txn-id>
   ```

   It reads the transaction and your categories, prints what it would do and
   refuses (exit 1) without writing. Then run it with `-yes`, which performs a
   real write on that throwaway transaction:

   ```
   bin/itemize-ynab -config config.local.yaml ynab probe-split -yes <txn-id>
   ```

   It splits the transaction in two, reports the verdict (`split-in-place
   WORKS`, `REJECTED (400)` or `IGNORED (200 but no subtransactions saved)`) and
   tries to revert. If the split works, YNAB may not allow reverting it through
   the API; the command says so and you fix that one transaction by hand in the
   app. Record the verdict in `docs/DECISIONS.md`.
2. Compare the proposed splits with 3 real orders:

   ```
   bin/itemize-ynab -config config.local.yaml walmart -dry-run -days 30 -verbose
   ```

3. Write one order, then inspect that transaction in the YNAB app:

   ```
   bin/itemize-ynab -config config.local.yaml walmart -max 1
   ```

4. Run the same command again and confirm nothing changes (`already_processed`).

### Dry run

A dry run makes no YNAB writes and records nothing in the database, and the
summary is labelled `DRY RUN`. It still reads Walmart, still calls the LLM (it
spends tokens, and needs the key), still reads YNAB, and may refresh the local
cache of YNAB transactions (section 8). It also creates the database file if it
does not exist yet. In a dry run `auto` mode always reports `split_in_place`
with a note, because acceptance cannot be known without writing.

### `-force`

`-force` skips the database check for charges already recorded, so they are
processed again. It does not turn off the memo-marker protection: a transaction
already carrying `[itemize:...]` is invisible to matching, so a forced rerun of a
charge that was categorized, split or staged normally ends `skipped`. A charge
recorded as `needs_manual_match` is refused outright while its database row
exists (`skipped`, with a note naming the existing sibling), because a rerun
would create a second sibling. Resolve or delete the sibling in YNAB first, and
because the refusal depends on the database row, remove its `ynab_charges` row
too before reprocessing.

## 5. Outcomes

Each card charge gets one row. An order paid by two cards, or charged twice,
produces two rows.

| Outcome | Meaning |
|---|---|
| `categorized` | The matched transaction had items in one category: its category was set and the marker appended to its memo (existing memo text is kept). |
| `split_in_place` | The matched transaction was turned into a split and YNAB's reply was checked to hold exactly that split. |
| `needs_manual_match` | A flagged, unapproved, user-entered split (payee Walmart, same account, date and amount, marker memo) was created next to the matched transaction, and the original was flagged. Until you resolve it the outflow appears twice. You merge or delete by hand in YNAB; the tool never does. Reached with `split_in_place: never`, or after a rejected in-place split. |
| `staged_for_import` | No matching transaction yet, so an unapproved user-entered transaction was created in the mapped account, to be merged when the bank import arrives. |
| `skipped` | Nothing written, nothing recorded, so the charge is retried next run. See reasons below. |
| `already_processed` | The database already has this charge. Nothing written, no LLM call. |
| `failed` | The charge or order could not be processed. Any failed row makes the exit code 2. |

`skipped` reasons you will see in the NOTE column:

- A ledger entry that is not a card charge: `gift card`, `non-card payment`,
  `refund`, `zero amount`. Also `order has no card charges`.
- `ambiguous match: candidates ... tie across accounts`.
- `no matching transaction; not pre-staged: <reason>`, where the reason is: no
  account mapping for the card; no usable date for the charge; charge date is in
  the future; charge is too old to pre-stage (10 days or more, fixed); charge
  date window starts before the loaded transaction range (widen `-days` or
  re-run); or a transaction with the same amount already exists in the account.
- `not forced: sibling split ... already exists` (a refused `-force`).

`failed` reasons: an order that could not be fetched, a categorization failure
(LLM error, invalid reply after one repair attempt, order without items), a
category with no YNAB match, or a YNAB write error. A failure does not end the run; only a stop (exit 3) does.

The idempotency marker is `[itemize:walmart:<orderID>:<amountCents>:<n>]`,
appended to the memo, where `<n>` counts same-amount charges within the order.
A YNAB transaction carrying any `[itemize:` text is never matched again. Removing
the marker by hand makes it eligible again.

A matched transaction must have exactly the charge amount, a payee or import
payee matching Walmart, Wal-Mart, Wal Mart, WM Supercenter or WMT (case-insensitive),
a date within the match window of the charge date (the ledger's per-charge date,
or the order date when the ledger has none), and, if the card is mapped, the
configured account. Transfers, split transactions and deleted transactions never match.

Two charges with identical amounts on the same day in the same account (for
example two orders with the same total) cannot be told apart: each takes a
different transaction, the lowest transaction ID first, so they may be paired
with each other's transaction. The amounts and categories are then still right
only if the two orders split the same way; check such pairs by hand.

Sample output (normal run, from the test suite):

```
ORDER  AMOUNT   OUTCOME            YNAB TXN  NOTE
A-100  $5.00    categorized        tA
B-200  $73.50   split_in_place     tB1
C-300  -$12.99  skipped            -         refund: returned item
D-400  $185.83  failed             -         no category for "Towels"
E-500  $1.00    already_processed  tE        recorded as categorized

Totals: 1 categorized, 1 split_in_place, 1 skipped, 1 already_processed, 1 failed (5 total)
```

A dry run starts with `DRY RUN — no YNAB writes were made`, labels writing
outcomes `categorized (planned)` and so on, and ends `Totals (DRY RUN): ...`.

## 6. Exit codes

| Code | Meaning | What to do |
|---|---|---|
| 0 | Finished; no row failed. `skipped` rows are not failures. | Nothing. |
| 1 | Config, auth or usage error, a missing or empty cookie store, no LLM key, a fatal error before or during the run, or Ctrl-C. | Read the `error:` line on stderr. |
| 2 | The run finished but at least one row is `failed`. | Read the `failed` rows; rerun after fixing the cause. Completed charges are not redone. |
| 3 | Stopped early by a YNAB 429 or a Walmart block (stale session, bot challenge, rate limit). | Walmart: refresh the cookies (section 3). YNAB: wait, the limit is 200 requests per rolling hour. Then rerun. |

If both a failure and a stop occur, the code is 3.

## 7. Scheduling

Both examples are untested templates. Use absolute paths everywhere (cron and
launchd start with a nearly empty environment), put `-config` before `walmart`,
and use a small `-days` (7 or so): every run fetches every order in the window
from Walmart before it can tell which are already done.

Keep secrets out of the crontab and the plist: put them in a 0600 env file and
source it from a wrapper script.

```
mkdir -p ~/.config/itemize-ynab ~/Library/Logs/itemize-ynab
# first time only: never truncates an existing env file
[ -e ~/.config/itemize-ynab/env ] || install -m 600 /dev/null ~/.config/itemize-ynab/env
# then add NAME=value lines to it
```

`~/.config/itemize-ynab/run.sh` (`chmod 700`):

```sh
#!/bin/sh
set -eu
set -a
. "$HOME/.config/itemize-ynab/env"
set +a
exec /Users/YOU/bin/itemize-ynab \
  -config /Users/YOU/.config/itemize-ynab/config.local.yaml \
  walmart -days 7
```

Cron (daily 07:15; output appended to a log):

```
15 7 * * * /Users/YOU/.config/itemize-ynab/run.sh >> /Users/YOU/Library/Logs/itemize-ynab/run.log 2>&1
```

launchd, `~/Library/LaunchAgents/com.example.itemize-ynab.plist`, then
`launchctl load` that file:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.example.itemize-ynab</string>
  <key>ProgramArguments</key>
  <array><string>/Users/YOU/.config/itemize-ynab/run.sh</string></array>
  <key>StartCalendarInterval</key>
  <dict><key>Hour</key><integer>7</integer><key>Minute</key><integer>15</integer></dict>
  <key>StandardOutPath</key><string>/Users/YOU/Library/Logs/itemize-ynab/out.log</string>
  <key>StandardErrorPath</key><string>/Users/YOU/Library/Logs/itemize-ynab/err.log</string>
</dict>
</plist>
```

The summary lands in the stdout log and the slog lines in the stderr log (with
cron's `2>&1` they share one file). Exit 3 with a `Walmart blocked` reason is
the signal that the cookies need refreshing; neither scheduler notifies you, so
check the log or extend the wrapper. A cookie import cannot be scheduled: it
needs a fresh browser capture.

Runs are serialized by a lock file next to the database
(`<database.path>.lock`). A `walmart` run (dry runs included) that overlaps
another one does not wait: it exits 1 with `another itemize-ynab run is in
progress (lock: <path>)` before any network call. The lock is released when the
run ends, and the operating system drops it if the process dies, so there is no
stale lock to delete. `import-curl` and the `ynab` commands do not take it. The
lock uses `flock`, so it needs macOS, Linux or a BSD; on other platforms
`walmart` refuses to run.

## 8. Safety model and data

What is written to YNAB: a category and memo on matched transactions; a split
(subtransactions) on matched transactions; new unapproved user-entered
transactions (siblings and staged ones); and a flag on originals of siblings. It
never deletes a transaction and never approves one. No request leaves with a
split that does not sum exactly to its parent; the check is local, before sending.

Idempotency has two layers: the SQLite record of each processed charge, and the
`[itemize:...]` marker in the memo. A charge is recorded only after the YNAB
write succeeded. If recording fails, the marker still prevents a duplicate for a
categorized, split or staged charge. A sibling is the exception: the flagged
original carries no marker, so the row's note says the sibling exists, that a
rerun will create a second sibling, and that one must be deleted by hand.

Known gaps:

- A lost database while `needs_manual_match` siblings are unresolved (below).
- Ambiguous write timeouts: the YNAB client gives up after 30 seconds, but a
  POST or PUT can time out after YNAB applied it. The charge is then reported
  `failed` and not recorded; a rerun skips a marked transaction, but a sibling
  POST that timed out after being applied is unrecorded and a rerun creates a
  second one. Check `failed` rows that mention a timeout in YNAB before rerunning.
- `-force` (section 4).

- Database: `database.path` (default `itemize-ynab.db` beside the config file; `*.db` is git-ignored).
  Tables: `ynab_charges` (the idempotency record), `server_knowledge`,
  `ynab_txn_cache`, `ynab_txn_cache_meta`. It runs in WAL mode, so `-wal` and
  `-shm` files appear next to it. Back it up with
  `sqlite3 itemize-ynab.db ".backup backup.db"` or by copying all three files
  while no run is active.
- Deleting the database is mostly safe: markers keep categorized, split and
  staged charges from being written again, and the transaction cache is rebuilt
  with one full YNAB fetch. The exception is `needs_manual_match`: the flagged
  original carries no marker, so a rerun matches it again and creates a second
  sibling and flag. Do not delete the database while unresolved siblings exist.
- Rate limits: YNAB allows 200 requests per hour per token. A run typically
  makes 3 YNAB reads (categories; accounts when accounts are mapped; one
  transactions list) plus typically 1 or 2 writes per charge (a sibling is 2:
  the sibling POST and the flag PUT). A rejected in-place split costs 3 to 4
  writes (the split PUT, a memo restore PUT when YNAB ignored the split, the
  sibling POST, the flag PUT), and a failed delta fetch costs one extra full
  transactions read. The YNAB client never retries or sleeps. Walmart requests
  are spaced 2 seconds apart, and the Walmart client retries a ledger 429 up to
  3 times with 5s, 10s, 20s backoff.
- YNAB transaction cache: only with an explicit plan UUID in `plan_id`. The
  first run fetches transactions since the start of the window; later runs fetch
  only changes and merge them into the cache. With `last-used` (or any non-UUID
  `plan_id`) the cache is bypassed and every run makes one full read.
- Window edge: transactions are loaded from `days_before + 1` days before the
  `-days` window. A charge whose own match window starts earlier is `skipped`
  rather than pre-staged, because a same-amount transaction could exist unseen.
- Overlapping runs: serialized by the run lock (section 7).
- Identical charges: see the tie note in section 5.
- Secrets: the YNAB token, Walmart cookies and LLM keys are redacted from logs,
  errors and output. They are never committed; the config holds only `${VAR}`.

## 9. Development

```
cmd/itemize-ynab/          the binary (cmd/spike-walmart: throwaway live spike, never run by tests)
internal/domain/           order, categorizer, allocator, splitter, matcher, memo (pure rules)
internal/application/sync/ orchestrator, writer, transaction source, summary model
internal/adapters/         walmart, ynab (+ ynabtest fake server), llm (anthropic, openai, llmhttp)
internal/infrastructure/   config, storage (SQLite, migrations), lock (run lock)
internal/cli/              flags, wiring, summary printing, exit codes
docs/                      PRD, ARCHITECTURE, DECISIONS
```

`make check` (vet, lint, `go test ./... -race`) must be clean. Tests use `httptest`
and fakes only: never a real YNAB plan, never live Walmart or LLM calls. See
`AGENTS.md` for the conventions (TDD, money as integer cents or milliunits,
slog levels, commit format).

Credit: the design is based on eshaffer321/itemize, which has no license, so no
code was copied (clean reimplementation). The Walmart data layer is
eshaffer321/walmart-client-go v2.
