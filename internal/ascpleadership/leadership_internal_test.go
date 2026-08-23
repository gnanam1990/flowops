package ascpleadership

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestLeadershipRejectsMalformedInputs(t *testing.T) {
	if _, err := NewPostgres(nil, "public"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil database=%v", err)
	}
	if _, err := NewPostgres(&sql.DB{}, "public;drop schema"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid schema=%v", err)
	}
	for _, schema := range []string{"pg_temp", "pg_temp_3", "pg_toast_temp_3"} {
		if _, err := NewPostgres(&sql.DB{}, schema); !errors.Is(err, ErrInvalid) {
			t.Fatalf("temporary schema %q accepted: %v", schema, err)
		}
	}
	if validOrganization("org test") || validOrganization("org\u00a0test") || validOrganization("org\u0085test") ||
		validMutation("org-test", "owner-safe", "0x01") {
		t.Fatal("malformed leadership input accepted")
	}
	store, _ := NewPostgres(&sql.DB{}, "public")
	if err := store.Fence(t.Context(), "org-test", maxEpoch+1, func(context.Context) error { return nil }); !errors.Is(err, ErrInvalid) {
		t.Fatalf("out-of-range fence epoch=%v", err)
	}
	if _, err := store.Advance(t.Context(), "org-test", maxEpoch, "owner-safe", internalLeadershipHash("1")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("overflowing advance epoch=%v", err)
	}
	if err := store.AbandonEffect(t.Context(), "org-test", 1, "not-a-digest", "owner-safe", internalLeadershipHash("1")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("malformed abandonment effect id=%v", err)
	}
	effectID, err := randomEffectID()
	if err != nil || !hashPattern.MatchString(effectID) {
		t.Fatalf("random effect id=%q err=%v", effectID, err)
	}
}

func internalLeadershipHash(fill string) string {
	return "0x" + repeatForLeadershipTest(fill, 64)
}

func repeatForLeadershipTest(value string, count int) string {
	result := ""
	for len(result) < count {
		result += value
	}
	return result[:count]
}
