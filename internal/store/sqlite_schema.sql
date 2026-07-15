CREATE TABLE IF NOT EXISTS skills (
    id TEXT PRIMARY KEY,
    slug TEXT NOT NULL,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL,
    installs INTEGER NOT NULL DEFAULT 0 CHECK (installs >= 0),
    stars INTEGER NOT NULL CHECK (stars > 10),
    source_type TEXT NOT NULL DEFAULT 'github',
    install_url TEXT NOT NULL,
    public_url TEXT NOT NULL,
    is_duplicate INTEGER NOT NULL DEFAULT 0,
    security_score INTEGER NOT NULL CHECK (security_score BETWEEN 5 AND 10),
    official INTEGER NOT NULL DEFAULT 0,
    content_hash TEXT NOT NULL,
    files TEXT NOT NULL DEFAULT '[]',
    audit_provider TEXT NOT NULL,
    audit_slug TEXT NOT NULL,
    audit_status TEXT NOT NULL,
    audit_summary TEXT NOT NULL,
    audit_risk_level TEXT NOT NULL,
    audit_categories TEXT NOT NULL DEFAULT '[]',
    audited_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS skills_rank_idx ON skills (stars DESC, id);
CREATE INDEX IF NOT EXISTS skills_official_idx ON skills (official, source) WHERE official = 1;
CREATE INDEX IF NOT EXISTS skills_search_idx ON skills (lower(name), lower(source));

CREATE TABLE IF NOT EXISTS api_keys (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    token_hash BLOB NOT NULL UNIQUE,
    encrypted_token BLOB NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at DATETIME NOT NULL,
    last_used_at DATETIME,
    revoked_at DATETIME
);

CREATE INDEX IF NOT EXISTS api_keys_active_idx ON api_keys (token_hash, expires_at) WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS skill_downloads (
    skill_id TEXT NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
    api_key_id TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    downloads INTEGER NOT NULL DEFAULT 0 CHECK (downloads >= 0),
    first_downloaded_at DATETIME NOT NULL,
    last_downloaded_at DATETIME NOT NULL,
    PRIMARY KEY (skill_id, api_key_id)
);

CREATE INDEX IF NOT EXISTS skill_downloads_last_idx ON skill_downloads (last_downloaded_at DESC);
