// Package ascpsignerruntime exposes the isolated two-phase signer ledger over
// two distinct Unix-socket HTTP boundaries. It never accepts TCP connections
// and never returns signature bytes on the prepare/activation boundary.
package ascpsignerruntime

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/gnanam1990/flowops/internal/ascpbearer"
)

const (
	SignerProtocol   = "ASCP_BEARER_RUNTIME_V1"
	ArtifactProtocol = "ASCP_KEEPER_BOUNDARY_V1"
	maxBodyBytes     = 2 << 20
)

type Service struct {
	prepared      *ascpbearer.LedgerPreparedSigner
	ledger        *ascpbearer.SignerStore
	artifactToken [32]byte
}

func NewService(prepared *ascpbearer.LedgerPreparedSigner, ledger *ascpbearer.SignerStore, artifactToken []byte) (*Service, error) {
	nonzero := byte(0)
	for _, value := range artifactToken {
		nonzero |= value
	}
	if prepared == nil || ledger == nil || len(artifactToken) != 32 || nonzero == 0 {
		return nil, errors.New("prepared signer, ledger, and 32-byte artifact capability are required")
	}
	service := &Service{prepared: prepared, ledger: ledger}
	copy(service.artifactToken[:], artifactToken)
	return service, nil
}

func (s *Service) SignerHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health(SignerProtocol, "signer"))
	mux.HandleFunc("POST /v1/prepare", s.prepare)
	mux.HandleFunc("POST /v1/acknowledge", s.acknowledge)
	mux.HandleFunc("POST /v1/prove-unactivated", s.proveUnactivated)
	return securityHeaders(mux)
}

func (s *Service) requireArtifactCapability(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.Header.Values("Authorization")
		if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
			writeArtifactUnauthorized(w)
			return
		}
		encoded := strings.TrimPrefix(values[0], "Bearer ")
		if len(encoded) != base64.StdEncoding.EncodedLen(len(s.artifactToken)) {
			writeArtifactUnauthorized(w)
			return
		}
		decoded := make([]byte, base64.StdEncoding.DecodedLen(len(encoded)))
		n, err := base64.StdEncoding.Decode(decoded, []byte(encoded))
		decoded = decoded[:n]
		valid := err == nil && len(decoded) == len(s.artifactToken) &&
			base64.StdEncoding.EncodeToString(decoded) == encoded &&
			subtle.ConstantTimeCompare(decoded, s.artifactToken[:]) == 1
		clear(decoded)
		if !valid {
			writeArtifactUnauthorized(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeArtifactUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="ascp-artifact"`)
	writeFailure(w, http.StatusUnauthorized, "ARTIFACT_UNAUTHORIZED")
}

func (s *Service) ArtifactHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health(ArtifactProtocol, "artifact"))
	mux.HandleFunc("POST /v1/release", s.release)
	return securityHeaders(s.requireArtifactCapability(mux))
}

func health(protocol, boundary string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, struct {
			Protocol string `json:"protocol"`
			Boundary string `json:"boundary"`
			Status   string `json:"status"`
		}{protocol, boundary, "ok"})
	}
}

func (s *Service) prepare(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Protocol string                     `json:"protocol"`
		Input    ascpbearer.ActivationInput `json:"input"`
	}
	if decodeRequest(w, r, &request) != nil || request.Protocol != SignerProtocol {
		writeFailure(w, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	handleID, err := s.prepared.Prepare(r.Context(), request.Input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		HandleID string `json:"handleId"`
	}{handleID})
}

func (s *Service) acknowledge(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Protocol string                     `json:"protocol"`
		Proof    ascpbearer.ActivationProof `json:"proof"`
	}
	if decodeRequest(w, r, &request) != nil || request.Protocol != SignerProtocol {
		writeFailure(w, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	if err := s.prepared.AcknowledgeActivation(r.Context(), request.Proof); err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Acknowledged bool `json:"acknowledged"`
	}{true})
}

func (s *Service) proveUnactivated(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Protocol    string `json:"protocol"`
		RequestID   string `json:"requestId"`
		OperationID string `json:"operationId"`
		ActionID    string `json:"actionId"`
		InputHash   string `json:"inputHash"`
	}
	if decodeRequest(w, r, &request) != nil || request.Protocol != SignerProtocol {
		writeFailure(w, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	proof, err := s.ledger.ProveAndExpireUnactivated(r.Context(), request.RequestID, request.OperationID, request.ActionID, request.InputHash)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Proof ascpbearer.UnactivatedProof `json:"proof"`
	}{proof})
}

func (s *Service) release(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Protocol string `json:"protocol"`
		HandleID string `json:"handleId"`
		KeeperID string `json:"keeperId"`
	}
	if decodeRequest(w, r, &request) != nil || request.Protocol != ArtifactProtocol {
		writeFailure(w, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	handle, artifact, err := s.ledger.Release(r.Context(), request.HandleID, request.KeeperID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	defer clear(artifact)
	writeJSON(w, http.StatusOK, struct {
		Handle   ascpbearer.Handle `json:"handle"`
		Artifact []byte            `json:"artifact"`
	}{handle, artifact})
}

func decodeRequest(w http.ResponseWriter, r *http.Request, output any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || strings.ToLower(mediaType) != "application/json" {
		return errors.New("request content type must be JSON")
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	raw, err := io.ReadAll(r.Body)
	defer clear(raw)
	if err != nil || len(raw) == 0 || len(raw) > maxBodyBytes {
		return errors.New("request body is invalid or too large")
	}
	if err := rejectDuplicateKeys(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request must contain exactly one JSON value")
	}
	return nil
}

func rejectDuplicateKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var visit func(int) error
	visit = func(depth int) error {
		if depth > 64 {
			return errors.New("JSON nesting exceeds 64 levels")
		}
		token, err := decoder.Token()
		if err != nil {
			return errors.New("invalid JSON")
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				key, ok := keyToken.(string)
				if err != nil || !ok {
					return errors.New("invalid JSON object")
				}
				if _, exists := seen[key]; exists {
					return errors.New("duplicate JSON field")
				}
				seen[key] = struct{}{}
				if err := visit(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err := visit(depth + 1); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid JSON delimiter")
		}
		_, err = decoder.Token()
		return err
	}
	if err := visit(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("request must contain exactly one JSON value")
	}
	return nil
}

func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ascpbearer.ErrSignerRefused):
		writeFailure(w, http.StatusUnprocessableEntity, "SIGNER_REFUSED")
	case errors.Is(err, ascpbearer.ErrActivationInput):
		writeFailure(w, http.StatusBadRequest, "INVALID_REQUEST")
	case errors.Is(err, ascpbearer.ErrKeeper):
		writeFailure(w, http.StatusForbidden, "KEEPER_UNAUTHORIZED")
	case errors.Is(err, ascpbearer.ErrMismatch), errors.Is(err, ascpbearer.ErrActivationBinding):
		writeFailure(w, http.StatusConflict, "BINDING_MISMATCH")
	case errors.Is(err, ascpbearer.ErrTransition), errors.Is(err, ascpbearer.ErrActivationState):
		writeFailure(w, http.StatusConflict, "STATE_CONFLICT")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeFailure(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE")
	default:
		writeFailure(w, http.StatusServiceUnavailable, "VERIFICATION_REFUSED")
	}
}

func writeFailure(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, struct {
		Code string `json:"code"`
	}{code})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}
