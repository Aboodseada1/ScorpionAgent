PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS clients (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    business    TEXT NOT NULL DEFAULT '',
    industry    TEXT NOT NULL DEFAULT '',
    stage       TEXT NOT NULL DEFAULT 'new',
    role        TEXT NOT NULL DEFAULT 'seller',
    notes       TEXT NOT NULL DEFAULT '',
    profile_json TEXT NOT NULL DEFAULT '{}',
    avatar_color TEXT NOT NULL DEFAULT '',
    created_at  REAL NOT NULL,
    updated_at  REAL NOT NULL
);

CREATE TABLE IF NOT EXISTS client_docs (
    id         TEXT PRIMARY KEY,
    client_id  TEXT,
    title      TEXT NOT NULL,
    mime       TEXT NOT NULL DEFAULT 'text/plain',
    body       TEXT NOT NULL DEFAULT '',
    created_at REAL NOT NULL,
    FOREIGN KEY(client_id) REFERENCES clients(id) ON DELETE SET NULL
);

CREATE TABLE IF NOT EXISTS conversations (
    id          TEXT PRIMARY KEY,
    client_id   TEXT,
    started_at  REAL NOT NULL,
    ended_at    REAL,
    summary     TEXT NOT NULL DEFAULT '',
    audio_dir   TEXT NOT NULL DEFAULT '',
    FOREIGN KEY(client_id) REFERENCES clients(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_conv_client ON conversations(client_id, started_at DESC);

CREATE TABLE IF NOT EXISTS turns (
    id              TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL,
    idx             INTEGER NOT NULL,
    speaker         TEXT NOT NULL,
    text            TEXT NOT NULL,
    t_start_ms      INTEGER NOT NULL DEFAULT 0,
    t_end_ms        INTEGER NOT NULL DEFAULT 0,
    wav_path        TEXT NOT NULL DEFAULT '',
    FOREIGN KEY(conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_turns_conv ON turns(conversation_id, idx);

CREATE TABLE IF NOT EXISTS facts (
    id              TEXT PRIMARY KEY,
    client_id       TEXT,
    conversation_id TEXT,
    day             TEXT NOT NULL,
    subject         TEXT NOT NULL,
    predicate       TEXT NOT NULL,
    object          TEXT NOT NULL,
    category        TEXT NOT NULL DEFAULT 'general',
    confidence      REAL NOT NULL DEFAULT 0.7,
    source_turn_id  TEXT,
    created_at      REAL NOT NULL,
    FOREIGN KEY(client_id) REFERENCES clients(id) ON DELETE CASCADE,
    FOREIGN KEY(conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_facts_client ON facts(client_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_facts_conv ON facts(conversation_id);

CREATE TABLE IF NOT EXISTS actions (
    id              TEXT PRIMARY KEY,
    client_id       TEXT,
    conversation_id TEXT,
    type            TEXT NOT NULL,
    payload_json    TEXT NOT NULL DEFAULT '{}',
    status          TEXT NOT NULL DEFAULT 'pending',
    due_at          REAL,
    created_at      REAL NOT NULL,
    executed_at     REAL,
    FOREIGN KEY(client_id) REFERENCES clients(id) ON DELETE CASCADE,
    FOREIGN KEY(conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_actions_client ON actions(client_id, created_at DESC);

CREATE TABLE IF NOT EXISTS embeddings (
    id        TEXT PRIMARY KEY,
    kind      TEXT NOT NULL,
    ref_id    TEXT NOT NULL,
    client_id TEXT,
    text      TEXT NOT NULL,
    vec       BLOB NOT NULL,
    dim       INTEGER NOT NULL,
    created_at REAL NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_emb_kind_ref ON embeddings(kind, ref_id);
CREATE INDEX IF NOT EXISTS idx_emb_client ON embeddings(client_id);
