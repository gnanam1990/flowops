ALTER TABLE organizations
ADD COLUMN IF NOT EXISTS authorizations_paused boolean NOT NULL DEFAULT false;
