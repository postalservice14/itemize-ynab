-- +goose Up
CREATE TABLE ynab_charges (
    key         TEXT PRIMARY KEY,
    order_id    TEXT NOT NULL,
    ynab_txn_id TEXT NOT NULL,
    outcome     TEXT NOT NULL,
    created_at  TEXT NOT NULL
);
CREATE INDEX idx_ynab_charges_order_id ON ynab_charges (order_id);

CREATE TABLE server_knowledge (
    plan_id    TEXT PRIMARY KEY,
    value      INTEGER NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE ynab_txn_cache (
    plan_id    TEXT NOT NULL,
    txn_id     TEXT NOT NULL,
    account_id TEXT NOT NULL,
    date       TEXT NOT NULL,
    body       BLOB NOT NULL,
    PRIMARY KEY (plan_id, txn_id)
);
CREATE INDEX idx_ynab_txn_cache_plan_date ON ynab_txn_cache (plan_id, date);

CREATE TABLE ynab_txn_cache_meta (
    plan_id    TEXT PRIMARY KEY,
    since_date TEXT NOT NULL
);

-- +goose Down
DROP TABLE ynab_txn_cache_meta;
DROP INDEX idx_ynab_txn_cache_plan_date;
DROP TABLE ynab_txn_cache;
DROP TABLE server_knowledge;
DROP INDEX idx_ynab_charges_order_id;
DROP TABLE ynab_charges;
