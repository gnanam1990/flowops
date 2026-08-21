// Package ascpevents implements the organization-wide append-only ASCP event
// chain and its independently verifiable writer authentication.
package ascpevents

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

const SchemaVersion = 1

var (
	ErrInvalidEvent       = errors.New("invalid ASCP event")
	ErrEventConflict      = errors.New("event ID replayed with different content")
	ErrIntegrity          = errors.New("ASCP event-chain integrity failure")
	ErrUnknownWriterKey   = errors.New("unknown event writer key")
	ErrCheckpointConflict = errors.New("checkpoint replayed with different content")
	zeroHash              = strings.Repeat("0", 64)
	maxSafeJSONInteger    = big.NewInt(9_007_199_254_740_991)
)

type Input struct {
	EventID           string
	OrganizationID    string
	OccurredAt        time.Time
	Type              string
	Actor             string
	CausationID       string
	CorrelationID     string
	EntityRefs        map[string]string
	Payload           any
	SupersedesEventID string
}

type Event struct {
	SchemaVersion     int               `json:"schemaVersion"`
	Sequence          uint64            `json:"seq"`
	EventID           string            `json:"eventId"`
	OrganizationID    string            `json:"orgId"`
	OccurredAtUnixMic int64             `json:"timestampUnixMicros"`
	Type              string            `json:"type"`
	Actor             string            `json:"actor"`
	CausationID       string            `json:"causationId,omitempty"`
	CorrelationID     string            `json:"correlationId"`
	EntityRefs        map[string]string `json:"entityRefs"`
	Payload           json.RawMessage   `json:"payload"`
	SupersedesEventID string            `json:"supersedesEventId,omitempty"`
	PreviousHash      string            `json:"previousHash"`
	EventHash         string            `json:"eventHash"`
	WriterKeyID       string            `json:"writerKeyId"`
	WriterMAC         string            `json:"writerMac"`
}

type Writer struct {
	KeyID string
	Key   []byte
}

type eventHashInput struct {
	SchemaVersion     int               `json:"schemaVersion"`
	Sequence          uint64            `json:"seq"`
	EventID           string            `json:"eventId"`
	OrganizationID    string            `json:"orgId"`
	OccurredAtUnixMic int64             `json:"timestampUnixMicros"`
	Type              string            `json:"type"`
	Actor             string            `json:"actor"`
	CausationID       string            `json:"causationId,omitempty"`
	CorrelationID     string            `json:"correlationId"`
	EntityRefs        map[string]string `json:"entityRefs"`
	Payload           json.RawMessage   `json:"payload"`
	SupersedesEventID string            `json:"supersedesEventId,omitempty"`
	PreviousHash      string            `json:"previousHash"`
	WriterKeyID       string            `json:"writerKeyId"`
}

func NewWriter(keyID string, key []byte) (Writer, error) {
	if !identifier(keyID, 8, 128) || len(key) != 32 {
		return Writer{}, ErrInvalidEvent
	}
	return Writer{KeyID: keyID, Key: append([]byte(nil), key...)}, nil
}

func buildEvent(input Input, sequence uint64, previousHash string, writer Writer) (Event, error) {
	payload, err := canonicalJSON(input.Payload)
	if err != nil || len(payload) == 0 || len(payload) > 1<<20 || !validInput(input) || sequence == 0 || !hash(previousHash) || len(writer.Key) != 32 || !identifier(writer.KeyID, 8, 128) {
		return Event{}, ErrInvalidEvent
	}
	refs := cloneRefs(input.EntityRefs)
	event := Event{
		SchemaVersion: SchemaVersion, Sequence: sequence, EventID: input.EventID,
		OrganizationID: input.OrganizationID, OccurredAtUnixMic: input.OccurredAt.UTC().UnixMicro(),
		Type: input.Type, Actor: input.Actor, CausationID: input.CausationID,
		CorrelationID: input.CorrelationID, EntityRefs: refs, Payload: payload,
		SupersedesEventID: input.SupersedesEventID, PreviousHash: previousHash, WriterKeyID: writer.KeyID,
	}
	event.EventHash, err = calculateEventHash(event)
	if err != nil {
		return Event{}, err
	}
	event.WriterMAC = calculateMAC(writer.Key, event.EventHash)
	return event, nil
}

