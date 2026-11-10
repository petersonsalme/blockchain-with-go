-- ADR-0038. Amounts are NUMERIC(20,0): a uint64 does not fit BIGINT.
CREATE TABLE blocks (
    height   BIGINT PRIMARY KEY,
    hash     TEXT NOT NULL UNIQUE,
    prev_hash TEXT NOT NULL,
    time     TIMESTAMPTZ NOT NULL,
    tx_count INTEGER NOT NULL
);

-- An output is keyed by the height that created it too, so a txid reused after its outputs were
-- spent (ADR-0015, coinbase) does not break the insert; at most one copy of (txid, idx) is unspent.
CREATE TABLE outputs (
    txid         TEXT NOT NULL,
    idx          INTEGER NOT NULL,
    height       BIGINT NOT NULL,
    address      TEXT NOT NULL,
    amount       NUMERIC(20,0) NOT NULL,
    spent_txid   TEXT,
    spent_height BIGINT,
    PRIMARY KEY (txid, idx, height)
);
CREATE UNIQUE INDEX outputs_unspent ON outputs (txid, idx) WHERE spent_height IS NULL;
CREATE INDEX outputs_height ON outputs (height);
CREATE INDEX outputs_spent_height ON outputs (spent_height) WHERE spent_height IS NOT NULL;
CREATE INDEX outputs_address ON outputs (address) WHERE spent_height IS NULL;

CREATE TABLE balances (
    address TEXT PRIMARY KEY,
    amount  NUMERIC(20,0) NOT NULL CHECK (amount >= 0)
);

CREATE TABLE supply (
    height   BIGINT PRIMARY KEY REFERENCES blocks (height),
    created  NUMERIC(20,0) NOT NULL,
    spent    NUMERIC(20,0) NOT NULL,
    coinbase NUMERIC(20,0) NOT NULL,
    total    NUMERIC(20,0) NOT NULL
);
