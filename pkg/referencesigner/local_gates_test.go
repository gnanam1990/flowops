package referencesigner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/reconciliation"
	"github.com/gnanam1990/flowops/pkg/envelope"
)

func TestFileFreezeGateRereadsAndFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "freeze.json")
	writePrivateFile(t, path, `{"version":"flowops.freeze.v1","organizationFrozen":false,"frozenAgents":[],"frozenTasks":[]}`)
	gate, err := NewFileFreezeGate(path)
	if err != nil {
		t.Fatal(err)
	}
	authorization := envelope.Authorization{AgentID: "agent-1", TaskID: "task-1"}
	if err := gate.CheckFrozen(context.Background(), authorization); err != nil {
		t.Fatal(err)
	}
	writePrivateFile(t, path, `{"version":"flowops.freeze.v1","organizationFrozen":false,"frozenAgents":["agent-1"],"frozenTasks":[]}`)
	if err := gate.CheckFrozen(context.Background(), authorization); err == nil {
		t.Fatal("updated freeze was not enforced")
	}
	writePrivateFile(t, path, `{"version":"flowops.freeze.v1","organizationFrozen":false,"organizationFrozen":true,"frozenAgents":[],"frozenTasks":[]}`)
	if err := gate.CheckFrozen(context.Background(), authorization); err == nil {
		t.Fatal("duplicate security field was accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := gate.CheckFrozen(context.Background(), authorization); err == nil {
		t.Fatal("missing freeze file was accepted")
	}
	writePrivateFile(t, path, `{"version":"flowops.freeze.v1","organizationFrozen":false,"frozenAgents":[],"frozenTasks":[]}`)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := gate.CheckFrozen(context.Background(), authorization); err == nil {
		t.Fatal("unsafe freeze-file permissions were accepted")
	}
}

func TestFileFreezeGateRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	writePrivateFile(t, target, `{"version":"flowops.freeze.v1","organizationFrozen":false,"frozenAgents":[],"frozenTasks":[]}`)
	link := filepath.Join(dir, "freeze.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileFreezeGate(link); err == nil {
		t.Fatal("symlink freeze file was accepted")
	}
}

func TestQuorumChainGateRequiresFreshCanonicalAgreement(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	valid := []reconciliation.Observation{
		{Provider: "a", ChainID: 84532, HeadNumber: 101, HeadHash: hashOf("1"), HeadTime: now.Add(-time.Second), AnchorNumber: 100, AnchorHash: hashOf("a"), AnchorTime: now.Add(-2 * time.Second), ObservedAt: now},
		{Provider: "b", ChainID: 84532, HeadNumber: 100, HeadHash: hashOf("2"), HeadTime: now.Add(-2 * time.Second), AnchorNumber: 100, AnchorHash: hashOf("a"), AnchorTime: now.Add(-2 * time.Second), ObservedAt: now},
	}
	source := &staticSnapshot{result: reconciliation.SnapshotResult{Observations: valid}}
	gate, err := NewQuorumChainGate(QuorumChainGateConfig{ChainID: 84532, Source: source, Quorum: 2, MaxHeadSkew: 2, StallThreshold: time.Minute, MaxFutureSkew: 5 * time.Second, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.CheckChain(context.Background(), 84532); err != nil {
		t.Fatal(err)
	}
	tests := map[string]func([]reconciliation.Observation) []reconciliation.Observation{
		"no quorum": func(in []reconciliation.Observation) []reconciliation.Observation { return in[:1] },
		"anchor disagreement": func(in []reconciliation.Observation) []reconciliation.Observation {
			in[1].AnchorHash = hashOf("b")
			return in
		},
		"stale": func(in []reconciliation.Observation) []reconciliation.Observation {
			in[1].HeadTime = now.Add(-2 * time.Minute)
			return in
		},
		"skew": func(in []reconciliation.Observation) []reconciliation.Observation {
			in[1].HeadNumber = 90
			return in
		},
	}
	for name, mutation := range tests {
		t.Run(name, func(t *testing.T) {
			copyObservations := append([]reconciliation.Observation(nil), valid...)
			source.result = reconciliation.SnapshotResult{Observations: mutation(copyObservations)}
			if err := gate.CheckChain(context.Background(), 84532); err == nil {
				t.Fatal("unsafe observation set accepted")
			}
		})
	}
}

type staticSnapshot struct{ result reconciliation.SnapshotResult }

func (s *staticSnapshot) Snapshot(context.Context) reconciliation.SnapshotResult { return s.result }

func hashOf(character string) string { return "0x" + strings.Repeat(character, 64) }

func writePrivateFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}