func VerifyEvent(event Event, expectedSequence uint64, previousHash string, keys map[string][]byte) error {
	if event.SchemaVersion != SchemaVersion || event.Sequence != expectedSequence || event.PreviousHash != previousHash ||
		!validStoredEvent(event) {
		return ErrIntegrity
	}
	wantHash, err := calculateEventHash(event)
	if err != nil || !hmac.Equal([]byte(wantHash), []byte(event.EventHash)) {
		return ErrIntegrity
	}
	key, ok := keys[event.WriterKeyID]
	if !ok || len(key) != 32 {
		return ErrUnknownWriterKey
	}
	wantMAC := calculateMAC(key, event.EventHash)
	if !hmac.Equal([]byte(wantMAC), []byte(event.WriterMAC)) {
		return ErrIntegrity
	}
	return nil
}

func calculateEventHash(event Event) (string, error) {
	encoded, err := canonicalJSON(eventHashInput{
		SchemaVersion: event.SchemaVersion, Sequence: event.Sequence, EventID: event.EventID,
		OrganizationID: event.OrganizationID, OccurredAtUnixMic: event.OccurredAtUnixMic,
		Type: event.Type, Actor: event.Actor, CausationID: event.CausationID,
		CorrelationID: event.CorrelationID, EntityRefs: event.EntityRefs, Payload: event.Payload,
		SupersedesEventID: event.SupersedesEventID, PreviousHash: event.PreviousHash, WriterKeyID: event.WriterKeyID,
	})
	if err != nil {
		return "", fmt.Errorf("canonicalize event hash input: %w", err)
	}
	digest := sha256.Sum256(append([]byte("ASCP_EVENT_V1\x00"), encoded...))
	return hex.EncodeToString(digest[:]), nil
}

func calculateMAC(key []byte, eventHash string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("ASCP_EVENT_MAC_V1\x00" + eventHash))
	return hex.EncodeToString(mac.Sum(nil))
}

