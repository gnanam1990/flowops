package ascpevents

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestEventHashMACReplayAndKeyRotation(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 123456000, time.UTC)
	writerA, _ := NewWriter("writer-key-a", []byte(strings.Repeat("a", 32)))
	writerB, _ := NewWriter("writer-key-b", []byte(strings.Repeat("b", 32)))
	first, err := buildEvent(testInput("event_0001", now, map[string]any{"amount": "100", "ok": true}), 1, zeroHash, writerA)
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildEvent(testInput("event_0002", now.Add(time.Second), map[string]any{"amount": "200"}), 2, first.EventHash, writerB)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string][]byte{writerA.KeyID: writerA.Key, writerB.KeyID: writerB.Key}
	if err := VerifyEvent(first, 1, zeroHash, keys); err != nil {
		t.Fatal(err)
	}
	if err := VerifyEvent(second, 2, first.EventHash, keys); err != nil {
		t.Fatal(err)
	}

	mutations := []struct {
		name   string
		mutate func(*Event)
	}{
		{"payload", func(event *Event) { event.Payload = json.RawMessage(`{"amount":"999"}`) }},
		{"actor", func(event *Event) { event.Actor = "attacker" }},
		{"previous hash", func(event *Event) { event.PreviousHash = strings.Repeat("1", 64) }},
		{"writer MAC", func(event *Event) { event.WriterMAC = strings.Repeat("0", 64) }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			changed := cloneEvent(second)
			mutation.mutate(&changed)
			if err := VerifyEvent(changed, 2, first.EventHash, keys); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	if err := VerifyEvent(second, 2, first.EventHash, map[string][]byte{writerA.KeyID: writerA.Key}); !errors.Is(err, ErrUnknownWriterKey) {
		t.Fatalf("unknown key error=%v", err)
	}
}

func TestCanonicalPayloadRejectsAmbiguityAndUsesUTF16Ordering(t *testing.T) {
	canonical, err := canonicalJSON(map[string]any{"z": uint64(2), "a": "\u2028", "nested": map[string]any{"b": true, "a": nil}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(canonical), `{"a":" ","nested":{"a":null,"b":true},"z":2}`; got != want {
		t.Fatalf("canonical=%q want=%q", got, want)
	}
	bad := []json.RawMessage{
		json.RawMessage(` {"a":1}`), json.RawMessage(`{"a":1,"a":1}`), json.RawMessage(`{"a":-1}`),
		json.RawMessage(`{"a":1.0}`), json.RawMessage(`{"a":9007199254740992}`), json.RawMessage(`{"a":1}{}`),
	}
	for _, raw := range bad {
		if _, err := canonicalJSON(raw); !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("raw=%s error=%v", raw, err)
		}
	}
}

func TestInvalidEventAndWriterInputsFailClosed(t *testing.T) {
	if _, err := NewWriter("short", make([]byte, 32)); !errors.Is(err, ErrInvalidEvent) {
		t.Fatal(err)
	}
	if _, err := NewWriter("writer-key-a", make([]byte, 31)); !errors.Is(err, ErrInvalidEvent) {
		t.Fatal(err)
	}
	writer, _ := NewWriter("writer-key-a", []byte(strings.Repeat("a", 32)))
	inputs := []Input{
		{},
		testInput("short", time.Now(), map[string]any{"ok": true}),
		testInput("event_0001", time.Time{}, map[string]any{"ok": true}),
		testInput("event_0001", time.Now(), json.RawMessage(`{"b":1,"a":2}`)),
	}
	for _, input := range inputs {
		if _, err := buildEvent(input, 1, zeroHash, writer); !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("input=%+v error=%v", input, err)
		}
	}
}

func testInput(id string, at time.Time, payload any) Input {
	return Input{EventID: id, OrganizationID: "org-test", OccurredAt: at, Type: "payment.authorized",
		Actor: "agent-test", CausationID: "cause_0001", CorrelationID: "correlation_0001",
		EntityRefs: map[string]string{"operationId": "operation_0001"}, Payload: payload}
}
