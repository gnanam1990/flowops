package ascpcapacity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	EvidenceVersion        = 1
	minimumSuccessPPM      = 999000
	decisionP95Limit       = 300 * time.Millisecond
	signerP95Limit         = 2 * time.Second
	broadcastMinedP95Limit = 60 * time.Second
	claimExpiredP99Limit   = 10 * time.Minute
)

type Stage string

const (
	StageDecision       Stage = "DECISION"
	StageSigner         Stage = "SIGNER"
	StageBroadcastMined Stage = "BROADCAST_TO_MINED"
	StageClaimExpired   Stage = "CLAIM_EXPIRED_BROADCAST"
)

type Profile struct {
	Version          int            `json:"version"`
	Name             string         `json:"name"`
	TargetRPS        int            `json:"targetRps"`
	MinimumDuration  time.Duration  `json:"minimumDuration"`
	MinimumRetryPPM  int            `json:"minimumRetryPpm"`
	RequiredRestarts map[string]int `json:"requiredRestarts"`
	MaxQueueDepth    map[string]int `json:"maxQueueDepth"`
}

type Sample struct {
	OperationID      string    `json:"operationId"`
	Attempt          int       `json:"attempt"`
	Stage            Stage     `json:"stage"`
	StartedAt        time.Time `json:"startedAt"`
	CompletedAt      time.Time `json:"completedAt"`
	Success          bool      `json:"success"`
	Replay           bool      `json:"replay"`
	EconomicEffectID string    `json:"economicEffectId,omitempty"`
}

type Restart struct {
	Process        string    `json:"process"`
	BeforeInstance string    `json:"beforeInstance"`
	AfterInstance  string    `json:"afterInstance"`
	RequestedAt    time.Time `json:"requestedAt"`
	ReadyAt        time.Time `json:"readyAt"`
}

type QueuePoint struct {
	Queue string    `json:"queue"`
	At    time.Time `json:"at"`
	Depth int       `json:"depth"`
}

type OperationFinal struct {
	OperationID        string   `json:"operationId"`
	Accepted           bool     `json:"accepted"`
	AuthorizationState string   `json:"authorizationState,omitempty"`
	ReservationState   string   `json:"reservationState,omitempty"`
	BearerOutcome      string   `json:"bearerOutcome,omitempty"`
	ActionablePending  bool     `json:"actionablePending"`
	EconomicEffectIDs  []string `json:"economicEffectIds,omitempty"`
}

type Evidence struct {
	Version       int              `json:"version"`
	RunID         string           `json:"runId"`
	ProfileDigest string           `json:"profileDigest"`
	StartedAt     time.Time        `json:"startedAt"`
	CompletedAt   time.Time        `json:"completedAt"`
	Samples       []Sample         `json:"samples"`
	Restarts      []Restart        `json:"restarts"`
	Queues        []QueuePoint     `json:"queues"`
	Operations    []OperationFinal `json:"operations"`
}

type Metrics struct {
	AchievedRPS       float64       `json:"achievedRps"`
	SuccessPPM        int           `json:"successPpm"`
	RetryPPM          int           `json:"retryPpm"`
	DecisionP95       time.Duration `json:"decisionP95"`
	SignerP95         time.Duration `json:"signerP95"`
	BroadcastMinedP95 time.Duration `json:"broadcastMinedP95"`
	ClaimExpiredP99   time.Duration `json:"claimExpiredP99"`
}

type Report struct {
	Version        int      `json:"version"`
	RunID          string   `json:"runId"`
	ProfileDigest  string   `json:"profileDigest"`
	EvidenceDigest string   `json:"evidenceDigest"`
	Passed         bool     `json:"passed"`
	Metrics        Metrics  `json:"metrics"`
	Issues         []string `json:"issues,omitempty"`
}

func ProfileDigest(profile Profile) (string, error) {
	if issues := validateProfile(profile); len(issues) > 0 {
		return "", fmt.Errorf("invalid capacity profile: %s", strings.Join(issues, "; "))
	}
	encoded, err := json.Marshal(profile)
	if err != nil {
		return "", err
	}
	return digest("ASCP_CAPACITY_PROFILE_V1", encoded), nil
}

