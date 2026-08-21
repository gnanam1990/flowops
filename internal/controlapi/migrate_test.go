package controlapi

import "testing"

func TestPublishedMigrationsRemainForwardOnly(t *testing.T) {
	manifest, err := MigrationManifest()
	if err != nil {
		t.Fatal(err)
	}
	checksums := make(map[string]string, len(manifest))
	for _, migration := range manifest {
		checksums[migration.Name] = migration.Checksum
	}
	const published0015 = "787e34a594ff237dd3123f103b3bd5d907baca14cc3b65d77990b6c899f9e102"
	if checksums["0015_ascp_seller_egress.sql"] != published0015 {
		t.Fatalf("0015 checksum changed after publication: %s", checksums["0015_ascp_seller_egress.sql"])
	}
	if checksums["0016_harden_ascp_seller_egress.sql"] == "" {
		t.Fatal("forward seller-egress hardening migration is missing")
	}
	const published0017 = "f7d0fe85c4b9b579a5beefa12240ed51d345cf762a0dd08d898dd46f01fc038a"
	if checksums["0017_ascp_leadership_fence.sql"] != published0017 {
		t.Fatalf("0017 checksum changed after publication: %s", checksums["0017_ascp_leadership_fence.sql"])
	}
	const published0018 = "37128bd0656cd80028c6b8550a6f869133249dd91a4ca64e43e39f7aa2e99e22"
	if checksums["0018_durable_leadership_effects.sql"] != published0018 {
		t.Fatalf("0018 checksum changed after publication: %s", checksums["0018_durable_leadership_effects.sql"])
	}
}
