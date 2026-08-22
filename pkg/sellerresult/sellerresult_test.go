package sellerresult

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestExactResultSurvivesSevenThirtyAnd365DayRestores(t *testing.T) {
	base := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	now := base
	store := NewMemoryStore()
	service, _ := New(store, func() time.Time { return now })
	request := validRequest(base)
	want := Response{StatusCode: http.StatusCreated, Header: http.Header{"Content-Type": {"application/json"}, "X-Receipt": {"receipt-1"}}, Body: []byte(`{"ok":true}`)}
	var effects atomic.Int32
	first, replayed, err := service.Execute(context.Background(), request, func(context.Context) (Response, error) {
		effects.Add(1)
		return want, nil
	})
	if err != nil || replayed || first.ContentDigest == "" {
		t.Fatalf("first=%+v replayed=%t err=%v", first, replayed, err)
	}

	for _, age := range []time.Duration{7 * 24 * time.Hour, 30 * 24 * time.Hour, 365 * 24 * time.Hour} {
		now = base.Add(age)
		restored, err := NewMemoryStoreFromSnapshot(store.Snapshot())
		if err != nil {
			t.Fatal(err)
		}
		store = restored
		service, _ = New(store, func() time.Time { return now })
		got, replayed, err := service.Execute(context.Background(), request, func(context.Context) (Response, error) {
			effects.Add(1)
			return Response{}, errors.New("must not execute")
		})
		if err != nil || !replayed || !sameResponse(first, got) {
			t.Fatalf("age=%s got=%+v replayed=%t err=%v", age, got, replayed, err)
		}
	}
	if effects.Load() != 1 {
		t.Fatalf("effects=%d", effects.Load())
	}
}

func TestRestoreOfUnknownSideEffectFailsClosed(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	request := validRequest(now)
	if _, execute, err := store.Begin(context.Background(), request, now); err != nil || !execute {
		t.Fatalf("begin execute=%t err=%v", execute, err)
	}
	restored, err := NewMemoryStoreFromSnapshot(store.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	service, _ := New(restored, func() time.Time { return now.Add(365 * 24 * time.Hour) })
	var effects atomic.Int32
	_, _, err = service.Execute(context.Background(), request, func(context.Context) (Response, error) {
		effects.Add(1)
		return Response{StatusCode: 200, Body: []byte("duplicate")}, nil
	})
	if !errors.Is(err, ErrRecoveryRequired) || effects.Load() != 0 {
		t.Fatalf("err=%v effects=%d", err, effects.Load())
	}
}

func TestChangedBindingAndResourceKeyReuseAreRejected(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	request := validRequest(now)
	if _, _, err := store.Begin(context.Background(), request, now); err != nil {
		t.Fatal(err)
	}
	changed := request
	changed.RequestHash = hash(3)
	if _, _, err := store.Begin(context.Background(), changed, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed binding err=%v", err)
	}
	secondCall := request
	secondCall.CallID = hash(4)
	if _, _, err := store.Begin(context.Background(), secondCall, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("resource key reuse err=%v", err)
	}
}

func TestResponseDigestMustBindExactBytes(t *testing.T) {
	response := Response{StatusCode: 200, Body: []byte("actual"), ContentDigest: hash(9)}
	if _, err := normalizeResponse(response); err == nil {
		t.Fatal("mismatched digest accepted")
	}
}

func TestResourceOperationConstraintIsTypedConflict(t *testing.T) {
	err := classifyClaimError(&pgconn.PgError{Code: "23505", ConstraintName: "ascp_seller_results_resource_operation_key_unique"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err=%v", err)
	}
}

func validRequest(now time.Time) Request {
	return Request{SellerID: "seller_1", CallID: hash(1), RequestHash: hash(2), ResourceOperationKey: "resource-op-1", SettleBy: now.Add(24 * time.Hour)}
}

func hash(value byte) string { return "0x" + strings.Repeat(string([]byte{'0' + value}), 64) }