func Analyze(profile Profile, evidence Evidence) Report {
	profileDigest, profileErr := ProfileDigest(profile)
	report := Report{Version: EvidenceVersion, RunID: evidence.RunID, ProfileDigest: profileDigest}
	encoded, _ := json.Marshal(evidence)
	report.EvidenceDigest = digest("ASCP_CAPACITY_EVIDENCE_V1", encoded)
	if profileErr != nil {
		report.Issues = append(report.Issues, profileErr.Error())
		return report
	}
	if evidence.Version != EvidenceVersion || evidence.ProfileDigest != profileDigest || !validID(evidence.RunID) ||
		evidence.StartedAt.IsZero() || !evidence.CompletedAt.After(evidence.StartedAt) {
		report.Issues = append(report.Issues, "evidence envelope or profile binding is invalid")
		return report
	}
	duration := evidence.CompletedAt.Sub(evidence.StartedAt)
	if duration < profile.MinimumDuration {
		report.Issues = append(report.Issues, "sustained duration is below the declared peak profile")
	}
	primaryDecisions, successfulDecisions, retries := 0, 0, 0
	seenPrimary := make(map[string]struct{})
	replayedOperations := make(map[string]struct{})
	stageDurations := make(map[Stage][]time.Duration)
	firstEffect := make(map[string]string)
	for _, sample := range evidence.Samples {
		if !validHash(sample.OperationID) || sample.Attempt < 1 || sample.StartedAt.Before(evidence.StartedAt) ||
			sample.CompletedAt.After(evidence.CompletedAt) || !sample.CompletedAt.After(sample.StartedAt) {
			report.Issues = append(report.Issues, "sample binding or timing is invalid")
			continue
		}
		if sample.Stage != StageDecision && sample.Stage != StageSigner && sample.Stage != StageBroadcastMined && sample.Stage != StageClaimExpired {
			report.Issues = append(report.Issues, "sample stage is unsupported")
			continue
		}
		stageDurations[sample.Stage] = append(stageDurations[sample.Stage], sample.CompletedAt.Sub(sample.StartedAt))
		if sample.Stage == StageDecision {
			if sample.Replay {
				retries++
				replayedOperations[sample.OperationID] = struct{}{}
				if sample.Attempt < 2 {
					report.Issues = append(report.Issues, "retry sample has an invalid attempt number")
				}
			} else if _, duplicate := seenPrimary[sample.OperationID]; duplicate {
				report.Issues = append(report.Issues, "operation has multiple primary decision samples")
			} else {
				if sample.Attempt != 1 {
					report.Issues = append(report.Issues, "primary decision sample has an invalid attempt number")
				}
				seenPrimary[sample.OperationID] = struct{}{}
				primaryDecisions++
				if sample.Success {
					successfulDecisions++
				}
			}
		}
		if sample.EconomicEffectID != "" {
			if prior := firstEffect[sample.OperationID]; prior != "" && prior != sample.EconomicEffectID {
				report.Issues = append(report.Issues, "retry changed the economic effect identity")
			} else {
				firstEffect[sample.OperationID] = sample.EconomicEffectID
			}
		}
	}
	for operationID := range replayedOperations {
		if _, exists := seenPrimary[operationID]; !exists {
			report.Issues = append(report.Issues, "retry sample has no primary decision")
		}
	}
	if duration > 0 {
		report.Metrics.AchievedRPS = float64(primaryDecisions) / duration.Seconds()
	}
	if primaryDecisions > 0 {
		report.Metrics.SuccessPPM = successfulDecisions * 1_000_000 / primaryDecisions
		report.Metrics.RetryPPM = retries * 1_000_000 / (primaryDecisions + retries)
	}
	if report.Metrics.AchievedRPS < float64(profile.TargetRPS)*0.99 {
		report.Issues = append(report.Issues, "achieved throughput is below 99% of the declared peak")
	}
	if report.Metrics.SuccessPPM < minimumSuccessPPM {
		report.Issues = append(report.Issues, "accepted-intent success rate is below 99.9%")
	}
	if report.Metrics.RetryPPM < profile.MinimumRetryPPM {
		report.Issues = append(report.Issues, "retry injection rate is below profile")
	}
	report.Metrics.DecisionP95 = percentile(stageDurations[StageDecision], 95)
	report.Metrics.SignerP95 = percentile(stageDurations[StageSigner], 95)
	report.Metrics.BroadcastMinedP95 = percentile(stageDurations[StageBroadcastMined], 95)
	report.Metrics.ClaimExpiredP99 = percentile(stageDurations[StageClaimExpired], 99)
	for _, slo := range []struct {
		name  string
		value time.Duration
		limit time.Duration
		count int
	}{
		{"decision p95", report.Metrics.DecisionP95, decisionP95Limit, len(stageDurations[StageDecision])},
		{"signer p95", report.Metrics.SignerP95, signerP95Limit, len(stageDurations[StageSigner])},
		{"broadcast-to-mined p95", report.Metrics.BroadcastMinedP95, broadcastMinedP95Limit, len(stageDurations[StageBroadcastMined])},
		{"claimExpired p99", report.Metrics.ClaimExpiredP99, claimExpiredP99Limit, len(stageDurations[StageClaimExpired])},
	} {
		if slo.count == 0 || slo.value > slo.limit {
			report.Issues = append(report.Issues, slo.name+" is missing or exceeds the PRD limit")
		}
	}
	validateRestarts(profile, evidence, &report)
	validateQueues(profile, evidence, &report)
	validateOperations(evidence, firstEffect, &report)
	report.Passed = len(report.Issues) == 0
	return report
}

