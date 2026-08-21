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
	const published0017 = "e576811d34d701b9381319cb9536d57ed0c93656559cf5d2e3b5679756f056ae"
	if checksums["0017_ascp_leadership_fence.sql"] != published0017 {
		t.Fatalf("0017 checksum changed after publication: %s", checksums["0017_ascp_leadership_fence.sql"])
	}
}
