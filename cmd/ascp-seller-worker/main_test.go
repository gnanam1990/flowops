package main

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestLoadConfigAcceptsCompleteFailClosedWorker(t *testing.T) {
	setValidEnvironment(t)
	config, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.chainID != 84532 || config.rpcQuorum != 2 || len(config.rpcProviders) != 2 || len(config.integrityKeys) != 1 || config.workerID != "seller-worker-1" {
		t.Fatalf("config=%+v", config)
	}
}

func TestStartupIntegrityReceivesConfiguredTimeoutAboveDatabaseBudget(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("FLOWOPS_SELLER_INTEGRITY_TIMEOUT", "20s")
	t.Setenv("FLOWOPS_SELLER_RPC_REQUEST_TIMEOUT", "1s")
	t.Setenv("FLOWOPS_SELLER_HTTP_TIMEOUT", "1s")
	t.Setenv("FLOWOPS_SELLER_CYCLE_TIMEOUT", "50s")
	t.Setenv("FLOWOPS_SELLER_INTERVAL", "55s")
	t.Setenv("FLOWOPS_SELLER_LEASE_DURATION", "55s")
	config, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	gate := &deadlineIntegrityGate{}
	started := time.Now()
	if err := checkStartupIntegrity(t.Context(), gate, config.integrityTimeout); err != nil {
		t.Fatal(err)
	}
	if gate.deadline.Before(started.Add(19*time.Second)) || gate.deadline.After(started.Add(21*time.Second)) {
		t.Fatalf("integrity deadline=%s configured=%s", gate.deadline, config.integrityTimeout)
	}
}

