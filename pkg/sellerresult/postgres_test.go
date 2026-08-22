package sellerresult

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPostgresStoreClaimsCompletesAndReplaysExactResult(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	store, _ := NewPostgresStore(db)
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	request := validRequest(now)
	retainUntil, _ := retentionDeadline(request.SettleBy)

	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO ascp_seller_results`).WithArgs(
		request.SellerID, request.CallID, request.RequestHash, request.ResourceOperationKey,
		request.SettleBy, retainUntil, now,
	).WillReturnRows(sqlmock.NewRows([]string{"created_at", "retain_until"}).AddRow(now, retainUntil))
	mock.ExpectCommit()
	claimed, execute, err := store.Begin(context.Background(), request, now)
	if err != nil || !execute || claimed.State != StateStartedUnknown {
		t.Fatalf("claimed=%+v execute=%t err=%v", claimed, execute, err)
	}

	response, _ := normalizeResponse(Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: []byte(`{"ok":true}`)})
	completedAt := now.Add(time.Second)
	mock.ExpectBegin()
	mock.ExpectQuery(`UPDATE ascp_seller_results`).WithArgs(
		request.SellerID, request.CallID, request.RequestHash, request.ResourceOperationKey,
		response.StatusCode, sqlmock.AnyArg(), response.Body, response.ContentDigest, completedAt,
	).WillReturnRows(sqlmock.NewRows([]string{"completed_at"}).AddRow(completedAt))
	mock.ExpectCommit()
	completed, err := store.Complete(context.Background(), request, response, completedAt)
	if err != nil || completed.State != StateCompleted || !sameResponse(completed.Response, response) {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO ascp_seller_results`).WillReturnRows(sqlmock.NewRows([]string{"created_at", "retain_until"}))
	mock.ExpectQuery(`SELECT seller_id, call_id, request_hash`).WithArgs(request.SellerID, request.CallID).WillReturnRows(
		sqlmock.NewRows([]string{"seller_id", "call_id", "request_hash", "resource_operation_key", "state", "response_status", "response_headers", "response_body", "content_digest", "side_effect_status", "settle_by", "retain_until", "created_at", "completed_at"}).AddRow(
			request.SellerID, request.CallID, request.RequestHash, request.ResourceOperationKey, StateCompleted,
			response.StatusCode, []byte(`{"Content-Type":["application/json"]}`), response.Body, response.ContentDigest, "COMPLETED",
			request.SettleBy, retainUntil, now, completedAt,
		))
	mock.ExpectCommit()
	replayed, execute, err := store.Begin(context.Background(), request, now.Add(365*24*time.Hour))
	if err != nil || execute || !sameResponse(replayed.Response, response) {
		t.Fatalf("replayed=%+v execute=%t err=%v", replayed, execute, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
