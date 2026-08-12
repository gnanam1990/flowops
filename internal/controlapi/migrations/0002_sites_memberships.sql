CREATE TABLE IF NOT EXISTS sites_identity_providers (
    site_project_id text PRIMARY KEY,
    exchange_token_digest bytea NOT NULL UNIQUE CHECK (octet_length(exchange_token_digest) = 32),
    enabled boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    rotated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS sites_memberships (
    id text PRIMARY KEY,
    site_project_id text NOT NULL REFERENCES sites_identity_providers(site_project_id),
    site_user_key text NOT NULL CHECK (site_user_key ~ '^[0-9a-f]{64}$'),
    email_digest bytea NOT NULL CHECK (octet_length(email_digest) = 32),
    organization_id text NOT NULL REFERENCES organizations(id),
    principal_id text NOT NULL,
    role text NOT NULL CHECK (role IN ('OWNER','ADMIN','DEVELOPER','FINANCE','APPROVER','AUDITOR','VIEWER')),
    status text NOT NULL CHECK (status IN ('ACTIVE','REVOKED')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (site_project_id, site_user_key)
);

CREATE INDEX IF NOT EXISTS sites_memberships_org_idx
ON sites_memberships (organization_id, status);
