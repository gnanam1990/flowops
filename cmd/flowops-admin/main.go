package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/gnanam1990/flowops/internal/controlapi"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const maxAdminInputBytes = 32 * 1024

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdin, os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "flowops-admin:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	if len(args) != 1 {
		return errors.New("use migrate, sites-bootstrap-owner, sites-rotate-token, or sites-disable-provider")
	}
	var operation func(context.Context, *sql.DB) (any, error)
	switch args[0] {
	case "migrate":
		operation = func(context.Context, *sql.DB) (any, error) {
			return map[string]any{"status": "ok", "migrations": "current"}, nil
		}
	case "sites-bootstrap-owner":
		var request controlapi.SiteOwnerBootstrap
		if err := decodeStrictJSON(input, &request); err != nil {
			return err
		}
		operation = func(operationCtx context.Context, db *sql.DB) (any, error) {
			result, err := controlapi.BootstrapSiteOwner(operationCtx, db, request)
			if err != nil {
				return nil, err
			}
			return struct {
				Status string `json:"status"`
				controlapi.SiteOwnerBootstrapResult
			}{Status: "ok", SiteOwnerBootstrapResult: result}, nil
		}
	case "sites-rotate-token":
		var request controlapi.SiteExchangeTokenRotation
		if err := decodeStrictJSON(input, &request); err != nil {
			return err
		}
		operation = func(operationCtx context.Context, db *sql.DB) (any, error) {
			rotated, err := controlapi.RotateSiteExchangeToken(operationCtx, db, request)
			if err != nil {
				return nil, err
			}
			return map[string]any{"status": "ok", "rotated": rotated}, nil
		}
	case "sites-disable-provider":
		var request controlapi.SiteProviderDisable
		if err := decodeStrictJSON(input, &request); err != nil {
			return err
		}
		operation = func(operationCtx context.Context, db *sql.DB) (any, error) {
			disabled, err := controlapi.DisableSiteIdentityProvider(operationCtx, db, request)
			if err != nil {
				return nil, err
			}
			return map[string]any{"status": "ok", "disabled": disabled}, nil
		}
	default:
		return errors.New("use migrate, sites-bootstrap-owner, sites-rotate-token, or sites-disable-provider")
	}
	databaseURL := strings.TrimSpace(os.Getenv("FLOWOPS_DATABASE_URL"))
	if databaseURL == "" {
		return errors.New("FLOWOPS_DATABASE_URL is required")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open PostgreSQL: %w", err)
	}
	defer db.Close()
	operationCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := db.PingContext(operationCtx); err != nil {
		return fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	if err := controlapi.ApplyMigrations(operationCtx, db); err != nil {
		return err
	}
	result, err := operation(operationCtx, db)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(result)
}

func decodeStrictJSON(input io.Reader, target any) error {
	raw, err := io.ReadAll(io.LimitReader(input, maxAdminInputBytes+1))
	if err != nil {
		return fmt.Errorf("read admin request: %w", err)
	}
	if len(raw) > maxAdminInputBytes {
		return errors.New("admin request exceeds 32 KiB")
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("admin request must contain exactly one JSON object")
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode admin request: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("admin request must contain exactly one JSON object")
	}
	return nil
}

func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var visit func() error
	visit = func() error {
		token, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("decode admin request: %w", err)
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return fmt.Errorf("decode admin request: %w", err)
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("admin request object key is invalid")
				}
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("admin request contains duplicate key %q", key)
				}
				seen[key] = struct{}{}
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
			return errors.New("admin request has an unexpected JSON delimiter")
		}
		if _, err := decoder.Token(); err != nil {
			return fmt.Errorf("decode admin request: %w", err)
		}
		return nil
	}
	if err := visit(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("admin request must contain exactly one JSON object")
	}
	return nil
}