func SameEvent(left, right Event) bool {
	leftJSON, leftErr := canonicalJSON(left)
	rightJSON, rightErr := canonicalJSON(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func eventMatchesInput(event Event, input Input) bool {
	payload, err := canonicalJSON(input.Payload)
	refs, refsErr := canonicalJSON(input.EntityRefs)
	storedRefs, storedRefsErr := canonicalJSON(event.EntityRefs)
	return err == nil && refsErr == nil && storedRefsErr == nil && validInput(input) &&
		event.EventID == input.EventID && event.OrganizationID == input.OrganizationID &&
		event.OccurredAtUnixMic == input.OccurredAt.UTC().UnixMicro() && event.Type == input.Type &&
		event.Actor == input.Actor && event.CausationID == input.CausationID &&
		event.CorrelationID == input.CorrelationID && event.SupersedesEventID == input.SupersedesEventID &&
		bytes.Equal(event.Payload, payload) && bytes.Equal(storedRefs, refs)
}

func validInput(input Input) bool {
	if !identifier(input.EventID, 8, 200) || !identifier(input.OrganizationID, 3, 200) ||
		!identifier(input.Type, 3, 200) || !identifier(input.Actor, 3, 200) ||
		!identifier(input.CorrelationID, 8, 200) || input.OccurredAt.IsZero() ||
		input.OccurredAt.UnixMicro() <= 0 || len(input.EntityRefs) > 32 {
		return false
	}
	for key, value := range input.EntityRefs {
		if !identifier(key, 1, 100) || !identifier(value, 1, 300) {
			return false
		}
	}
	for _, value := range []string{input.CausationID, input.SupersedesEventID} {
		if value != "" && !identifier(value, 8, 200) {
			return false
		}
	}
	return true
}

func validStoredEvent(event Event) bool {
	return validInput(Input{EventID: event.EventID, OrganizationID: event.OrganizationID,
		OccurredAt: time.UnixMicro(event.OccurredAtUnixMic), Type: event.Type, Actor: event.Actor,
		CausationID: event.CausationID, CorrelationID: event.CorrelationID, EntityRefs: event.EntityRefs,
		Payload: event.Payload, SupersedesEventID: event.SupersedesEventID}) &&
		hash(event.EventHash) && hash(event.PreviousHash) && hash(event.WriterMAC) &&
		identifier(event.WriterKeyID, 8, 128) && len(event.Payload) > 0
}

func identifier(value string, min, max int) bool {
	if len(value) < min || len(value) > max || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character == 0x7f {
			return false
		}
	}
	return true
}

func hash(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func nonzeroHash(value string) bool { return hash(value) && value != zeroHash }

func cloneRefs(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func cloneEvent(event Event) Event {
	event.EntityRefs = cloneRefs(event.EntityRefs)
	event.Payload = append(json.RawMessage(nil), event.Payload...)
	return event
}

// canonicalJSON is the restricted RFC 8785 profile used by the ASCP wire
// contracts: I-JSON strings, booleans, null, arrays, objects and non-negative
// safe integers. Monetary and chain quantities remain decimal strings.
func canonicalJSON(value any) ([]byte, error) {
	if raw, ok := value.(json.RawMessage); ok {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var decoded any
		if err := decoder.Decode(&decoded); err != nil {
			return nil, ErrInvalidEvent
		}
		if err := requireJSONEOF(decoder); err != nil {
			return nil, ErrInvalidEvent
		}
		var output bytes.Buffer
		if err := writeCanonical(&output, decoded, 0); err != nil {
			return nil, err
		}
		if !bytes.Equal(output.Bytes(), raw) {
			return nil, ErrInvalidEvent
		}
		return output.Bytes(), nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil || requireJSONEOF(decoder) != nil {
		return nil, ErrInvalidEvent
	}
	var output bytes.Buffer
	if err := writeCanonical(&output, decoded, 0); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if !errors.Is(err, io.EOF) {
		return ErrInvalidEvent
	}
	return nil
}

func writeCanonical(output *bytes.Buffer, value any, depth int) error {
	if depth > 128 {
		return ErrInvalidEvent
	}
	switch typed := value.(type) {
	case nil:
		output.WriteString("null")
	case bool:
		output.WriteString(strconv.FormatBool(typed))
	case string:
		if !utf8.ValidString(typed) {
			return ErrInvalidEvent
		}
		writeCanonicalString(output, typed)
	case json.Number:
		integer, ok := new(big.Int).SetString(typed.String(), 10)
		if !ok || integer.Sign() < 0 || integer.Cmp(maxSafeJSONInteger) > 0 || integer.String() != typed.String() {
			return ErrInvalidEvent
		}
		output.WriteString(typed.String())
	case []any:
		output.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				output.WriteByte(',')
			}
			if err := writeCanonical(output, item, depth+1); err != nil {
				return err
			}
		}
		output.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			if !utf8.ValidString(key) {
				return ErrInvalidEvent
			}
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool { return utf16Less(keys[i], keys[j]) })
		output.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				output.WriteByte(',')
			}
			writeCanonicalString(output, key)
			output.WriteByte(':')
			if err := writeCanonical(output, typed[key], depth+1); err != nil {
				return err
			}
		}
		output.WriteByte('}')
	default:
		return ErrInvalidEvent
	}
	return nil
}

func writeCanonicalString(output *bytes.Buffer, value string) {
	const digits = "0123456789abcdef"
	output.WriteByte('"')
	for _, character := range []byte(value) {
		switch character {
		case '"', '\\':
			output.WriteByte('\\')
			output.WriteByte(character)
		case '\b':
			output.WriteString(`\b`)
		case '\t':
			output.WriteString(`\t`)
		case '\n':
			output.WriteString(`\n`)
		case '\f':
			output.WriteString(`\f`)
		case '\r':
			output.WriteString(`\r`)
		default:
			if character < 0x20 {
				output.WriteString(`\u00`)
				output.WriteByte(digits[character>>4])
				output.WriteByte(digits[character&15])
			} else {
				output.WriteByte(character)
			}
		}
	}
	output.WriteByte('"')
}

func utf16Less(left, right string) bool {
	l, r := utf16.Encode([]rune(left)), utf16.Encode([]rune(right))
	for i := 0; i < len(l) && i < len(r); i++ {
		if l[i] != r[i] {
			return l[i] < r[i]
		}
	}
	return len(l) < len(r)
}
