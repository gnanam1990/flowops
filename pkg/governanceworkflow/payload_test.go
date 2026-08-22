package governanceworkflow

import (
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

const vectorContract = "0x1111111111111111111111111111111111111111"

func TestGovernancePayloadGoldenVectors(t *testing.T) {
	values := map[string]string{
		"add verifier": mustHash(t, func() (common.Hash, error) {
			return CallEscrowAddVerifier(8453, vectorContract, "0x2222222222222222222222222222222222222222", 0, 0, 0, 7)
		}),
		"revoke verifier": mustHash(t, func() (common.Hash, error) {
			return CallEscrowRevokeVerifier(8453, vectorContract, "0x2222222222222222222222222222222222222222", 7)
		}),
		"escrow pause": mustHash(t, func() (common.Hash, error) {
			return CallEscrowPause(8453, vectorContract)
		}),
		"authorizer": mustHash(t, func() (common.Hash, error) {
			return SpendAuthorizer(8453, vectorContract,
				"0x2222222222222222222222222222222222222222", 9, "0x3333333333333333333333333333333333333333")
		}),
		"allowlist": mustHash(t, func() (common.Hash, error) {
			return SpendAllowlist(8453, vectorContract,
				"0x3333333333333333333333333333333333333333", hashValue(1), hashValue(2))
		}),
		"caps": mustHash(t, func() (common.Hash, error) {
			return SpendCaps(8453, vectorContract, Caps{"400", "800", "1000"}, Caps{"500", "900", "1200"})
		}),
		"module pause": mustHash(t, func() (common.Hash, error) {
			return SpendPause(8453, vectorContract, false, true)
		}),
		"invalidate": mustHash(t, func() (common.Hash, error) {
			return SpendInvalidateNonces(8453, vectorContract, []string{hashValue(3), hashValue(4)})
		}),
		"directory publish": mustHash(t, func() (common.Hash, error) {
			return DirectoryPublish(8453, vectorContract, DirectoryProposal{
				VersionID: 2, PreviousVersion: 1, PreviousRoot: hashValue(5), NewRoot: hashValue(6),
				BlobContentHash: hashValue(7), LocationsHash: hashValue(8), ChangeClass: 2, RequestedActivatesAt: 1_800_000_000,
			})
		}),
		"directory cancel": mustHash(t, func() (common.Hash, error) {
			return DirectoryCancel(8453, vectorContract, 2, hashValue(9))
		}),
	}
	wants := map[string]string{
		"add verifier":      "0x0f19082ff9903ba033a08ccbb3b8528aecf73085f137c7525e75edfd3eb82ae5",
		"revoke verifier":   "0xc495ef0524c5118be2b3f5212e30d052b96b53e213fe6ad459612e0acff0837b",
		"escrow pause":      "0xd392524af9b81440a6267a4b8b84939e061dc4df4a9d1133258dc88dc857001a",
		"authorizer":        "0xcbfaa0eb15e9f0bbfd62fb7e9aec03630ed5341a82826c795ff379b37eb3c6e7",
		"allowlist":         "0x0b62fea6f1b5ce4ba038893f1648b2a094a09f40df7e55618961cc15df717973",
		"caps":              "0x31232f6aa50156b95bf23de30b201dae965c824d12d6ff0997dfddbd91ff5b6c",
		"module pause":      "0xaae2725657863db70a99d73ed88ced5a073de3ff26b20de56afa922417966969",
		"invalidate":        "0xbbd33343b45306654ce8246f2aa753f2d1ed7126a15acc99cf83c9837eef9937",
		"directory publish": "0x9860e21d489d18d298b85887a6c2156d36f4a4da8e9d0673d9945343e7774b98",
		"directory cancel":  "0x85edd7f9038e2ed38c3e577fbd5fa3c36dc48403b29ccabc0d6ea755aa5f0770",
	}
	for name, want := range wants {
		if values[name] != want {
			t.Errorf("%s=%s want %s", name, values[name], want)
		}
	}
}

func TestGovernancePayloadRejectsDomainAndValueSubstitution(t *testing.T) {
	base := mustHash(t, func() (common.Hash, error) {
		return SpendAuthorizer(8453, vectorContract,
			"0x2222222222222222222222222222222222222222", 9, "0x3333333333333333333333333333333333333333")
	})
	mutations := []func() (common.Hash, error){
		func() (common.Hash, error) {
			return SpendAuthorizer(84532, vectorContract, "0x2222222222222222222222222222222222222222", 9, "0x3333333333333333333333333333333333333333")
		},
		func() (common.Hash, error) {
			return SpendAuthorizer(8453, "0x4444444444444444444444444444444444444444", "0x2222222222222222222222222222222222222222", 9, "0x3333333333333333333333333333333333333333")
		},
		func() (common.Hash, error) {
			return SpendAuthorizer(8453, vectorContract, "0x2222222222222222222222222222222222222222", 10, "0x3333333333333333333333333333333333333333")
		},
		func() (common.Hash, error) {
			return SpendAuthorizer(8453, vectorContract, "0x2222222222222222222222222222222222222222", 9, "0x5555555555555555555555555555555555555555")
		},
	}
	for index, mutation := range mutations {
		value, err := mutation()
		if err != nil || value.Hex() == base {
			t.Fatalf("mutation %d value=%s err=%v", index, value.Hex(), err)
		}
	}
	if _, err := SpendInvalidateNonces(8453, vectorContract, []string{hashValue(1), hashValue(1)}); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("duplicate nonce error=%v", err)
	}
	if _, err := SpendCaps(8453, vectorContract, Caps{"01", "2", "3"}, Caps{"1", "2", "3"}); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("ambiguous decimal error=%v", err)
	}
	for name, build := range map[string]func() (common.Hash, error){
		"unchanged allowlist": func() (common.Hash, error) {
			return SpendAllowlist(8453, vectorContract, "0x3333333333333333333333333333333333333333", hashValue(1), hashValue(1))
		},
		"invalid active caps": func() (common.Hash, error) {
			return SpendCaps(8453, vectorContract, Caps{"3", "2", "4"}, Caps{"1", "2", "3"})
		},
		"invalid proposed caps": func() (common.Hash, error) {
			return SpendCaps(8453, vectorContract, Caps{"1", "2", "3"}, Caps{"0", "2", "3"})
		},
		"unchanged pause": func() (common.Hash, error) {
			return SpendPause(8453, vectorContract, true, true)
		},
	} {
		if _, err := build(); !errors.Is(err, ErrInvalidPayload) {
			t.Errorf("%s error=%v", name, err)
		}
	}
}

func mustHash(t *testing.T, source func() (common.Hash, error)) string {
	t.Helper()
	value, err := source()
	if err != nil {
		t.Fatal(err)
	}
	return value.Hex()
}

func hashValue(value byte) string {
	const digits = "0123456789abcdef"
	result := make([]byte, 66)
	result[0], result[1] = '0', 'x'
	for index := 2; index < 66; index++ {
		result[index] = '0'
	}
	result[64], result[65] = digits[value>>4], digits[value&15]
	return string(result)
}
