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
	const published0019 = "f0099d68c41ffec50f82097616e3a6041c35ebcc4fbda6f250a54a55d588fb52"
	if checksums["0019_restricted_event_head.sql"] != published0019 {
		t.Fatalf("0019 checksum changed after publication: %s", checksums["0019_restricted_event_head.sql"])
	}
	const published0020 = "5edcc33cbb31cdc88370506716e93206046c3d45cb960ac273d39fd43d83c6f4"
	if checksums["0020_ascp_verifier_runtime.sql"] != published0020 {
		t.Fatalf("0020 checksum changed after publication: %s", checksums["0020_ascp_verifier_runtime.sql"])
	}
	const published0021 = "3730d18f558b9ee94d1b52711efa19eca5b208616bd9ed11d67637fc23acdb58"
	if checksums["0021_harden_ascp_verifier_runtime.sql"] != published0021 {
		t.Fatalf("0021 checksum changed after publication: %s", checksums["0021_harden_ascp_verifier_runtime.sql"])
	}
	const published0028 = "d422be432b097d6eb1a69d76564144f77e143ff5a53f347e75d6a1f0624769c3"
	if checksums["0028_ascp_governance_receipt_ownership.sql"] != published0028 {
		t.Fatalf("0028 checksum changed after publication: %s", checksums["0028_ascp_governance_receipt_ownership.sql"])
	}
	const published0029 = "6b8cd13e845442a21a0d5c972a55de809dbffc64008bc8e20917d6e704231bd0"
	if checksums["0029_ascp_governance_action_lifecycle.sql"] != published0029 {
		t.Fatalf("0029 checksum changed after publication: %s", checksums["0029_ascp_governance_action_lifecycle.sql"])
	}
	const published0030 = "a4b19cec81136dd6b4a0d58f956bb73984cc87337746ded98e763ccfdd5b0368"
	if checksums["0030_ascp_governance_safe_relayer.sql"] != published0030 {
		t.Fatalf("0030 checksum changed after publication: %s", checksums["0030_ascp_governance_safe_relayer.sql"])
	}
}
