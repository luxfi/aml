-- AML core collections for Hanzo Base.
-- Base auto-manages id, created, updated on every record.
-- These are the 9 canonical collections.

-- 1. Transactions — incoming transaction events
CREATE TABLE IF NOT EXISTS transactions (
    id          TEXT PRIMARY KEY,
    org_id      TEXT NOT NULL DEFAULT '',
    tenant_id   TEXT NOT NULL DEFAULT '',
    source      TEXT NOT NULL DEFAULT '',
    user_id     TEXT NOT NULL DEFAULT '',
    account_id  TEXT NOT NULL DEFAULT '',
    symbol      TEXT NOT NULL DEFAULT '',
    asset_class TEXT NOT NULL DEFAULT '',
    side        TEXT NOT NULL DEFAULT '',
    qty         REAL NOT NULL DEFAULT 0,
    notional    REAL NOT NULL DEFAULT 0,
    currency    TEXT NOT NULL DEFAULT 'USD',
    counterparty TEXT NOT NULL DEFAULT '',
    ip_address  TEXT NOT NULL DEFAULT '',
    device_fingerprint TEXT NOT NULL DEFAULT '',
    timestamp   TEXT NOT NULL DEFAULT '',
    raw         JSON DEFAULT '{}',
    created_at  TEXT NOT NULL DEFAULT '',
    updated_at  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_tx_org ON transactions(org_id);
CREATE INDEX IF NOT EXISTS idx_tx_user ON transactions(user_id);
CREATE INDEX IF NOT EXISTS idx_tx_timestamp ON transactions(timestamp);

-- 2. Entities — normalized actors
CREATE TABLE IF NOT EXISTS entities (
    id             TEXT PRIMARY KEY,
    org_id         TEXT NOT NULL DEFAULT '',
    entity_type    TEXT NOT NULL DEFAULT 'user',
    external_id    TEXT NOT NULL DEFAULT '',
    name           TEXT NOT NULL DEFAULT '',
    jurisdiction   TEXT NOT NULL DEFAULT '',
    kyc_level      INTEGER NOT NULL DEFAULT 0,
    pep            INTEGER NOT NULL DEFAULT 0,
    sanctions_flag INTEGER NOT NULL DEFAULT 0,
    risk_score     REAL NOT NULL DEFAULT 0,
    first_seen     TEXT NOT NULL DEFAULT '',
    last_seen      TEXT NOT NULL DEFAULT '',
    created_at     TEXT NOT NULL DEFAULT '',
    updated_at     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_entity_org ON entities(org_id);
CREATE INDEX IF NOT EXISTS idx_entity_external ON entities(external_id);

-- 3. Rules — evaluation rules
CREATE TABLE IF NOT EXISTS rules (
    id                  TEXT PRIMARY KEY,
    org_id              TEXT NOT NULL DEFAULT '',
    name                TEXT NOT NULL DEFAULT '',
    description         TEXT NOT NULL DEFAULT '',
    dsl                 TEXT NOT NULL DEFAULT '',
    severity            TEXT NOT NULL DEFAULT 'medium',
    weight              REAL NOT NULL DEFAULT 0.1,
    action              TEXT NOT NULL DEFAULT 'flag',
    enabled             INTEGER NOT NULL DEFAULT 1,
    jurisdiction_filter JSON DEFAULT '[]',
    asset_class_filter  JSON DEFAULT '[]',
    priority            INTEGER NOT NULL DEFAULT 0,
    created_at          TEXT NOT NULL DEFAULT '',
    updated_at          TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_rules_org ON rules(org_id);

-- 4. Alerts — rule hits
CREATE TABLE IF NOT EXISTS alerts (
    id              TEXT PRIMARY KEY,
    org_id          TEXT NOT NULL DEFAULT '',
    tx_id           TEXT NOT NULL DEFAULT '',
    rule_id         TEXT NOT NULL DEFAULT '',
    rule_name       TEXT NOT NULL DEFAULT '',
    severity        TEXT NOT NULL DEFAULT 'medium',
    score           REAL NOT NULL DEFAULT 0,
    score_breakdown JSON DEFAULT '{}',
    action_taken    TEXT NOT NULL DEFAULT 'flag',
    reviewed_by     TEXT NOT NULL DEFAULT '',
    reviewed_at     TEXT NOT NULL DEFAULT '',
    decision        TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL DEFAULT '',
    updated_at      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_alerts_org ON alerts(org_id);
CREATE INDEX IF NOT EXISTS idx_alerts_tx ON alerts(tx_id);
CREATE INDEX IF NOT EXISTS idx_alerts_rule ON alerts(rule_id);

-- 5. Cases — human review cases
CREATE TABLE IF NOT EXISTS cases (
    id          TEXT PRIMARY KEY,
    org_id      TEXT NOT NULL DEFAULT '',
    number      INTEGER NOT NULL DEFAULT 0,
    status      TEXT NOT NULL DEFAULT 'open',
    severity    TEXT NOT NULL DEFAULT 'medium',
    entity_ids  JSON DEFAULT '[]',
    alert_ids   JSON DEFAULT '[]',
    assignee_id TEXT NOT NULL DEFAULT '',
    opened_at   TEXT NOT NULL DEFAULT '',
    closed_at   TEXT NOT NULL DEFAULT '',
    resolution  TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL DEFAULT '',
    updated_at  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_cases_org ON cases(org_id);
CREATE INDEX IF NOT EXISTS idx_cases_status ON cases(status);

-- 6. Case events — case timeline
CREATE TABLE IF NOT EXISTS case_events (
    id         TEXT PRIMARY KEY,
    case_id    TEXT NOT NULL DEFAULT '',
    author_id  TEXT NOT NULL DEFAULT '',
    kind       TEXT NOT NULL DEFAULT 'note',
    body       TEXT NOT NULL DEFAULT '',
    file_path  TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_case_events_case ON case_events(case_id);

-- 7. Sanctions lists — list metadata
CREATE TABLE IF NOT EXISTS sanctions_lists (
    id         TEXT PRIMARY KEY,
    source     TEXT NOT NULL DEFAULT '',
    url        TEXT NOT NULL DEFAULT '',
    format     TEXT NOT NULL DEFAULT 'xml',
    fetched_at TEXT NOT NULL DEFAULT '',
    sha256     TEXT NOT NULL DEFAULT '',
    active     INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL DEFAULT ''
);

-- 8. Sanctions entries — flattened entries
CREATE TABLE IF NOT EXISTS sanctions_entries (
    id          TEXT PRIMARY KEY,
    list_id     TEXT NOT NULL DEFAULT '',
    ref_id      TEXT NOT NULL DEFAULT '',
    name        TEXT NOT NULL DEFAULT '',
    aliases     JSON DEFAULT '[]',
    dob         TEXT NOT NULL DEFAULT '',
    nationality TEXT NOT NULL DEFAULT '',
    address     TEXT NOT NULL DEFAULT '',
    type        TEXT NOT NULL DEFAULT 'individual',
    raw         JSON DEFAULT '{}',
    created_at  TEXT NOT NULL DEFAULT '',
    updated_at  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_sanctions_list ON sanctions_entries(list_id);
CREATE INDEX IF NOT EXISTS idx_sanctions_name ON sanctions_entries(name);

-- 9. Webhooks — subscribers
CREATE TABLE IF NOT EXISTS webhooks (
    id               TEXT PRIMARY KEY,
    org_id           TEXT NOT NULL DEFAULT '',
    url              TEXT NOT NULL DEFAULT '',
    secret           TEXT NOT NULL DEFAULT '',
    events           JSON DEFAULT '[]',
    enabled          INTEGER NOT NULL DEFAULT 1,
    last_delivery_at TEXT NOT NULL DEFAULT '',
    failure_count    INTEGER NOT NULL DEFAULT 0,
    created_at       TEXT NOT NULL DEFAULT '',
    updated_at       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_webhooks_org ON webhooks(org_id);
