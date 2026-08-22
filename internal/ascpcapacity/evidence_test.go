package ascpcapacity

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestAnalyzeAcceptsBoundedSustainedRestartRun(t *testing.T) {
	profile, evidence := passingRun(t)
	report := Analyze(profile, evidence)
	if !report.Passed || len(report.Issues) != 0 {
		t.Fatalf("report=%+v", report)
	}
	if report.Metrics.AchievedRPS != 1 || report.Metrics.SuccessPPM != 1_000_000 {
		t.Fatalf("metrics=%+v", report.Metrics)
	}
}

func TestAnalyzeRejectsAC34FailureClasses(t *testing.T) {
	tests := []struct {
		name string
		want string
		edit func(*Profile, *Evidence)
	}{
		{name: "profile substitution", want: "profile binding", edit: func(_ *Profile, evidence *Evidence) { evidence.ProfileDigest = testHash(9999) }},
		{name: "duplicate economic effect", want: "duplicate economic effects", edit: func(_ *Profile, evidence *Evidence) {
			evidence.Operations[0].EconomicEffectIDs = []string{"lock-1", "lock-2"}
		}},
		{name: "reservation leak", want: "leaked reservation", edit: func(_ *Profile, evidence *Evidence) { evidence.Operations[0].ReservationState = "RESERVED" }},
		{name: "queue overflow", want: "queue exceeded", edit: func(_ *Profile, evidence *Evidence) { evidence.Queues[0].Depth = 11 }},
		{name: "queue not drained", want: "did not drain", edit: func(_ *Profile, evidence *Evidence) { evidence.Queues[len(evidence.Queues)-1].Depth = 1 }},
		{name: "restart missing", want: "required restart", edit: func(_ *Profile, evidence *Evidence) { evidence.Restarts = nil }},
		{name: "decision slo", want: "decision p95", edit: func(_ *Profile, evidence *Evidence) {
			for index := range evidence.Samples {
				if evidence.Samples[index].Stage == StageDecision {
					evidence.Samples[index].CompletedAt = evidence.Samples[index].StartedAt.Add(301 * time.Millisecond)
				}
			}
		}},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			profile, evidence := passingRun(t)
			item.edit(&profile, &evidence)
			report := Analyze(profile, evidence)
			if report.Passed || !containsIssue(report.Issues, item.want) {
				t.Fatalf("issues=%v want substring %q", report.Issues, item.want)
			}
		})
	}
}

func passingRun(t *testing.T) (Profile, Evidence) {
	t.Helper()
	start := time.Unix(1800000000, 0).UTC()
	profile := Profile{
		Version: EvidenceVersion, Name: "operator-peak-v1", TargetRPS: 1,
		MinimumDuration: 10 * time.Minute, MinimumRetryPPM: 1000,
		RequiredRestarts: map[string]int{"control-plane": 1},
		MaxQueueDepth:    map[string]int{"execution": 10},
	}
	digest, err := ProfileDigest(profile)
	if err != nil {
		t.Fatal(err)
	}
	evidence := Evidence{
		Version: EvidenceVersion, RunID: "capacity-run-1", ProfileDigest: digest,
		StartedAt: start, CompletedAt: start.Add(10 * time.Minute),
		Restarts: []Restart{{Process: "control-plane", BeforeInstance: "instance-1", AfterInstance: "instance-2", RequestedAt: start.Add(2 * time.Minute), ReadyAt: start.Add(2*time.Minute + time.Second)}},
		Queues:   []QueuePoint{{Queue: "execution", At: start, Depth: 10}, {Queue: "execution", At: start.Add(10 * time.Minute), Depth: 0}},
	}
	for index := 0; index < 600; index++ {
		operationID := testHash(index + 1)
		at := start.Add(time.Duration(index) * time.Second)
		evidence.Samples = append(evidence.Samples, Sample{OperationID: operationID, Attempt: 1, Stage: StageDecision, StartedAt: at, CompletedAt: at.Add(10 * time.Millisecond), Success: true, EconomicEffectID: "effect-" + operationID})
		evidence.Operations = append(evidence.Operations, OperationFinal{OperationID: operationID, Accepted: true, AuthorizationState: "VALIDATED_AND_RESERVED", ReservationState: "CONSUMED_ON_RELEASE", EconomicEffectIDs: []string{"effect-" + operationID}})
	}
	operationID := evidence.Operations[0].OperationID
	for index, stage := range []Stage{StageSigner, StageBroadcastMined, StageClaimExpired} {
		at := start.Add(time.Duration(index+1) * time.Second)
		duration := []time.Duration{time.Second, 30 * time.Second, 5 * time.Minute}[index]
		evidence.Samples = append(evidence.Samples, Sample{OperationID: operationID, Attempt: 1, Stage: stage, StartedAt: at, CompletedAt: at.Add(duration), Success: true, EconomicEffectID: "effect-" + operationID})
	}
	evidence.Samples = append(evidence.Samples, Sample{OperationID: operationID, Attempt: 2, Stage: StageDecision, StartedAt: start.Add(9 * time.Minute), CompletedAt: start.Add(9*time.Minute + 10*time.Millisecond), Success: true, Replay: true, EconomicEffectID: "effect-" + operationID})
	return profile, evidence
}

func containsIssue(issues []string, want string) bool {
	for _, issue := range issues {
		if strings.Contains(issue, want) {
			return true
		}
	}
	return false
}

func testHash(value int) string { return fmt.Sprintf("0x%064x", value) }
