ALTER TABLE sites_identity_providers
ADD COLUMN organization_id text;

UPDATE sites_identity_providers AS provider
SET organization_id = membership.organization_id
FROM (
    SELECT site_project_id, min(organization_id) AS organization_id
    FROM sites_memberships
    GROUP BY site_project_id
    HAVING count(DISTINCT organization_id) = 1
) AS membership
WHERE membership.site_project_id = provider.site_project_id;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM sites_identity_providers WHERE organization_id IS NULL) THEN
        RAISE EXCEPTION 'every Sites provider must have exactly one organization before migration';
    END IF;
END;
$$;

ALTER TABLE sites_identity_providers
ALTER COLUMN organization_id SET NOT NULL;

ALTER TABLE sites_identity_providers
ADD CONSTRAINT sites_identity_providers_organization_fk
FOREIGN KEY (organization_id) REFERENCES organizations(id);

ALTER TABLE sites_identity_providers
ADD CONSTRAINT sites_identity_providers_project_organization_unique
UNIQUE (site_project_id, organization_id);

ALTER TABLE sites_memberships
ADD CONSTRAINT sites_memberships_provider_organization_fk
FOREIGN KEY (site_project_id, organization_id)
REFERENCES sites_identity_providers(site_project_id, organization_id);
