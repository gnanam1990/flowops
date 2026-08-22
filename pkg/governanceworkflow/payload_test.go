package governanceworkflow

import (
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

const vectorContract = "0x1111111111111111111111111111111111111111"
const vectorWorkflow = "0x000000000000000000000000000000000000000000000000000000000000000a"

func TestGovernancePayloadGoldenVectors(t *testing.T) {
	values := map[string]string{
		"add verifier": mustHash(t, func() (common.Hash, error) {
			return CallEscrowAddVerifier(8453, vectorContract, vectorWorkflow, "0x2222222222222222222222222222222222222222", 0, 0, 0, false, 7)
		}),
		"revoke verifier": mustHash(t, func() (common.Hash, error) {
			return CallEscrowRevokeVerifier(8453, vectorContract, vectorWorkflow, "0x2222222222222222222222222222222222222222", 7, 0, 0, false)
		}),
		"escrow pause": mustHash(t, func() (common.Hash, error) {
			return CallEscrowPause(8453, vectorContract, vectorWorkflow)
		}),
		"authorizer": mustHash(t, func() (common.Hash, error) {
			return SpendAuthorizer(8453, vectorContract, vectorWorkflow,
				"0x2222222222222222222222222222222222222222", 9, "0x3333333333333333333333333333333333333333")
		}),
		"allowlist": mustHash(t, func() (common.Hash, error) {
			return SpendAllowlist(8453, vectorContract, vectorWorkflow,
				"0x3333333333333333333333333333333333333333", hashValue(1), hashValue(2))
		}),
		"caps": mustHash(t, func() (common.Hash, error) {
			return SpendCaps(8453, vectorContract, vectorWorkflow, Caps{"400", "800", "1000"}, Caps{"500", "900", "1200"})
		}),
		"module pause": mustHash(t, func() (common.Hash, error) {
			return SpendPause(8453, vectorContract, vectorWorkflow, false, true)
		}),
		"invalidate": mustHash(t, func() (common.Hash, error) {
			return SpendInvalidateNonces(8453, vectorContract, vectorWorkflow, []string{hashValue(3), hashValue(4)})
		}),
		"directory publish": mustHash(t, func() (common.Hash, error) {
			return DirectoryPublish(8453, vectorContract, vectorWorkflow, DirectoryProposal{
				VersionID: 2, PreviousVersion: 1, PreviousRoot: hashValue(5), NewRoot: hashValue(6),
				BlobContentHash: hashValue(7), LocationsHash: hashValue(8), ChangeClass: 2, RequestedActivatesAt: 1_800_000_000,
			})
		}),
		"directory cancel": mustHash(t, func() (common.Hash, error) {
			return DirectoryCancel(8453, vectorContract, vectorWorkflow, 2, hashValue(9))
		}),
	}
	wants := map[string]string{
		"add verifier":      "0x9b0c7f4e567efee6a00679ec82a130cd533033556778b4714954ae33c5e21a9a",
		"revoke verifier":   "0x01309d6736691a730ed69edebe6de0947eb2e3cca8644bfc0f7d95c73f4cf9aa",
		"escrow pause":      "0x9ac44345065eface2da788044bb90eeba3677169074860e43fb034a8cb8d1daf",
		"authorizer":        "0x3c4e6de0140852b75b2db79942976748a0d8b0cee3249a5c39044a9675ab720c",
		"allowlist":         "0x584236800edc934197628ebfb3f2148ea00694291c6a6e74565208bbd3533544",
		"caps":              "0x0064db44b287bf57c4b40240ea6588aa2bd230d6349aae90da6f03f0c59833d4",
		"module pause":      "0x92c84c2ef61ba58353d2bbf13cec02f789e014c8a076923dc2b22011c106e890",
		"invalidate":        "0xc3b8f091400426a9302ded1fb80763d7bffe24744781106fa9a70af9487be863",
		"directory publish": "0xf577289b92b129c625813d0725e72da6c048a94651ad07508083ecc3a01f24b9",
		"directory cancel":  "0xdffa3dea6724afbc06b8e60d4306cd37fd64c84cf20c506aa29f188791eb2b08",
	}
	for name, value := range values {
		want, ok := wants[name]
		if !ok || value != want {
			t.Errorf("%s=%s want %q", name, value, want)
		}
	}
	if len(values) != len(wants) {
		t.Errorf("generated %d vectors, have %d expectations", len(values), len(wants))
	}
}

func TestGovernancePayloadRejectsDomainAndValueSubstitution(t *testing.T) {
	base := mustHash(t, func() (common.Hash, error) {
		return SpendAuthorizer(8453, vectorContract, vectorWorkflow,
			"0x2222222222222222222222222222222222222222", 9, "0x3333333333333333333333333333333333333333")
	})
	mutations := []func() (common.Hash, error){
		func() (common.Hash, error) {
			return SpendAuthorizer(8453, vectorContract, hashValue(11),
				"0x2222222222222222222222222222222222222222", 9, "0x3333333333333333333333333333333333333333")
		},
		func() (common.Hash, error) {
			return SpendAuthorizer(84532, vectorContract, vectorWorkflow, "0x2222222222222222222222222222222222222222", 9, "0x3333333333333333333333333333333333333333")
		},
		func() (common.Hash, error) {
			return SpendAuthorizer(8453, "0x4444444444444444444444444444444444444444", vectorWorkflow, "0x2222222222222222222222222222222222222222", 9, "0x3333333333333333333333333333333333333333")
		},
		func() (common.Hash, error) {
			return SpendAuthorizer(8453, vectorContract, vectorWorkflow, "0x2222222222222222222222222222222222222222", 10, "0x3333333333333333333333333333333333333333")
		},
		func() (common.Hash, error) {
			return SpendAuthorizer(8453, vectorContract, vectorWorkflow, "0x2222222222222222222222222222222222222222", 9, "0x5555555555555555555555555555555555555555")
		},
	}
	for index, mutation := range mutations {
		value, err := mutation()
		if err != nil || value.Hex() == base {
			t.Fatalf("mutation %d value=%s err=%v", index, value.Hex(), err)
		}
	}
	if _, err := SpendInvalidateNonces(8453, vectorContract, vectorWorkflow, []string{hashValue(1), hashValue(1)}); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("duplicate nonce error=%v", err)
	}
	if _, err := SpendCaps(8453, vectorContract, vectorWorkflow, Caps{"01", "2", "3"}, Caps{"1", "2", "3"}); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("ambiguous decimal error=%v", err)
	}
	if _, err := SpendAuthorizer(8453, vectorContract, hashValue(0),
		"0x2222222222222222222222222222222222222222", 9,
		"0x3333333333333333333333333333333333333333"); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("zero workflow error=%v", err)
	}
	for name, build := range map[string]func() (common.Hash, error){
		"unsupported chain": func() (common.Hash, error) {
			return CallEscrowPause(1, vectorContract, vectorWorkflow)
		},
		"mixed-case contract": func() (common.Hash, error) {
			return CallEscrowPause(8453, "0x11111111111111111111111111111111111111Aa", vectorWorkflow)
		},
		"zero contract": func() (common.Hash, error) {
			return CallEscrowPause(8453, "0x0000000000000000000000000000000000000000", vectorWorkflow)
		},
		"unchanged allowlist": func() (common.Hash, error) {
			return SpendAllowlist(8453, vectorContract, vectorWorkflow, "0x3333333333333333333333333333333333333333", hashValue(1), hashValue(1))
		},
		"invalid active caps": func() (common.Hash, error) {
			return SpendCaps(8453, vectorContract, vectorWorkflow, Caps{"3", "2", "4"}, Caps{"1", "2", "3"})
		},
		"invalid proposed caps": func() (common.Hash, error) {
			return SpendCaps(8453, vectorContract, vectorWorkflow, Caps{"1", "2", "3"}, Caps{"0", "2", "3"})
		},
		"unchanged caps": func() (common.Hash, error) {
			return SpendCaps(8453, vectorContract, vectorWorkflow, Caps{"1", "2", "3"}, Caps{"1", "2", "3"})
		},
		"invalid pending verifier tuple": func() (common.Hash, error) {
			return CallEscrowRevokeVerifier(8453, vectorContract, vectorWorkflow,
				"0x2222222222222222222222222222222222222222", 0, 7, 0, false)
		},
		"stale pending add verifier": func() (common.Hash, error) {
			return CallEscrowAddVerifier(8453, vectorContract, vectorWorkflow,
				"0x2222222222222222222222222222222222222222", 9, 7, 1_800_000_000, false, 10)
		},
		"stale pending revoke verifier": func() (common.Hash, error) {
			return CallEscrowRevokeVerifier(8453, vectorContract, vectorWorkflow,
				"0x2222222222222222222222222222222222222222", 9, 7, 1_800_000_000, false)
		},
		"revoked add verifier": func() (common.Hash, error) {
			return CallEscrowAddVerifier(8453, vectorContract, vectorWorkflow,
				"0x2222222222222222222222222222222222222222", 9, 0, 0, true, 10)
		},
		"revoked verifier": func() (common.Hash, error) {
			return CallEscrowRevokeVerifier(8453, vectorContract, vectorWorkflow,
				"0x2222222222222222222222222222222222222222", 9, 0, 0, true)
		},
		"unchanged pause": func() (common.Hash, error) {
			return SpendPause(8453, vectorContract, vectorWorkflow, true, true)
		},
	} {
		if _, err := build(); !errors.Is(err, ErrInvalidPayload) {
			t.Errorf("%s error=%v", name, err)
		}
	}
}

