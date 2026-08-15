package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	maxInputBytes    = 32 * 1024
	maxResponseBytes = 256 * 1024
)

type haltInput struct {
	Operator string `json:"operator"`
	Reason   string `json:"reason"`
}

type resumeInput struct {
	Operator string `json:"operator"`
}

type reconciliationInput struct {
	OrganizationID string `json:"organizationId"`
}

type quarantineInput struct {
	OrganizationID string `json:"organizationId"`
	ExecutionID    string `json:"executionId"`
	Operator       string `json:"operator"`
	Disposition    string `json:"disposition"`
	Reason         string `json:"reason"`
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	if len(args) != 1 {
		return usageError()
	}
	baseURL, err := controlAPIURL(os.Getenv("FLOWOPS_CONTROL_API_URL"))
	if err != nil {
		return err
	}
	method, path := http.MethodGet, "/health"
	var body []byte
	if args[0] != "chain-status" {
		method = http.MethodPost
		key := strings.TrimSpace(os.Getenv("FLOWOPS_OPERATOR_CONTROL_KEY_B64"))
		decoded, decodeErr := base64.StdEncoding.DecodeString(key)
		if decodeErr != nil || len(decoded) != 32 {
			return errors.New("FLOWOPS_OPERATOR_CONTROL_KEY_B64 must encode exactly 32 bytes")
		}
		switch args[0] {
		case "chain-halt":
			path = "/v1/operator/chain/halt"
			var request haltInput
			if err := decodeStrict(input, &request); err != nil {
				return err
			}
			body, _ = json.Marshal(request)
		case "chain-resume":
			path = "/v1/operator/chain/resume"
			var request resumeInput
			if err := decodeStrict(input, &request); err != nil {
				return err
			}
			body, _ = json.Marshal(request)
		case "reconciliation-status":
			method = http.MethodGet
			var request reconciliationInput
			if err := decodeStrict(input, &request); err != nil {
				return err
			}
			if strings.TrimSpace(request.OrganizationID) == "" {
				return errors.New("organizationId is required")
			}
			path = "/v1/operator/reconciliation?organizationId=" + url.QueryEscape(request.OrganizationID)
		case "execution-quarantine":
			var request quarantineInput
			if err := decodeStrict(input, &request); err != nil {
				return err
			}
			if strings.TrimSpace(request.ExecutionID) == "" {
				return errors.New("executionId is required")
			}
			path = "/v1/operator/executions/" + url.PathEscape(request.ExecutionID) + "/quarantine"
			body, _ = json.Marshal(struct {
				OrganizationID string `json:"organizationId"`
				Operator       string `json:"operator"`
				Disposition    string `json:"disposition"`
				Reason         string `json:"reason"`
			}{request.OrganizationID, request.Operator, request.Disposition, request.Reason})
		default:
			return usageError()
		}
		return send(ctx, output, method, baseURL+path, key, body)
	}
	if args[0] != "chain-status" {
		return usageError()
	}
	return send(ctx, output, method, baseURL+path, "", nil)
}

func usageError() error {
	return errors.New("usage: flowops-operator chain-status|chain-halt|chain-resume|reconciliation-status|execution-quarantine")
}

func send(ctx context.Context, output io.Writer, method, endpoint, token string, body []byte) error {
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("control API request failed: %w", err)
	}
	defer response.Body.Close()
	if response.Request == nil || response.Request.URL.String() != request.URL.String() {
		return errors.New("control API redirected the request")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > maxResponseBytes {
		return errors.New("control API response exceeds 256 KiB")
	}
	if !json.Valid(raw) {
		return errors.New("control API returned a non-JSON response")
	}
	if _, err := output.Write(append(raw, '\n')); err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("control API returned HTTP %d", response.StatusCode)
	}
	return nil
}

func controlAPIURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("FLOWOPS_CONTROL_API_URL is invalid")
	}
	local := parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1"
	if parsed.Scheme != "https" && !(local && parsed.Scheme == "http") {
		return "", errors.New("FLOWOPS_CONTROL_API_URL must use HTTPS except on loopback")
	}
	return strings.TrimSuffix(parsed.String(), "/"), nil
}

func decodeStrict(input io.Reader, target any) error {
	raw, err := io.ReadAll(io.LimitReader(input, maxInputBytes+1))
	if err != nil {
		return errors.New("stdin could not be read")
	}
	trimmed := bytes.TrimSpace(raw)
	if len(raw) > maxInputBytes || len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("stdin must contain a strict JSON object")
	}
	if err := rejectDuplicateTopLevelFields(raw); err != nil {
		return errors.New("stdin JSON object must not contain duplicate fields")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("stdin must contain a strict JSON object")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("stdin must contain exactly one JSON value")
	}
	return nil
}

func rejectDuplicateTopLevelFields(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return errors.New("not an object")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok {
			return errors.New("invalid object key")
		}
		if _, exists := seen[key]; exists {
			return errors.New("duplicate object field")
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return errors.New("invalid object closing token")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("multiple JSON values")
	}
	return nil
}
