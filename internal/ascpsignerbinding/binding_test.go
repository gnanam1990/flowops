package ascpsignerbinding

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPutRejectsUnsafeRoutingBeforeDatabaseAccess(t *testing.T) {
	store := &Store{chainID: 84532}
	valid := PutRequest{
		SignerKeyID: "signer-key-1", KeyEpoch: 1,
		ModuleAddress: "0x1111111111111111111111111111111111111111",
		SafeAddress:   "0x2222222222222222222222222222222222222222",
		KeeperID:      "keeper-primary", Reason: "Initial binding",
	}
	for name, mutate := range map[string]func(*PutRequest){
		"zero module": func(request *PutRequest) { request.ModuleAddress = "0x" + strings.Repeat("0", 40) },
		"zero safe":   func(request *PutRequest) { request.SafeAddress = "0x" + strings.Repeat("0", 40) },
		"same address": func(request *PutRequest) {
			request.SafeAddress = request.ModuleAddress
		},
		"zero epoch": func(request *PutRequest) { request.KeyEpoch = 0 },
		"blank reason": func(request *PutRequest) {
			request.Reason = "  "
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := valid
			mutate(&request)
			if _, err := store.Put(context.Background(), "org_a", "agent_a", "owner_a", "binding_v1", request); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestExactReplayDoesNotRequireFreshEntropy(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Unix(1_800_000_000, 0).UTC()
	store := &Store{db: db, chainID: 84532, clock: func() time.Time { return now }, random: bytes.NewReader(nil)}
	request := PutRequest{
		SignerKeyID: "signer-key-1", KeyEpoch: 1,
		ModuleAddress: "0x1111111111111111111111111111111111111111",
		SafeAddress:   "0x2222222222222222222222222222222222222222",
		KeeperID:      "keeper-primary", Reason: "Initial binding",
	}
	inputHash, err := putHash(84532, "org_a", "agent_a", "owner_a", request)
	if err != nil {
		t.Fatal(err)
	}
	changeID := "0x" + strings.Repeat("a", 64)
	mock.ExpectQuery("SELECT change_id,agent_id,actor_id,input_hash,resulting_version").
		WithArgs("org_a", "binding_v1").
		WillReturnRows(sqlmock.NewRows([]string{"change_id", "agent_id", "actor_id", "input_hash", "resulting_version"}).
			AddRow(changeID, "agent_a", "owner_a", inputHash, 1))
	mock.ExpectQuery("SELECT organization_id,agent_id,version,chain_id,signer_key_id,key_epoch,module_address,safe_address,keeper_id,changed_by,created_at").
		WithArgs("org_a", "agent_a", uint64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"organization_id", "agent_id", "version", "chain_id", "signer_key_id", "key_epoch", "module_address", "safe_address", "keeper_id", "changed_by", "created_at"}).
			AddRow("org_a", "agent_a", 1, 84532, request.SignerKeyID, request.KeyEpoch, request.ModuleAddress, request.SafeAddress, request.KeeperID, "owner_a", now))
	result, err := store.Put(context.Background(), "org_a", "agent_a", "owner_a", "binding_v1", request)
	if err != nil || !result.Replayed || result.ChangeID != changeID || result.Binding.Version != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestStoreConfigurationFailsClosed(t *testing.T) {
	if _, err := NewStore(nil, 84532); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("nil database error=%v", err)
	}
	if _, err := NewStore(new(sql.DB), 0); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("zero chain error=%v", err)
	}
}
