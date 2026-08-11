package evidencefetch

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

const maxRequestBytes = 16 << 10

func Handler(service *Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/fetch", func(writer http.ResponseWriter, request *http.Request) {
		if contentType := request.Header.Get("Content-Type"); !strings.HasPrefix(strings.ToLower(contentType), "application/json") {
			writeError(writer, http.StatusUnsupportedMediaType, newError(CodeInvalidRequest, "Content-Type must be application/json", nil))
			return
		}
		var input Request
		decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, maxRequestBytes))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(writer, http.StatusBadRequest, newError(CodeInvalidRequest, "request body must be a valid JSON object", err))
			return
		}
		if err := ensureEOF(decoder); err != nil {
			writeError(writer, http.StatusBadRequest, newError(CodeInvalidRequest, "request body must contain one JSON object", err))
			return
		}
		result, err := service.Fetch(request.Context(), input)
		if err != nil {
			writeError(writer, statusFor(err), err)
			return
		}
		writeJSON(writer, http.StatusOK, result)
	})
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
	})
	return mux
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("extra JSON value")
		}
		return err
	}
	return nil
}

func statusFor(err error) int {
	switch ErrorCode(err) {
	case CodeInvalidRequest:
		return http.StatusBadRequest
	case CodeUnsafeURL:
		return http.StatusUnprocessableEntity
	case CodeResolutionFailed, CodeUpstreamFailure:
		return http.StatusBadGateway
	case CodeUnsupportedContent:
		return http.StatusUnsupportedMediaType
	case CodeResponseTooLarge:
		return http.StatusRequestEntityTooLarge
	case CodeEmptyEvidence:
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
}

func writeError(writer http.ResponseWriter, status int, err error) {
	var typed *Error
	if !errors.As(err, &typed) {
		typed = newError(CodeUpstreamFailure, "request failed", err)
	}
	writeJSON(writer, status, map[string]any{"error": typed})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
