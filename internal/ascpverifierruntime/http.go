// Package ascpverifierruntime exposes the isolated verifier through a bounded,
// authenticated loopback HTTP boundary. It has no transaction broadcaster.
package ascpverifierruntime

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gnanam1990/flowops/internal/ascpverifier"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	maxRequestBytes = 24 << 20
	requestTimeout  = 5*time.Minute + 5*time.Second
	maxJSONDepth    = 64
	maxConcurrent   = 4
)

var (
	ErrInvalidConfig   = errors.New("invalid verifier runtime configuration")
	ErrUnauthenticated = errors.New("verifier intake authentication failed")
	ErrReplay          = errors.New("verifier intake replay rejected")
	identifierPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{7,127}$`)
	noncePattern       = regexp.MustCompile(`^[A-Za-z0-9_-]{22,128}$`)
)

type Verifier interface {
	VerifyAndSign(context.Context, ascpverifier.Input) (ascpverifier.SignedDecision, error)
}

type ReplayGuard interface {
	Consume(context.Context, string, string, string, time.Time, time.Time) error
}

type PostgresReplayGuard struct{ db *sql.DB }

func NewPostgresReplayGuard(db *sql.DB) (*PostgresReplayGuard, error) {
	if db == nil {
		return nil, ErrInvalidConfig
	}
	return &PostgresReplayGuard{db: db}, nil
}

func (g *PostgresReplayGuard) Consume(ctx context.Context, keyID, nonce, digest string, signedAt, receivedAt time.Time) error {
	_, err := g.db.ExecContext(ctx, `INSERT INTO ascp_verifier_intake_replays
		(key_id,request_nonce,body_digest,signed_at,received_at) VALUES ($1,$2,$3,$4,$5)`,
		keyID, nonce, digest, signedAt.UTC(), receivedAt.UTC())
	if err == nil {
		return nil
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" {
		return ErrReplay
	}
	return fmt.Errorf("record verifier intake replay: %w", err)
}

func (g *PostgresReplayGuard) PruneExpired(ctx context.Context) (int64, error) {
	var deleted int64
	if err := g.db.QueryRowContext(ctx, `SELECT public.prune_ascp_verifier_intake_replays()`).Scan(&deleted); err != nil {
		return 0, fmt.Errorf("prune verifier intake replays: %w", err)
	}
	if deleted < 0 {
		return 0, errors.New("prune verifier intake replays returned a negative count")
	}
	return deleted, nil
}

type HandlerConfig struct {
	Verifier       Verifier
	ReplayGuard    ReplayGuard
	Keys           map[string][]byte
	Clock          func() time.Time
	MaxSkew        time.Duration
	ChainID        string
	EscrowContract string
}

type Handler struct {
	verifier Verifier
	replays  ReplayGuard
	keys     map[string][]byte
	clock    func() time.Time
	maxSkew  time.Duration
	chainID  string
	escrow   string
	slots    chan struct{}
}

func NewHandler(config HandlerConfig) (*Handler, error) {
	if config.Verifier == nil || config.ReplayGuard == nil || config.Clock == nil || config.MaxSkew < time.Second || config.MaxSkew > time.Minute ||
		len(config.Keys) == 0 || config.ChainID == "" || !common.IsHexAddress(config.EscrowContract) ||
		common.HexToAddress(config.EscrowContract) == (common.Address{}) || strings.ToLower(config.EscrowContract) != config.EscrowContract {
		return nil, ErrInvalidConfig
	}
	keys := make(map[string][]byte, len(config.Keys))
	for keyID, key := range config.Keys {
		if !identifierPattern.MatchString(keyID) || len(key) != sha256.Size {
			return nil, ErrInvalidConfig
		}
		keys[keyID] = append([]byte(nil), key...)
	}
	return &Handler{verifier: config.Verifier, replays: config.ReplayGuard, keys: keys, clock: config.Clock,
		maxSkew: config.MaxSkew, chainID: config.ChainID, escrow: config.EscrowContract, slots: make(chan struct{}, maxConcurrent)}, nil
}

func (h *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	requestContext, cancel := context.WithTimeout(request.Context(), requestTimeout)
	defer cancel()
	request = request.WithContext(requestContext)
	if request.URL.Path != "/v1/verdicts" || request.URL.RawQuery != "" || request.Method != http.MethodPost {
		writeError(response, http.StatusNotFound, "NOT_FOUND")
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		writeError(response, http.StatusServiceUnavailable, "VERIFIER_BUSY")
		return
	}
	if request.ContentLength > maxRequestBytes || request.Header.Get("Content-Encoding") != "" {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || strings.ToLower(mediaType) != "application/json" {
		writeError(response, http.StatusUnsupportedMediaType, "JSON_REQUIRED")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(request.Body, maxRequestBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > maxRequestBytes {
		writeError(response, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	if err := h.authenticate(request.Context(), request.Method, request.URL.Path, request.Header, raw); err != nil {
		status := http.StatusUnauthorized
		code := "UNAUTHENTICATED"
		if errors.Is(err, ErrReplay) {
			status, code = http.StatusConflict, "REPLAY_REJECTED"
		} else if !errors.Is(err, ErrUnauthenticated) {
			status, code = http.StatusServiceUnavailable, "AUTH_STATE_UNAVAILABLE"
		}
		writeError(response, status, code)
		return
	}
	if err := rejectDuplicateKeys(raw); err != nil {
		writeError(response, http.StatusBadRequest, "INVALID_JSON")
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var envelope struct {
		RequestID string             `json:"requestId"`
		Input     ascpverifier.Input `json:"input"`
	}
	if err := decoder.Decode(&envelope); err != nil || !identifierPattern.MatchString(envelope.RequestID) {
		writeError(response, http.StatusBadRequest, "INVALID_JSON")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(response, http.StatusBadRequest, "INVALID_JSON")
		return
	}
	if envelope.Input.Commitment.ChainID != h.chainID || envelope.Input.Commitment.EscrowContract != h.escrow {
		writeError(response, http.StatusUnprocessableEntity, "VERIFICATION_REJECTED")
		return
	}
	decision, err := h.verifier.VerifyAndSign(request.Context(), envelope.Input)
	if err != nil {
		status, code := verifierError(err)
		writeError(response, status, code)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(struct {
		RequestID string                      `json:"requestId"`
		Decision  ascpverifier.SignedDecision `json:"decision"`
	}{envelope.RequestID, decision})
}

func (h *Handler) authenticate(ctx context.Context, method, path string, headers http.Header, raw []byte) error {
	for _, name := range []string{"X-FlowOps-Verifier-Key-Id", "X-FlowOps-Verifier-Timestamp", "X-FlowOps-Verifier-Nonce", "X-FlowOps-Verifier-Signature"} {
		if len(headers.Values(name)) != 1 {
			return ErrUnauthenticated
		}
	}
	keyID := headers.Get("X-FlowOps-Verifier-Key-Id")
	timestamp := headers.Get("X-FlowOps-Verifier-Timestamp")
	nonce := headers.Get("X-FlowOps-Verifier-Nonce")
	signature := headers.Get("X-FlowOps-Verifier-Signature")
	key, ok := h.keys[keyID]
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if !ok || !identifierPattern.MatchString(keyID) || !noncePattern.MatchString(nonce) || err != nil || strconv.FormatInt(seconds, 10) != timestamp {
		return ErrUnauthenticated
	}
	now := h.clock().UTC()
	signedAt := time.Unix(seconds, 0).UTC()
	if signedAt.After(now.Add(h.maxSkew)) || signedAt.Before(now.Add(-h.maxSkew)) {
		return ErrUnauthenticated
	}
	digest := sha256.Sum256(raw)
	message := "ASCP_VERIFIER_INTAKE_V2\n" + keyID + "\n" + method + "\n" + path + "\n" +
		timestamp + "\n" + nonce + "\n" + hex.EncodeToString(digest[:])
	want := hmac.New(sha256.New, key)
	_, _ = want.Write([]byte(message))
	provided, err := hex.DecodeString(signature)
	if err != nil || len(provided) != sha256.Size || signature != strings.ToLower(signature) || !hmac.Equal(provided, want.Sum(nil)) {
		return ErrUnauthenticated
	}
	if err := h.replays.Consume(ctx, keyID, nonce, hex.EncodeToString(digest[:]), signedAt, now); err != nil {
		return err
	}
	return nil
}

func rejectDuplicateKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := consumeJSONValue(decoder, 0); err != nil {
		return err
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) || token != nil {
		return errors.New("trailing JSON")
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder, depth int) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if depth >= maxJSONDepth {
		return errors.New("JSON nesting limit exceeded")
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("duplicate JSON key")
			}
			seen[key] = struct{}{}
			if err := consumeJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	closing, err := decoder.Token()
	if err != nil || closing != matchingDelimiter(delimiter) {
		return errors.New("invalid JSON container")
	}
	return nil
}

func matchingDelimiter(open json.Delim) json.Delim {
	if open == '{' {
		return '}'
	}
	return ']'
}

func verifierError(err error) (int, string) {
	switch {
	case errors.Is(err, ascpverifier.ErrDecisionConflict):
		return http.StatusConflict, "DECISION_CONFLICT"
	case errors.Is(err, ascpverifier.ErrStateUnavailable):
		return http.StatusServiceUnavailable, "VERIFIER_STATE_UNAVAILABLE"
	case errors.Is(err, ascpverifier.ErrVerifierInactive):
		return http.StatusServiceUnavailable, "VERIFIER_INACTIVE"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return http.StatusServiceUnavailable, "VERIFICATION_UNAVAILABLE"
	case errors.Is(err, ascpverifier.ErrSigning):
		return http.StatusServiceUnavailable, "SIGNING_UNAVAILABLE"
	default:
		return http.StatusUnprocessableEntity, "VERIFICATION_REJECTED"
	}
}

func writeError(response http.ResponseWriter, status int, code string) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(map[string]string{"error": code})
}

var _ http.Handler = (*Handler)(nil)
