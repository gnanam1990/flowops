package ascpring6

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/gnanam1990/flowops/internal/ascpbearer"
)

const maxBodyBytes = 2 << 20

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, struct {
			Protocol string `json:"protocol"`
			Boundary string `json:"boundary"`
			Status   string `json:"status"`
		}{Protocol, "ring6", "ok"})
	})
	mux.HandleFunc("POST /v1/verify-and-sign", s.verifyAndSign)
	return securityHeaders(mux)
}

func (s *Service) verifyAndSign(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Protocol string                     `json:"protocol"`
		Input    ascpbearer.ActivationInput `json:"input"`
	}
	defer func() {
		clear(request.Input.CanonicalPayload)
		clear(request.Input.EvidenceBundle)
	}()
	if decodeRequest(w, r, &request) != nil || request.Protocol != Protocol {
		writeFailure(w, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	signature, err := s.VerifyAndSign(r.Context(), request.Input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	defer clear(signature)
	writeJSON(w, http.StatusOK, struct {
		Signature []byte `json:"signature"`
	}{signature})
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
	return decodeStrict(raw, output)
}

func decodeStrict(raw []byte, output any) error {
	if err := rejectDuplicateKeys(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("JSON must contain exactly one value")
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
		return errors.New("JSON must contain exactly one value")
	}
	return nil
}

func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrRefused):
		writeFailure(w, http.StatusUnprocessableEntity, "SIGNER_REFUSED")
	case errors.Is(err, ascpbearer.ErrActivationInput):
		writeFailure(w, http.StatusBadRequest, "INVALID_REQUEST")
	case errors.Is(err, ErrBinding):
		writeFailure(w, http.StatusConflict, "BINDING_MISMATCH")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeFailure(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE")
	default:
		writeFailure(w, http.StatusServiceUnavailable, "SIGNER_UNAVAILABLE")
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
