# Architecture notes

## Layers

CLI -> application -> domain <- adapters -> infrastructure (see `AGENTS.md`).

```
cmd/itemize-ynab/            main: signal handling, exit code
internal/cli/                flags, wiring, summary printing, exit codes
internal/application/sync/   orchestrator, writer, transaction source, summary model
internal/domain/             order (types + OrderProvider port), categorizer, allocator,
                             splitter, matcher, memo: pure, no I/O
internal/adapters/walmart/   OrderProvider over walmart-client-go (float -> cents, error classes)
internal/adapters/ynab/      YNAB HTTP client (+ ynabtest fake server)
internal/adapters/llm/       anthropic, openai, llmhttp, backend selection from env
internal/infrastructure/     config (YAML, ${VAR}, validation), storage (SQLite + goose),
                             lock (run lock: flock on <database.path>.lock)
```

Infrastructure imports neither adapters nor domain.

The domain defines the ports (`order.OrderProvider`, `categorizer.ChatClient`);
the application defines the narrow interfaces it needs from YNAB and storage;
adapters and infrastructure satisfy them, and `internal/cli/wire.go` is the only
place real implementations are assembled.

### Run flow (`walmart` command)

1. Load the config (no network). Take the run lock (`<database.path>.lock`,
   exclusive and non-blocking; a held lock exits 1 with "another itemize-ynab
   run is in progress"); it is held until the run ends. Build the LLM client,
   open the SQLite store (migrations run), and build the Walmart client; each
   failure is exit 1 before any request is made.
2. Preflight: fetch YNAB categories, and accounts when `ynab.accounts` is set;
   cross-check overrides and account IDs against them.
3. List Walmart orders since `now - days` (newest first, capped by `-max`), then
   fetch each order and its ledger, 2s apart.
4. Per order: report skipped ledger entries (gift card, refund, ...); for each
   card charge check the store (with `-force` a recorded charge still needs
   work, and the writer decides).
5. If any charge needs work, load YNAB transactions (once per run, before the
   first categorization, so a failed load spends no LLM tokens): one list call
   from `days_before + 1` days before the window. With a UUID `plan_id` it is a
   delta merged into the local cache when possible; with `last-used` or any
   other non-UUID it is a full read and the cache is not touched. Then
   categorize the order's items once through the LLM and split the CHARGE
   amount across categories.
6. Writer per charge: match, then categorize, split in place, create a flagged
   sibling, pre-stage, or skip; record the finished outcome in SQLite after the
   YNAB write succeeded.
7. Print the summary (stdout; logs go to stderr). A YNAB 429 or a Walmart block
   stops the run early with the partial summary.

Why a local transaction cache exists, and the other deviations from the PRD,
are in `DECISIONS.md`.

## Walmart data source: walmart-client-go v2.2.1

All references are to the module source at
`github.com/eshaffer321/walmart-client-go/v2@v2.2.1` (call it `$MOD`; find it with
`go list -m -f '{{.Dir}}' github.com/eshaffer321/walmart-client-go/v2`).
Everything below was read from source. Anything that depends on what Walmart
actually returns is marked UNVERIFIED until `go run ./cmd/spike-walmart` is run
by John against a live session.

### Construction and config (`$MOD/config.go`, `$MOD/client.go`)

`walmart.NewWalmartClient(walmart.ClientConfig) (*WalmartClient, error)` (`client.go:37`).

| Field | Default | Notes |
|---|---|---|
| `CookieFile` | `~/.walmart-api/cookies.json` | `CookieDir` also accepted; dir is created 0750 (`client.go:39-51`) |
| `RateLimit` | 2s | ticker between purchase-history / order calls (`client.go:53`) |
| `LedgerRateLimit` | = `RateLimit` | doc recommends 10-30s for the ledger endpoint (`config.go:18-21`) |
| `MaxRetries` | 3 | 0 means 3; **-1 disables** (`config.go:23-26`, `client.go:63-67`); only used for 429 in the ledger call |
| `AutoSave` | false | declared but not read by the constructor |
| `Logger` | discard | nil gives a no-op slog logger (`client.go:71-75`) |

HTTP timeout is 30s and redirects are not followed (`client.go:95-100`).
Calls take a `context.Context`; the rate-limit wait honors cancellation.
The rate limiter is skipped for the first call of each kind (`lastRequest.IsZero()`),
so the first call is immediate.

### Cookies (`$MOD/client.go`, `$MOD/internal/cookies/store.go`, `cookie.go`)

- On construction the client loads the cookie file if present; a missing or
  unreadable file is silent (Debug log) and the client is still returned
  (`client.go:80-87`). Check `client.CookieCount()` (`client.go:306`) to detect an
  empty store.
- File format is NOT a flat name->value map. It is the store JSON:
  `{"cookies": {"<name>": {"value","last_update","source","essential"}}, "last_update", "request_profile": {"get_order_hash","headers"}}`
  (`store.go:23-27`, `cookie.go:11-16`). Do not hand-write it; produce it with
  `client.InitializeFromCurl(path)` from a browser "Copy as cURL" capture of a
  `getOrder` request (`client.go:114`). That call requires `CID`, `SPID` and `auth`
  cookies (`client.go:127-131`), replaces the whole store, and saves the file.
- `ExportCookies()` returns a name->value map; `SaveCookies()` persists.
  Successful `GetOrder` calls auto-save the store (`orders.go` after parse).
- Response `Set-Cookie` values are merged back via `updateCookiesFromResponse`.
- `RefreshFromBrowser()` is interactive (stdin) and unsuitable for unattended runs.
- Session upkeep: the request profile (GetOrder persisted-query hash, platform
  and sec-ch headers) travels with the cURL capture, so a stale capture can cause
  rejections even with valid cookies.

### Calls and shapes

#### Purchase history: `GetPurchaseHistory(ctx, PurchaseHistoryRequest) (*PurchaseHistoryResponse, error)` (`purchases.go:86`)

Request (`purchases.go:15-23`): `Cursor`, `Search`, `FilterIds []string`, `Limit`
(0 becomes 10), `Type *string`, `MinTimestamp *int64`, `MaxTimestamp *int64` (unix).
Helpers: `GetRecentOrders`, `GetAllOrders(ctx, maxPages)` (page size 20, follows
`NextPageCursor`), `SearchOrders`, `GetOrdersByType`.

Response path: `Data.OrderHistoryV2.{PageInfo{NextPageCursor,PrevPageCursor}, OrderGroups []OrderSummary}`.

`OrderSummary` (`purchases.go:39-53`): `Type` (IN_STORE, GLASS, ...), `OrderID`,
`GroupID`, `PurchaseOrderID *string`, `FulfillmentType`, `DerivedFulfillmentType`,
`IsActive`, `ItemCount`, `DeliveryMessage`, `Store *StoreInfo{ID,Name,Address.AddressLineOne}`,
`Status *StatusInfo{StatusType, Message.Parts[].Text}`, `Items []ItemSummary{ID, Quantity int, Name, ImageInfo}`,
`DeliveredDate *string`. Note: the summary has **no price and no order date**;
`DeliveredDate` is a pointer string with UNVERIFIED format. The order date comes
from `Order.OrderDate` (below).

#### Order detail: `GetOrder(ctx, orderID, isInStore bool)` / `GetOrderWithGroup(ctx, orderID, groupID, isInStore)` (`orders.go:60,67`)

Use `GetOrderWithGroup` with the `GroupID` from the summary; `GetOrder` passes
group "0". `GetOrderAutoDetect` tries in-store then delivery (`orders.go:224`).
`isInStore` is `Type == "IN_STORE"` by convention; UNVERIFIED which values of
`OrderSummary.Type` map to it.

`Order` (`models.go:13-25`): `ID`, `Type`, `OrderDate string` (UNVERIFIED format),
`DisplayID`, `Title`, `ShortTitle`, `Groups []OrderGroup` (JSON key `groups_2101`),
`Customer` (contains PII: names/email; never log), `Timezone`,
`PriceDetails *OrderPriceDetails`, `PaymentMethods []OrderPaymentMethod{Description,CardType,PaymentType}`.

Order-level money (`models.go:28-43`), each a `*PriceLineItem{Label, Value float64, DisplayValue}`:
`SubTotal`, `TaxTotal`, `GrandTotal`, `DriverTip` (tip), `TotalWithTip`, `Savings`,
and `Fees []PriceLineItem` (delivery and other fees; labels UNVERIFIED).
`CalculateTotalWithTip()` is applied by the client for delivery orders
(`orders.go`, `IsDeliveryOrder` is SC_DELIVERY or DFS, `models.go:278`).

`OrderGroup` (`models.go:104-116`): `ID`, `ItemCount`, `Items []OrderItem`,
`FulfillmentType`, `Status`, `TotalPrice *PriceInfo`, `Store`,
`PriceDetails *PriceDetails`, `PaymentDetails *PaymentDetails`, `Categories`,
`SubGroups`. Group-level `PriceDetails` (`models.go:142-150`):
`SubTotal`, `Tax *TaxInfo{TaxAmount}`, `Savings`, `GrandTotal`, `DriverTip`,
`DeliveryFee`, `TotalWithTip` (all `*Money`, `Money = Price{DisplayValue, Value float64}`).
`PaymentDetails.PaymentMethods[]{DisplayName, Last4Digits, Amount *Money}`.

`OrderItem` (`models.go:189-195`): `ID`, `ReturnID` (non-empty marks a refunded
item), `Quantity float64`, `ProductInfo{Name, USItemID, OfferID, IsAlcohol, SalesUnitType, ImageInfo}`,
`PriceInfo{LinePrice, UnitPrice}` (both `*Price`). Items are in `Group.Items`;
`Order.GetItems()` flattens all groups. `Categories`/`SubGroups` are Walmart UI
views of the same items, so do not sum them with `Items` (the client dedupes them
only in `GetRefundedItems`, `models.go:64`).
Money values are `float64` dollars; convert to cents in one place only.

Open: which of order-level vs group-level tax/tip/fees is populated for which
fulfillment types is UNVERIFIED; whether `sum(LinePrice) + tax + fees + tip`
equals the ledger total is UNVERIFIED (needs the spike).

#### Ledger: `GetOrderLedger(ctx, orderID) (*OrderLedger, error)` (`ledger.go:73`)

Separate rate limiter (`LedgerRateLimit`). Returns a simplified structure
(`ledger.go:18-31`):

```
OrderLedger{OrderID, PaymentMethods []PaymentMethodCharges}
PaymentMethodCharges{
  PaymentType  string      // "CREDITCARD" | "GIFTCARD" (per doc comments)
  CardType     string      // "VISA", "WMTRC", ...
  LastFour     string      // parsed from "Ending in 0953"; "" if no match
  FinalCharges []float64   // one entry per charge, dollars
  ChargedDates []time.Time // parallel to FinalCharges; zero value if unparsable
  TotalCharged float64
}
```

Only `ChargeType == "FINAL_CHARGES"` transactions are included; temporary holds
are dropped; methods with no final charges are omitted (`ledger.go:263-297`).
Refund rows (negative amounts such as `-$15.00` parse to negative floats, e.g.
`ORDER_ADJUSTMENT_REFUND` line types) would appear in `FinalCharges`, but the
client does not look at `LineType`, so whether refunds land there is UNVERIFIED.
`ChargedDates` come from `"Jan 2, 2006 3:04 PM"` strings parsed with
`time.Parse` (no zone, so UTC); date-only fallback; zero time on failure
(`ledger.go:335-372`). The time is in Walmart's local zone but labeled UTC:
treat as a wall-clock date, not an instant.

### Errors and retry behavior

- `walmart.ErrBotChallenge` (`orders.go:37`): sentinel for HTTP 456. Wrapped with
  `fmt.Errorf("%w: ...")`, so detect with `errors.Is`. Returned by order,
  purchase history and ledger. `GetOrderAutoDetect` stops immediately on it.
- HTTP 403 and 418: plain `fmt.Errorf` strings ("access denied - cookies
  expired..."); **no typed error**. Classify by string match or by wrapping in our
  adapter (`orders.go` `checkOrderResponseError`, `purchases.go:191`, `ledger.go:225`).
- HTTP 429: plain error "rate limited..." from order and purchase-history calls
  (no retry). The ledger call retries 429 with backoff 5s, 10s, 20s... up to
  `MaxRetries`, then returns `after N retries: rate limited (attempt a/b)`
  (`ledger.go:107-159`). `MaxRetries: -1` makes that single-attempt.
- Other statuses: `unexpected status code: N` (ledger) or `HTTP N: <body>`
  (order, purchase history; the body may be large and may contain account data,
  so scrub before logging).
- Missing order: `no order data in response`.
- Our adapter should map these to a small set of domain errors:
  `StaleSession` (403/418), `BotChallenge` (456), `RateLimited` (429 text), `Other`.
  The first three are blocked-session stops; as built they all end the run early
  with exit code 3 (this section first proposed exit 1 for stale cookies; see
  `DECISIONS.md`). `Other` fails only that order.

### Implications for `OrderProvider`

- The port needs: list orders in a date window (history, paginated), fetch order
  detail by (orderID, groupID, isInStore), fetch ledger by orderID.
- The adapter owns float to cents conversion (single place) and error classification.
- Charges for matching come from the ledger (per-charge amount, date, last four,
  payment type), not the order total. Gift-card portions (`PaymentType != CREDITCARD`)
  are reported and skipped per PRD section 6.5.

### UNVERIFIED — needs John's spike run

- Format of `Order.OrderDate` and `OrderSummary.DeliveredDate`.
- Which `OrderSummary.Type` values map to `isInStore`.
- Fee/tip/tax placement per fulfillment type and the reconciliation arithmetic.
- Whether refunds and Walmart Cash show up in `FinalCharges` and how.
- Real `PaymentType` values beyond CREDITCARD and GIFTCARD.
- Whether a multi-group order needs one ledger call or one per group.
- Whether purchase history `MinTimestamp`/`MaxTimestamp` filtering behaves.
- Whether the 403/418 and 456 paths trigger in practice (cookie lifetime).
- Whether purchase history lists orders newest first (the `-max` flag assumes it).
- Whether `ChargedDates` is populated for every charge in practice (the adapter
  falls back to the order date when it is not).
- Whether the cURL capture of a `getOrder` request is the right one to import
  (taken from the client library's documentation, not exercised live).