func TestRequirePublicSchemaFailsClosed(t *testing.T) {
	tests := []struct {
		name     string
		schema   string
		queryErr error
		wantErr  bool
	}{
		{name: "public", schema: "public"},
		{name: "alternate schema", schema: "tenant_1", wantErr: true},
		{name: "lookup failure", queryErr: errors.New("connection lost"), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			expectation := mock.ExpectQuery(`SELECT current_schema()`)
			if test.queryErr != nil {
				expectation.WillReturnError(test.queryErr)
			} else {
				expectation.WillReturnRows(sqlmock.NewRows([]string{"current_schema"}).AddRow(test.schema))
			}
			if err := requirePublicSchema(t.Context(), db); (err != nil) != test.wantErr {
				t.Fatalf("error=%v wantErr=%v", err, test.wantErr)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
	if err := requirePublicSchema(t.Context(), (*sql.DB)(nil)); err == nil {
		t.Fatal("nil database accepted")
	}
}

func TestLoadConfigRejectsUnsafeDatabaseProviderAndTiming(t *testing.T) {
	tests := []struct {
		name  string
		env   string
		value string
	}{
		{name: "database without TLS verification", env: "FLOWOPS_RAILS_DATABASE_URL", value: "postgresql" + "://rails@db.example/flowops?sslmode=require"},
		{name: "database without path", env: "FLOWOPS_RAILS_DATABASE_URL", value: "postgresql" + "://rails@db.example?sslmode=verify-full"},
		{name: "database query host override", env: "FLOWOPS_RAILS_DATABASE_URL", value: "postgresql" + "://rails@db.example/flowops?sslmode=verify-full&host=127.0.0.1"},
		{name: "database search path override", env: "FLOWOPS_RAILS_DATABASE_URL", value: "postgresql" + "://rails@db.example/flowops?sslmode=verify-full&search_path=tenant_1"},
		{name: "database options override", env: "FLOWOPS_RAILS_DATABASE_URL", value: "postgresql" + "://rails@db.example/flowops?sslmode=verify-full&options=-csearch_path%3Dtenant_1"},
		{name: "one RPC", env: "FLOWOPS_SELLER_RPC_PROVIDERS_JSON", value: `[{"name":"rpc-a","url":"https://rpc-a.example/v1"}]`},
		{name: "private RPC", env: "FLOWOPS_SELLER_RPC_PROVIDERS_JSON", value: `[{"name":"rpc-a","url":"https://127.0.0.1/v1"},{"name":"rpc-b","url":"https://rpc-b.example/v1"}]`},
		{name: "alternate RPC port", env: "FLOWOPS_SELLER_RPC_PROVIDERS_JSON", value: `[{"name":"rpc-a","url":"https://rpc-a.example:8443/v1"},{"name":"rpc-b","url":"https://rpc-b.example/v1"}]`},
		{name: "duplicate RPC host", env: "FLOWOPS_SELLER_RPC_PROVIDERS_JSON", value: `[{"name":"rpc-a","url":"https://rpc.example/a"},{"name":"rpc-b","url":"https://rpc.example/b"}]`},
		{name: "quorum exceeds providers", env: "FLOWOPS_SELLER_RPC_QUORUM", value: "3"},
		{name: "cycle reaches interval", env: "FLOWOPS_SELLER_CYCLE_TIMEOUT", value: "50s"},
		{name: "HTTP exceeds lease budget", env: "FLOWOPS_SELLER_HTTP_TIMEOUT", value: "46s"},
		{name: "RPC exceeds cycle budget", env: "FLOWOPS_SELLER_RPC_REQUEST_TIMEOUT", value: "8s"},
		{name: "unbounded chain lag", env: "FLOWOPS_SELLER_MAX_CHAIN_LAG", value: "11m"},
		{name: "invalid worker ID", env: "FLOWOPS_SELLER_WORKER_ID", value: "worker id"},
		{name: "HTTP integrity endpoint", env: "FLOWOPS_SELLER_INTEGRITY_URL", value: "http://integrity.example/latest"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setValidEnvironment(t)
			t.Setenv(test.env, test.value)
			if _, err := loadConfig(); err == nil {
				t.Fatalf("unsafe %s accepted", test.env)
			}
		})
	}
}

func TestStrictConfigJSONRejectsUnknownAndTrailingValues(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("FLOWOPS_SELLER_RPC_PROVIDERS_JSON", `[{"name":"rpc-a","url":"https://rpc-a.example","unknown":true},{"name":"rpc-b","url":"https://rpc-b.example"}]`)
	if _, err := loadConfig(); err == nil {
		t.Fatal("unknown RPC field accepted")
	}
	setValidEnvironment(t)
	t.Setenv("FLOWOPS_SELLER_RPC_PROVIDERS_JSON", `[{"name":"rpc-a","url":"https://rpc-a.example"},{"name":"rpc-b","url":"https://rpc-b.example"}] {}`)
	if _, err := loadConfig(); err == nil {
		t.Fatal("trailing RPC JSON accepted")
	}
	setValidEnvironment(t)
	t.Setenv("FLOWOPS_SELLER_RPC_PROVIDERS_JSON", `[{"name":"rpc-a","name":"rpc-b","url":"https://rpc-a.example"},{"name":"rpc-c","url":"https://rpc-c.example"}]`)
	if _, err := loadConfig(); err == nil {
		t.Fatal("duplicate RPC field accepted")
	}
	setValidEnvironment(t)
	t.Setenv("FLOWOPS_SELLER_RPC_PROVIDERS_JSON", `[{"Name":"rpc-a","url":"https://rpc-a.example"},{"name":"rpc-b","url":"https://rpc-b.example"}]`)
	if _, err := loadConfig(); err == nil {
		t.Fatal("non-canonical RPC field accepted")
	}
	setValidEnvironment(t)
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", ed25519.PublicKeySize)))
	t.Setenv("FLOWOPS_SELLER_INTEGRITY_KEYS_JSON", `{"recovery-key-1":"`+key+`"} {}`)
	if _, err := loadConfig(); err == nil {
		t.Fatal("trailing integrity-key JSON accepted")
	}
}

func setValidEnvironment(t *testing.T) {
	t.Helper()
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", ed25519.PublicKeySize)))
	t.Setenv("FLOWOPS_RAILS_DATABASE_URL", "postgresql"+"://rails@db.example/flowops?sslmode=verify-full")
	t.Setenv("FLOWOPS_SELLER_WORKER_ID", "seller-worker-1")
	t.Setenv("FLOWOPS_SELLER_CHAIN_ID", "84532")
	t.Setenv("FLOWOPS_SELLER_RPC_PROVIDERS_JSON", `[{"name":"rpc-a","url":"https://rpc-a.example/v1"},{"name":"rpc-b","url":"https://rpc-b.example/v1"}]`)
	t.Setenv("FLOWOPS_SELLER_RPC_QUORUM", "2")
	t.Setenv("FLOWOPS_SELLER_INTEGRITY_URL", "https://integrity.example/v1/recovery")
	t.Setenv("FLOWOPS_SELLER_INTEGRITY_KEYS_JSON", `{"recovery-key-1":"`+key+`"}`)
	for _, name := range []string{"FLOWOPS_SELLER_INTEGRITY_TIMEOUT", "FLOWOPS_SELLER_INTEGRITY_MAX_TTL", "FLOWOPS_SELLER_INTERVAL",
		"FLOWOPS_SELLER_CYCLE_TIMEOUT", "FLOWOPS_SELLER_LEASE_DURATION", "FLOWOPS_SELLER_HTTP_TIMEOUT",
		"FLOWOPS_SELLER_RETRY_DELAY", "FLOWOPS_SELLER_MAX_OBSERVATION_AGE", "FLOWOPS_SELLER_BATCH_SIZE", "FLOWOPS_SELLER_RPC_REQUEST_TIMEOUT",
		"FLOWOPS_SELLER_MAX_CHAIN_LAG"} {
		t.Setenv(name, "")
	}
}

type deadlineIntegrityGate struct{ deadline time.Time }

func (g *deadlineIntegrityGate) Check(ctx context.Context) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		return context.DeadlineExceeded
	}
	g.deadline = deadline
	return nil
}
