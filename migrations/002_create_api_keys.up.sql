CREATE TABLE api_keys (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    key_hash TEXT NOT NULL UNIQUE,
    key_prefix TEXT NOT NULL,
    org_id UUID NOT NULL REFERENCES organizations(id),
    name TEXT NOT NULL,
    scopes TEXT[] DEFAULT '{}',
    rate_limit_rpm INT DEFAULT 60,
    rate_limit_tpm INT DEFAULT 100000,
    is_active BOOLEAN DEFAULT TRUE,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    expires_at TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ
);

-- key_hash is UNIQUE, which Postgres already backs with an index; no
-- separate idx_api_keys_hash is needed.
CREATE INDEX idx_api_keys_org ON api_keys(org_id);
