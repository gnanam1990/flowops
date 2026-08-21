package ascpverifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
)

// StructuredDataEngine is the built-in v1 engine for a top-level JSON object.
// Each predicate ID names an exact top-level member, operator must be
// json-equals, and Expected must itself be one canonical JSON value.
type StructuredDataEngine struct{}

func (StructuredDataEngine) Verify(ctx context.Context, input EngineInput) (EngineResult, error) {
	select {
	case <-ctx.Done():
		return EngineResult{}, ctx.Err()
	default:
	}
	if input.Spec.Class != ClassStructuredData || input.Spec.Tolerance != "0" || input.Spec.ReferenceSource != "captured-delivery" {
		return EngineResult{}, fmt.Errorf("%w: structured-data engine requires exact self-contained evidence", ErrInvalidEngineResult)
	}
	decoder := json.NewDecoder(bytes.NewReader(input.Delivery.Content))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil || document == nil {
		return EngineResult{Verdict: VerdictFail, Code: "invalid-json-object"}, nil
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return EngineResult{Verdict: VerdictFail, Code: "invalid-json-object"}, nil
	}
	for _, predicate := range input.Spec.SemanticPredicates {
		if predicate.Operator != "json-equals" {
			return EngineResult{}, fmt.Errorf("%w: unsupported structured-data operator", ErrInvalidEngineResult)
		}
		var expected any
		expectedDecoder := json.NewDecoder(bytes.NewBufferString(predicate.Expected))
		expectedDecoder.UseNumber()
		if err := expectedDecoder.Decode(&expected); err != nil {
			return EngineResult{}, fmt.Errorf("%w: predicate expected value is not JSON", ErrInvalidEngineResult)
		}
		if err := expectedDecoder.Decode(&extra); err != io.EOF {
			return EngineResult{}, fmt.Errorf("%w: predicate expected value has trailing JSON", ErrInvalidEngineResult)
		}
		actual, present := document[predicate.ID]
		if !present || !reflect.DeepEqual(actual, expected) {
			return EngineResult{Verdict: VerdictFail, Code: "semantic-predicate-failed"}, nil
		}
	}
	return EngineResult{Verdict: VerdictPass, Code: "semantic-predicates-pass"}, nil
}