func validateProfile(profile Profile) []string {
	var issues []string
	if profile.Version != EvidenceVersion || !validID(profile.Name) || profile.TargetRPS < 1 || profile.TargetRPS > 100000 ||
		profile.MinimumDuration < 10*time.Minute || profile.MinimumDuration > 24*time.Hour ||
		profile.MinimumRetryPPM < 1 || profile.MinimumRetryPPM > 500000 || len(profile.RequiredRestarts) == 0 || len(profile.MaxQueueDepth) == 0 {
		issues = append(issues, "profile fields are outside safe bounds")
	}
	for process, count := range profile.RequiredRestarts {
		if !validID(process) || count < 1 || count > 100 {
			issues = append(issues, "restart requirement is invalid")
		}
	}
	for queue, depth := range profile.MaxQueueDepth {
		if !validID(queue) || depth < 1 || depth > 1_000_000 {
			issues = append(issues, "queue bound is invalid")
		}
	}
	return issues
}

func validateRestarts(profile Profile, evidence Evidence, report *Report) {
	counts := make(map[string]int)
	for _, restart := range evidence.Restarts {
		if !validID(restart.Process) || !validID(restart.BeforeInstance) || !validID(restart.AfterInstance) ||
			restart.BeforeInstance == restart.AfterInstance || restart.RequestedAt.Before(evidence.StartedAt) ||
			restart.ReadyAt.After(evidence.CompletedAt) || !restart.ReadyAt.After(restart.RequestedAt) {
			report.Issues = append(report.Issues, "restart evidence is invalid")
			continue
		}
		counts[restart.Process]++
	}
	for process, required := range profile.RequiredRestarts {
		if counts[process] < required {
			report.Issues = append(report.Issues, "required restart evidence is missing for "+process)
		}
	}
}

func validateQueues(profile Profile, evidence Evidence, report *Report) {
	points := make(map[string][]QueuePoint)
	for _, point := range evidence.Queues {
		limit, configured := profile.MaxQueueDepth[point.Queue]
		if !configured || point.At.Before(evidence.StartedAt) || point.At.After(evidence.CompletedAt) || point.Depth < 0 {
			report.Issues = append(report.Issues, "queue evidence is invalid or unscoped")
			continue
		}
		if point.Depth > limit {
			report.Issues = append(report.Issues, "queue exceeded its declared hard bound: "+point.Queue)
		}
		points[point.Queue] = append(points[point.Queue], point)
	}
	for queue := range profile.MaxQueueDepth {
		values := points[queue]
		if len(values) == 0 {
			report.Issues = append(report.Issues, "queue evidence is missing for "+queue)
			continue
		}
		sort.Slice(values, func(i, j int) bool { return values[i].At.Before(values[j].At) })
		if !values[len(values)-1].At.Equal(evidence.CompletedAt) || values[len(values)-1].Depth != 0 {
			report.Issues = append(report.Issues, "queue did not drain after load: "+queue)
		}
	}
}

