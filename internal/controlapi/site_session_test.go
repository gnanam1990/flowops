package controlapi

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func testSiteMembership() SiteMembership {
	return SiteMembership{
		ID: "membership_1", SiteProjectID: "appgprj_flowops_1",
		SiteUserKey: strings.Repeat("a", 64), OrganizationID: "org_a",
		PrincipalID: "owner_a", Role: RoleOwner,
	}
}

func TestSiteSessionBindsMembershipAndExpires(t *testing.T) {
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	codec, err := NewSiteSessionCodec([]byte(strings.Repeat("k", 32)), 2*time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	token, expiresAt, err := codec.Mint(testSiteMembership())
	if err != nil || expiresAt != now.Add(2*time.Minute) || !strings.HasPrefix(token, siteSessionPrefix) {
		t.Fatalf("mint = %q %v %v", token, expiresAt, err)
	}
	membership, err := codec.Verify(token)
	if err != nil || membership != testSiteMembership() {
		t.Fatalf("verify = %+v, %v", membership, err)
	}

	parts := strings.Split(token, ".")
	parts[1] = strings.Repeat("A", len(parts[1]))
	if _, err := codec.Verify(strings.Join(parts, ".")); !errors.Is(err, ErrInvalidSiteSession) {
		t.Fatalf("tampered payload error = %v", err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := codec.Verify(token); !errors.Is(err, ErrInvalidSiteSession) {
		t.Fatalf("expired token error = %v", err)
	}
}

func TestSiteSessionRejectsWeakKeysAndInvalidMemberships(t *testing.T) {
	if _, err := NewSiteSessionCodec(make([]byte, 31), time.Minute, nil); err == nil {
		t.Fatal("weak session key was accepted")
	}
	if _, err := NewSiteSessionCodec(make([]byte, 32), time.Second-time.Nanosecond, nil); err == nil {
		t.Fatal("unrepresentable sub-second session TTL was accepted")
	}
	codec, err := NewSiteSessionCodec(make([]byte, 32), time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	membership := testSiteMembership()
	membership.Role = RoleAgent
	if _, _, err := codec.Mint(membership); !errors.Is(err, ErrInvalidSiteSession) {
		t.Fatalf("agent role mint error = %v", err)
	}
}

func TestSiteUserKeyIsSiteBoundAndEmailDigestIsNormalized(t *testing.T) {
	first, err := SiteUserKey("appgprj_flowops_1", "user_opaque")
	if err != nil {
		t.Fatal(err)
	}
	second, _ := SiteUserKey("appgprj_flowops_2", "user_opaque")
	if first == second || len(first) != 64 {
		t.Fatalf("site-bound keys = %q %q", first, second)
	}
	lower, err := normalizedEmailDigest("operator@example.com")
	if err != nil {
		t.Fatal(err)
	}
	mixed, err := normalizedEmailDigest(" Operator@Example.COM ")
	if err != nil || lower != mixed {
		t.Fatalf("normalized email digests differ: %x %x %v", lower, mixed, err)
	}
	if _, err := normalizedEmailDigest("invalid"); err == nil {
		t.Fatal("invalid email was accepted")
	}
}

func TestOrganizationNameLimitCountsUnicodeCharacters(t *testing.T) {
	valid := Organization{ID: "org_unicode", Name: strings.Repeat("界", 200)}
	if !valid.Valid() {
		t.Fatal("200-character Unicode organization name was rejected")
	}
	valid.Name += "界"
	if valid.Valid() {
		t.Fatal("201-character organization name was accepted")
	}
}
