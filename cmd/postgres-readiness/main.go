package main

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/gnanam1990/flowops/internal/dbreadiness"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, _ io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: postgres-readiness sql | provider-evidence-sign | provider-evidence")
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	switch args[0] {
	case "sql":
		rawURL := strings.TrimSpace(os.Getenv("FLOWOPS_DATABASE_URL"))
		if err := dbreadiness.ValidateRuntimeURL(rawURL); err != nil {
			return err
		}
		db, err := sql.Open("pgx", rawURL)
		if err != nil {
			return fmt.Errorf("open PostgreSQL: %w", err)
		}
		defer db.Close()
		checkCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		report, err := dbreadiness.VerifyRuntimeSQL(checkCtx, db)
		if err != nil {
			return err
		}
		if err := encoder.Encode(report); err != nil {
			return err
		}
		if !report.Ready {
			return errors.New("managed PostgreSQL runtime SQL readiness is blocked")
		}
		return nil
	case "provider-evidence":
		encodedKey := strings.TrimSpace(os.Getenv("FLOWOPS_DB_EVIDENCE_PUBLIC_KEY_B64"))
		publicKey, err := base64.StdEncoding.DecodeString(encodedKey)
		if err != nil || len(publicKey) != ed25519.PublicKeySize {
			return errors.New("FLOWOPS_DB_EVIDENCE_PUBLIC_KEY_B64 must contain a 32-byte Ed25519 public key")
		}
		data, err := readLimited(stdin)
		if err != nil {
			return err
		}
		evidence, err := dbreadiness.DecodeProviderEvidence(data)
		if err != nil {
			return err
		}
		report := dbreadiness.VerifyProviderEvidence(evidence, ed25519.PublicKey(publicKey), time.Now().UTC())
		if err := encoder.Encode(report); err != nil {
			return err
		}
		if !report.Ready {
			return errors.New("managed PostgreSQL provider evidence is blocked")
		}
		return nil
	case "provider-evidence-sign":
		encodedKey := strings.TrimSpace(os.Getenv("FLOWOPS_DB_EVIDENCE_PRIVATE_KEY_B64"))
		privateKey, err := base64.StdEncoding.DecodeString(encodedKey)
		if err != nil || len(privateKey) != ed25519.PrivateKeySize {
			return errors.New("FLOWOPS_DB_EVIDENCE_PRIVATE_KEY_B64 must contain a 64-byte Ed25519 private key")
		}
		data, err := readLimited(stdin)
		if err != nil {
			return err
		}
		evidence, err := dbreadiness.DecodeProviderEvidence(data)
		if err != nil {
			return err
		}
		if evidence.Signature != "" {
			return errors.New("unsigned provider evidence input must have an empty signature")
		}
		signed, err := dbreadiness.SignProviderEvidence(evidence, ed25519.PrivateKey(privateKey))
		if err != nil {
			return err
		}
		return encoder.Encode(signed)
	default:
		return errors.New("usage: postgres-readiness sql | provider-evidence-sign | provider-evidence")
	}
}

func readLimited(input io.Reader) ([]byte, error) {
	const max = 1024 * 1024
	data, err := io.ReadAll(io.LimitReader(input, max+1))
	if err != nil {
		return nil, err
	}
	if len(data) > max {
		return nil, errors.New("provider evidence exceeds 1 MiB")
	}
	return data, nil
}
