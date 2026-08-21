package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpleadership"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const maxLeadershipInputBytes = 32 * 1024

type request struct {
	OrganizationID string  `json:"organizationId"`
	ExpectedEpoch  *uint64 `json:"expectedEpoch,omitempty"`
	Actor          *string `json:"actor,omitempty"`
	EvidenceDigest *string `json:"evidenceDigest,omitempty"`
	EffectID       *string `json:"effectId,omitempty"`
	fields         map[string]struct{}
}

type response struct {
	Status                   string               `json:"status"`
	OrganizationID           string               `json:"organizationId"`
	Epoch                    uint64               `json:"epoch"`
	State                    ascpleadership.State `json:"state"`
	EvidenceDigest           string               `json:"evidenceDigest"`
	Actor                    string               `json:"actor"`
	UpdatedAt                time.Time            `json:"updatedAt"`
	EffectID                 string               `json:"effectId,omitempty"`
	ResolutionActor          string               `json:"resolutionActor,omitempty"`
	ResolutionEvidenceDigest string               `json:"resolutionEvidenceDigest,omitempty"`
	InFlightEffectIDs        []string             `json:"inFlightEffectIds,omitempty"`
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdin, os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "ascp-leadership:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	if len(args) != 1 || !validCommand(args[0]) {
		return errors.New("use status, bootstrap, drain, advance, or abandon-effect")
	}
	var request request
	if err := decodeStrict(input, &request); err != nil {
		return err
	}
	if err := request.validateFor(args[0]); err != nil {
		return err
	}
	databaseURL := strings.TrimSpace(os.Getenv("FLOWOPS_LEADERSHIP_DATABASE_URL"))
	if err := validateLeadershipURL(databaseURL); err != nil {
		return err
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open leadership PostgreSQL: %w", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(4)
	operationCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := db.PingContext(operationCtx); err != nil {
		return fmt.Errorf("connect to leadership PostgreSQL: %w", err)
	}
	store, err := ascpleadership.NewPostgres(db, "public")
	if err != nil {
		return err
	}
	var record ascpleadership.Record
	switch args[0] {
	case "status":
		record, err = store.Get(operationCtx, request.OrganizationID)
	case "bootstrap":
		record, err = store.Bootstrap(operationCtx, request.OrganizationID, *request.Actor, *request.EvidenceDigest)
	case "drain":
		record, err = store.BeginDrain(operationCtx, request.OrganizationID, *request.ExpectedEpoch, *request.Actor, *request.EvidenceDigest)
	case "advance":
		record, err = store.Advance(operationCtx, request.OrganizationID, *request.ExpectedEpoch, *request.Actor, *request.EvidenceDigest)
	case "abandon-effect":
		err = store.AbandonEffect(operationCtx, request.OrganizationID, *request.ExpectedEpoch, *request.EffectID, *request.Actor, *request.EvidenceDigest)
		if err == nil {
			record, err = store.Get(operationCtx, request.OrganizationID)
		}
	}
	if err != nil {
		return err
	}
	result := response{
		Status: "ok", OrganizationID: record.OrganizationID, Epoch: record.Epoch,
		State: record.State, EvidenceDigest: record.EvidenceDigest, Actor: record.Actor, UpdatedAt: record.UpdatedAt,
	}
	if args[0] == "abandon-effect" {
		result.EffectID = *request.EffectID
		result.ResolutionActor = *request.Actor
		result.ResolutionEvidenceDigest = *request.EvidenceDigest
	}
	if args[0] == "status" || args[0] == "abandon-effect" {
		result.InFlightEffectIDs, err = store.InFlightEffectIDs(operationCtx, record.OrganizationID, record.Epoch)
		if err != nil {
			return err
		}
	}
	return json.NewEncoder(output).Encode(result)
}

func (r request) validateFor(command string) error {
	if strings.TrimSpace(r.OrganizationID) == "" {
		return errors.New("organizationId is required")
	}
	switch command {
	case "status":
		if r.has("expectedEpoch") || r.has("actor") || r.has("evidenceDigest") || r.has("effectId") {
			return errors.New("status accepts only organizationId")
		}
	case "bootstrap":
		if r.has("expectedEpoch") || !r.present("actor", r.Actor) || !r.present("evidenceDigest", r.EvidenceDigest) || r.has("effectId") {
			return errors.New("bootstrap requires organizationId, actor, and evidenceDigest only")
		}
	case "drain", "advance":
		if !r.positiveEpoch() || !r.present("actor", r.Actor) || !r.present("evidenceDigest", r.EvidenceDigest) || r.has("effectId") {
			return errors.New(command + " requires organizationId, expectedEpoch, actor, and evidenceDigest")
		}
	case "abandon-effect":
		if !r.positiveEpoch() || !r.present("actor", r.Actor) || !r.present("evidenceDigest", r.EvidenceDigest) || !r.present("effectId", r.EffectID) {
			return errors.New("abandon-effect requires organizationId, expectedEpoch, effectId, actor, and evidenceDigest")
		}
	default:
		return errors.New("use status, bootstrap, drain, advance, or abandon-effect")
	}
	return nil
}

func (r request) has(field string) bool {
	_, ok := r.fields[field]
	return ok
}

func (r request) present(field string, value *string) bool {
	return r.has(field) && value != nil && *value != ""
}

func (r request) positiveEpoch() bool {
	return r.has("expectedEpoch") && r.ExpectedEpoch != nil && *r.ExpectedEpoch > 0
}

func validCommand(command string) bool {
	switch command {
	case "status", "bootstrap", "drain", "advance", "abandon-effect":
		return true
	default:
		return false
	}
}

func validateLeadershipURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || parsed.Fragment != "" || parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return errors.New("FLOWOPS_LEADERSHIP_DATABASE_URL must be a PostgreSQL URL")
	}
	modes := parsed.Query()["sslmode"]
	if len(modes) != 1 || modes[0] != "verify-full" {
		return errors.New("FLOWOPS_LEADERSHIP_DATABASE_URL must set sslmode=verify-full exactly once")
	}
	return nil
}

func decodeStrict(input io.Reader, target any) error {
	raw, err := io.ReadAll(io.LimitReader(input, maxLeadershipInputBytes+1))
	if err != nil {
		return errors.New("leadership request could not be read")
	}
	trimmed := bytes.TrimSpace(raw)
	if len(raw) > maxLeadershipInputBytes || len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("leadership request must be one JSON object of at most 32 KiB")
	}
	if err := rejectDuplicateKeys(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("leadership request must match the strict JSON contract")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("leadership request must contain exactly one JSON value")
	}
	if requestTarget, ok := target.(*request); ok {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return errors.New("leadership request must contain valid JSON")
		}
		requestTarget.fields = make(map[string]struct{}, len(fields))
		for field := range fields {
			requestTarget.fields[field] = struct{}{}
		}
	}
	return nil
}

func rejectDuplicateKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var visit func() error
	visit = func() error {
		token, err := decoder.Token()
		if err != nil {
			return errors.New("leadership request must contain valid JSON")
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return errors.New("leadership request must contain valid JSON")
				}
				key, ok := keyToken.(string)
				if !ok || seen[key] {
					return errors.New("leadership request must not contain duplicate fields")
				}
				seen[key] = true
				if err := visit(); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err := visit(); err != nil {
					return err
				}
			}
		default:
			return errors.New("leadership request must contain valid JSON")
		}
		if _, err := decoder.Token(); err != nil {
			return errors.New("leadership request must contain valid JSON")
		}
		return nil
	}
	if err := visit(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("leadership request must contain exactly one JSON value")
	}
	return nil
}