func TestDirectoryAndRegistryAuthorityPayloadsBindDomainSelectorAndCurrentState(t *testing.T) {
	current := "0x2222222222222222222222222222222222222222"
	next := "0x3333333333333333333333333333333333333333"
	publisher := mustHash(t, func() (common.Hash, error) {
		return DirectorySetPublisher(8453, vectorContract, vectorWorkflow, current, 7, next)
	})
	pauser := mustHash(t, func() (common.Hash, error) {
		return DirectorySetPauser(8453, vectorContract, vectorWorkflow, current, 7, next)
	})
	registry := mustHash(t, func() (common.Hash, error) {
		return AgentSetRegistryAdmin(8453, vectorContract, vectorWorkflow, current, 7, next)
	})
	if publisher == pauser || publisher == registry || pauser == registry {
		t.Fatal("authority payload domains and selectors must not collide")
	}
	if _, err := DirectorySetPublisher(8453, vectorContract, vectorWorkflow, current, 7, current); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("unchanged authority error=%v", err)
	}
	if _, err := DirectoryPauseSeller(8453, vectorContract, vectorWorkflow, hashValue(12), true, true); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("unchanged seller overlay error=%v", err)
	}
	if _, err := DirectoryQuoteKeyRevocation(8453, vectorContract, vectorWorkflow, current, false, false); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("unchanged quote-key overlay error=%v", err)
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
