package main

import (
	"context"
	"errors"

	"github.com/gnanam1990/flowops/internal/ascpsettlement"
	"github.com/gnanam1990/flowops/internal/controlapi"
	"github.com/gnanam1990/flowops/internal/reconciliation"
)

type ascpSettlementRegistrar struct{ store *ascpsettlement.PostgresStore }

func (r ascpSettlementRegistrar) Register(ctx context.Context, request controlapi.ASCPSettlementAttemptRequest) (controlapi.ASCPSettlementAttempt, error) {
	if r.store == nil {
		return controlapi.ASCPSettlementAttempt{}, errors.New("ASCP settlement registrar is unavailable")
	}
	action := reconciliation.ASCPReceiptAction(request.Action)
	attempt, replayed, err := r.store.RegisterAttempt(ctx, ascpsettlement.AttemptInput{
		OperationID: request.OperationID, Action: action, TransactionHash: request.TransactionHash,
		DeliveryHash: request.DeliveryHash, EvidenceHash: request.EvidenceHash,
	})
	if err != nil {
		if errors.Is(err, ascpsettlement.ErrInvalidAttempt) {
			return controlapi.ASCPSettlementAttempt{}, controlapi.ErrASCPSettlementAttemptInvalid
		}
		if errors.Is(err, ascpsettlement.ErrStateConflict) || errors.Is(err, ascpsettlement.ErrNotFound) {
			return controlapi.ASCPSettlementAttempt{}, controlapi.ErrASCPSettlementAttemptConflict
		}
		return controlapi.ASCPSettlementAttempt{}, err
	}
	return controlapi.ASCPSettlementAttempt{
		ASCPSettlementAttemptRequest: request, State: string(attempt.State), RegisteredAt: attempt.RegisteredAt, Replayed: replayed,
	}, nil
}