func validateOperations(evidence Evidence, sampledEffects map[string]string, report *Report) {
	seen := make(map[string]struct{})
	sampledOperations := mapKeysFromSamples(evidence.Samples)
	terminal := map[string]bool{"CONSUMED_ON_RELEASE": true, "RESTORED_ON_REFUND": true, "RELEASED": true, "RELEASED_AFTER_EXPIRY_PROOF": true}
	for _, operation := range evidence.Operations {
		if !validHash(operation.OperationID) {
			report.Issues = append(report.Issues, "operation final-state identity is invalid")
			continue
		}
		if _, duplicate := seen[operation.OperationID]; duplicate {
			report.Issues = append(report.Issues, "operation final state is duplicated")
		}
		seen[operation.OperationID] = struct{}{}
		effects := make(map[string]struct{})
		for _, effect := range operation.EconomicEffectIDs {
			if effect == "" {
				report.Issues = append(report.Issues, "economic effect identity is empty")
			}
			effects[effect] = struct{}{}
		}
		if len(effects) > 1 {
			report.Issues = append(report.Issues, "operation has duplicate economic effects")
		}
		if sampledEffect := sampledEffects[operation.OperationID]; sampledEffect != "" {
			if _, bound := effects[sampledEffect]; !bound {
				report.Issues = append(report.Issues, "operation final state changed the sampled economic effect identity")
			}
		}
		if operation.Accepted {
			if operation.AuthorizationState != "VALIDATED_AND_RESERVED" {
				report.Issues = append(report.Issues, "accepted operation lacks authoritative authorization")
			}
			if !terminal[operation.ReservationState] && !(operation.ActionablePending && operation.BearerOutcome == "LIVE") {
				report.Issues = append(report.Issues, "accepted operation has a leaked reservation or authorization")
			}
		} else if operation.AuthorizationState != "" || operation.ReservationState != "" || operation.BearerOutcome == "LIVE" {
			report.Issues = append(report.Issues, "rejected operation retained economic state")
		}
	}
	for operationID := range sampledOperations {
		if _, exists := seen[operationID]; !exists {
			report.Issues = append(report.Issues, "operation final-state coverage is incomplete")
		}
	}
	for operationID := range seen {
		if _, exists := sampledOperations[operationID]; !exists {
			report.Issues = append(report.Issues, "operation final state has no run sample")
		}
	}
}

func mapKeysFromSamples(samples []Sample) map[string]struct{} {
	result := make(map[string]struct{})
	for _, sample := range samples {
		result[sample.OperationID] = struct{}{}
	}
	return result
}

func percentile(values []time.Duration, percentage int) time.Duration {
	if len(values) == 0 {
		return 0
	}
	copyValues := append([]time.Duration(nil), values...)
	sort.Slice(copyValues, func(i, j int) bool { return copyValues[i] < copyValues[j] })
	index := (percentage*len(copyValues)+99)/100 - 1
	if index < 0 {
		index = 0
	}
	return copyValues[index]
}

func digest(domain string, encoded []byte) string {
	sum := sha256.Sum256(append(append([]byte(domain), '\n'), encoded...))
	return "0x" + hex.EncodeToString(sum[:])
}

func validID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for index, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || index > 0 && strings.ContainsRune("._:-", character)) {
			return false
		}
	}
	return true
}

func validHash(value string) bool {
	if len(value) != 66 || !strings.HasPrefix(value, "0x") || value != strings.ToLower(value) || value == "0x"+strings.Repeat("0", 64) {
		return false
	}
	_, err := hex.DecodeString(value[2:])
	return err == nil
}
