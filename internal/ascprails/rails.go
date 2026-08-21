// Package ascprails implements the durable buyer-side seller egress boundary
// for an already-authorized escrow-call operation. It does not authorize spend,
// sign, settle, release, or refund funds.
package ascprails

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gnanam1990/flowops/internal/ascpverifier"
	"github.com/gnanam1990/flowops/pkg/escrowcall"
	"github.com/gnanam1990/flowops/pkg/purchasespec"
)

const (
	MaxResponseBytes = 16 << 20
	maxAttempts      = 3
	leaseWriteMargin = 5 * time.Second
)

type State string

const (
	StateQueued         State = "QUEUED"
	StateSending        State = "SENDING"
	StateRetryWait      State = "RETRY_WAIT"
	StateResponseStored State = "RESPONSE_STORED"
	StateCaptured       State = "CAPTURED"
	StateMissing        State = "MISSING"
	StateDeadLetter     State = "DEAD_LETTER"
)

var (
	ErrInvalidConfig          = errors.New("invalid seller egress configuration")
	ErrInvalidJob             = errors.New("invalid seller egress job")
	ErrStateConflict          = errors.New("seller egress state conflict")
	ErrLeaseLost              = errors.New("seller egress lease lost")
	ErrNoWork                 = errors.New("no seller egress work available")
	ErrResponseTooBig         = errors.New("seller response exceeds capture limit")
	ErrUnsafeResponse         = errors.New("seller response is not bound to the operation")
	ErrOperationNotExecutable = errors.New("payment operation is not executable")
	ErrLeadershipChanged      = errors.New("seller egress leadership epoch changed")
	hashPattern               = regexp.MustCompile(`^0x[0-9a-f]{64}$`)
	addressPattern            = regexp.MustCompile(`^0x[0-9a-f]{40}$`)
	identifierPattern         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

// EnqueueInput contains only immutable data produced by earlier authorization,
// escrow-lock, quote-intake, and leadership-selection boundaries.
type EnqueueInput struct {
	JobID                 string
	OperationID           string
	OrganizationID        string
	ChainID               uint64
	LeadershipEpoch       uint64
	DeliverBy             uint64
	Method                string
	URL                   string
	Headers               http.Header
	Body                  []byte
	CanonicalSpecJSON     []byte
	Offer                 escrowcall.Offer
	Payment               escrowcall.Payment
	Binding               escrowcall.OperationBinding
	LockedTransactionHash string
	Payer                 string
	ValidatedChainTime    uint64
}

type Job struct {
	EnqueueInput
	State           State
	AttemptCount    int
	EligibleAfter   time.Time
	LeaseOwner      string
	LeaseToken      string
	LeaseExpiresAt  time.Time
	LastError       string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	ResponseStatus  int
	ResponseType    string
	ResponseDigest  string
	PaymentResponse string
	ResponseBody    []byte
	CapturedAt      uint64
	CaptureEvidence string
}

type Lease struct {
	Job   Job
	Token string
}

type ChainObservation struct {
	Timestamp      uint64
	EvidenceDigest string
	ObservedAt     time.Time
}

type StoredResponse struct {
	Attempt         int
	Status          int
	ContentType     string
	ContentEncoding string
	PaymentResponse string
	Body            []byte
	Digest          string
	ReceivedAt      time.Time
}

type Store interface {
	Enqueue(context.Context, EnqueueInput) (Job, bool, error)
	ClaimDispatch(context.Context, string, time.Duration) (Lease, error)
	MarkSending(context.Context, Lease, ChainObservation) (Job, error)
	RecordResponse(context.Context, Lease, StoredResponse, State, string, time.Time) (Job, error)
	RecordTransportFailure(context.Context, Lease, string, State, time.Time) (Job, error)
	ClaimFinalization(context.Context, string, time.Duration) (Lease, error)
	FinalizeCapture(context.Context, Lease, ChainObservation) (Job, error)
	MarkDeadlineMissing(context.Context, Lease, ChainObservation, string) (Job, error)
	ReleaseLease(context.Context, Lease) error
	Get(context.Context, string) (Job, error)
}

// LeadershipGate must read current, finalized leadership state. Fence invokes
// effect synchronously at most once, only while expected is current, and must
// prevent the current epoch from changing until effect returns. It rejects a
// stale expected epoch without invoking effect. A stale cached epoch is not
// sufficient to permit paid seller egress.
type LeadershipGate interface {
	Current(context.Context, string) (uint64, error)
	Fence(context.Context, string, uint64, func(context.Context) error) error
}

// ChainClock returns a corroborated chain timestamp and evidence digest. Local
// wall time is deliberately excluded from escrow deadline decisions.
type ChainClock interface {
	Confirmed(context.Context, uint64) (ChainObservation, error)
}

// OperationGate rechecks the independently reconciled payment operation
// immediately before seller egress. In particular, a reorged, terminal, or
// quarantined escrow lock cannot rely on enqueue-time state.
type OperationGate interface {
	Check(context.Context, Job) error
}

type IntegrityGate interface {
	Check(context.Context) error
}

type Event struct {
	JobID, OperationID, OrganizationID, Code string
	State                                    State
	Attempt                                  int
}

type Recorder interface{ Record(context.Context, Event) }

type Config struct {
	WorkerID          string
	LeaseDuration     time.Duration
	HTTPTimeout       time.Duration
	RetryDelay        time.Duration
	MaxAttempts       int
	MaxObservationAge time.Duration
	Clock             func() time.Time
	Recorder          Recorder
}

type Service struct {
	store      Store
	leadership LeadershipGate
	chain      ChainClock
	operations OperationGate
	integrity  IntegrityGate
	client     *http.Client
	config     Config
}

func NewService(store Store, leadership LeadershipGate, chain ChainClock, operations OperationGate, integrity IntegrityGate, transport http.RoundTripper, config Config) (*Service, error) {
	_, restricted := transport.(interface{ ascpRestrictedTransport() })
	if store == nil || leadership == nil || chain == nil || operations == nil || integrity == nil || transport == nil || !restricted || !identifierPattern.MatchString(config.WorkerID) ||
		config.LeaseDuration < time.Second || config.LeaseDuration > time.Minute || config.HTTPTimeout <= 0 || config.HTTPTimeout+leaseWriteMargin > config.LeaseDuration || config.RetryDelay < time.Second ||
		config.RetryDelay > time.Hour || config.MaxAttempts != maxAttempts || config.MaxObservationAge < time.Second || config.MaxObservationAge > time.Minute {
		return nil, ErrInvalidConfig
	}
	client, err := escrowcall.NewHTTPClient(transport, config.HTTPTimeout)
	if err != nil {
		return nil, fmt.Errorf("%w: http client: %v", ErrInvalidConfig, err)
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	return &Service{store: store, leadership: leadership, chain: chain, operations: operations, integrity: integrity, client: client, config: config}, nil
}

func (s *Service) Enqueue(ctx context.Context, input EnqueueInput) (Job, bool, error) {
	input = cloneInput(input)
	if err := validateInput(input); err != nil {
		return Job{}, false, err
	}
	request, err := newBoundRequest(ctx, input)
	if err != nil {
		return Job{}, false, err
	}
	if _, err := escrowcall.PrepareRequest(time.Unix(int64(input.ValidatedChainTime), 0).UTC(), request, input.Body, input.Payment, input.Offer, input.Binding, input.CanonicalSpecJSON); err != nil {
		return Job{}, false, fmt.Errorf("%w: pre-egress binding: %v", ErrInvalidJob, err)
	}
	return s.store.Enqueue(ctx, input)
}

// DispatchOne advances exactly one durable dispatch job. SENDING is persisted
// before network I/O; an expired lease may replay only the exact same call ID
// and payment proof, which the escrow-call seller contract must deduplicate.
func (s *Service) DispatchOne(ctx context.Context) (Job, error) {
	lease, err := s.store.ClaimDispatch(ctx, s.config.WorkerID, s.config.LeaseDuration)
	if err != nil {
		return Job{}, err
	}
	defer func() { _ = s.store.ReleaseLease(context.WithoutCancel(ctx), lease) }()
	job := lease.Job
	if job.AttemptCount >= s.config.MaxAttempts && (job.State == StateSending || job.State == StateRetryWait) {
		return s.failWithoutResponse(ctx, lease, "RESPONSE_UNKNOWN_RETRIES_EXHAUSTED", StateDeadLetter, time.Time{})
	}
	if err := s.integrity.Check(ctx); err != nil {
		return s.failWithoutResponse(ctx, lease, "EVENT_INTEGRITY_UNAVAILABLE", StateRetryWait, s.config.Clock().UTC().Add(s.config.RetryDelay))
	}
	currentEpoch, err := s.leadership.Current(ctx, job.OrganizationID)
	if err != nil {
		return s.failWithoutResponse(ctx, lease, "LEADERSHIP_UNAVAILABLE", StateRetryWait, s.config.Clock().UTC().Add(s.config.RetryDelay))
	}
	if currentEpoch != job.LeadershipEpoch {
		return s.failWithoutResponse(ctx, lease, "LEADERSHIP_EPOCH_CHANGED", StateDeadLetter, time.Time{})
	}
	if err := s.operations.Check(ctx, job); err != nil {
		if errors.Is(err, ErrOperationNotExecutable) {
			return s.failWithoutResponse(ctx, lease, "OPERATION_NOT_EXECUTABLE", StateDeadLetter, time.Time{})
		}
		return s.failWithoutResponse(ctx, lease, "OPERATION_STATE_UNAVAILABLE", StateRetryWait, s.config.Clock().UTC().Add(s.config.RetryDelay))
	}
	observation, err := s.chain.Confirmed(ctx, job.ChainID)
	if err != nil || s.validateFreshObservation(observation) != nil || observation.Timestamp < job.ValidatedChainTime {
		return s.failWithoutResponse(ctx, lease, "CHAIN_TIME_UNAVAILABLE", StateRetryWait, s.config.Clock().UTC().Add(s.config.RetryDelay))
	}
	if observation.Timestamp >= job.DeliverBy {
		updated, missingErr := s.store.MarkDeadlineMissing(ctx, lease, observation, "DELIVER_BY_REACHED_BEFORE_EGRESS")
		if missingErr == nil {
			s.record(ctx, updated, "DELIVER_BY_REACHED_BEFORE_EGRESS")
		}
		return updated, missingErr
	}
	request, err := newBoundRequest(ctx, job.EnqueueInput)
	if err != nil {
		return s.failWithoutResponse(ctx, lease, "REQUEST_RECONSTRUCTION_FAILED", StateDeadLetter, time.Time{})
	}
	prepared, err := escrowcall.PrepareRequest(time.Unix(int64(observation.Timestamp), 0).UTC(), request, job.Body, job.Payment, job.Offer, job.Binding, job.CanonicalSpecJSON)
	if err != nil {
		return s.failWithoutResponse(ctx, lease, "PRE_EGRESS_BINDING_FAILED", StateDeadLetter, time.Time{})
	}
	// Revalidate immediately before the durable SENDING fence. The PostgreSQL
	// store also locks and rechecks the payment operation while committing that
	// fence, so an operation-state update cannot interleave with it.
	if err := s.integrity.Check(ctx); err != nil {
		return s.failWithoutResponse(ctx, lease, "EVENT_INTEGRITY_UNAVAILABLE", StateRetryWait, s.config.Clock().UTC().Add(s.config.RetryDelay))
	}
	if err := s.operations.Check(ctx, job); err != nil {
		if errors.Is(err, ErrOperationNotExecutable) {
			return s.failWithoutResponse(ctx, lease, "OPERATION_NOT_EXECUTABLE", StateDeadLetter, time.Time{})
		}
		return s.failWithoutResponse(ctx, lease, "OPERATION_STATE_UNAVAILABLE", StateRetryWait, s.config.Clock().UTC().Add(s.config.RetryDelay))
	}
	var result Job
	effectCalled := false
	fenceErr := s.leadership.Fence(ctx, job.OrganizationID, job.LeadershipEpoch, func(fencedContext context.Context) error {
		effectCalled = true
		result, err = s.dispatchUnderLeadershipFence(fencedContext, lease, observation, prepared)
		return err
	})
	if effectCalled {
		return result, fenceErr
	}
	if errors.Is(fenceErr, ErrLeadershipChanged) {
		return s.failWithoutResponse(ctx, lease, "LEADERSHIP_EPOCH_CHANGED", StateDeadLetter, time.Time{})
	}
	return s.failWithoutResponse(ctx, lease, "LEADERSHIP_UNAVAILABLE", StateRetryWait, s.config.Clock().UTC().Add(s.config.RetryDelay))
}

func (s *Service) dispatchUnderLeadershipFence(ctx context.Context, lease Lease, observation ChainObservation, prepared *http.Request) (Job, error) {
	job, err := s.store.MarkSending(ctx, lease, observation)
	if err != nil {
		return Job{}, err
	}
	lease.Job = job
	s.record(ctx, job, "EGRESS_STARTED")
	response, err := s.client.Do(prepared.WithContext(ctx))
	if err != nil {
		state, next := s.retryState(job.AttemptCount)
		return s.failWithoutResponse(ctx, lease, "TRANSPORT_AMBIGUOUS", state, next)
	}
	defer func() { _ = response.Body.Close() }()
	stored, readErr := captureResponse(response, job.AttemptCount, s.config.Clock().UTC())
	if readErr != nil {
		state, next := StateDeadLetter, time.Time{}
		if !errors.Is(readErr, ErrResponseTooBig) && !errors.Is(readErr, ErrUnsafeResponse) {
			state, next = s.retryState(job.AttemptCount)
		}
		return s.failWithoutResponse(ctx, lease, errorCode(readErr), state, next)
	}
	state, code, next := s.classifyResponse(job, stored)
	updated, err := s.store.RecordResponse(ctx, lease, stored, state, code, next)
	if err == nil {
		s.record(ctx, updated, code)
	}
	return updated, err
}

// FinalizeOne attaches independently confirmed chain time to an already
// durable response. Recovery from RESPONSE_STORED never contacts the seller.
func (s *Service) FinalizeOne(ctx context.Context) (Job, error) {
	lease, err := s.store.ClaimFinalization(ctx, s.config.WorkerID, s.config.LeaseDuration)
	if err != nil {
		return Job{}, err
	}
	defer func() { _ = s.store.ReleaseLease(context.WithoutCancel(ctx), lease) }()
	observation, err := s.chain.Confirmed(ctx, lease.Job.ChainID)
	if err != nil {
		return Job{}, fmt.Errorf("confirmed chain time: %w", err)
	}
	if observationErr := s.validateFreshObservation(observation); observationErr != nil || observation.Timestamp < lease.Job.ValidatedChainTime {
		return Job{}, fmt.Errorf("confirmed chain time: %w", ErrInvalidJob)
	}
	job, err := s.store.FinalizeCapture(ctx, lease, observation)
	if err == nil {
		s.record(ctx, job, "DELIVERY_CAPTURED")
	}
	return job, err
}

func (s *Service) retryState(attempt int) (State, time.Time) {
	if attempt >= s.config.MaxAttempts {
		return StateDeadLetter, time.Time{}
	}
	return StateRetryWait, s.config.Clock().UTC().Add(s.config.RetryDelay)
}

func (s *Service) failWithoutResponse(ctx context.Context, lease Lease, code string, state State, eligible time.Time) (Job, error) {
	job, err := s.store.RecordTransportFailure(ctx, lease, code, state, eligible)
	if err == nil {
		s.record(ctx, job, code)
	}
	return job, err
}

func (s *Service) record(ctx context.Context, job Job, code string) {
	if s.config.Recorder != nil {
		s.config.Recorder.Record(ctx, Event{JobID: job.JobID, OperationID: job.OperationID, OrganizationID: job.OrganizationID, State: job.State, Attempt: job.AttemptCount, Code: code})
	}
}

func CapturedDelivery(job Job) (ascpverifier.Delivery, error) {
	if job.State != StateCaptured || job.CapturedAt == 0 || len(job.ResponseBody) == 0 || !hashPattern.MatchString(job.ResponseDigest) {
		return ascpverifier.Delivery{}, ErrStateConflict
	}
	return ascpverifier.Delivery{Reference: []byte("postgres:ascp_seller_responses/" + job.JobID), Content: append([]byte(nil), job.ResponseBody...), ContentDigest: job.ResponseDigest, HTTPStatus: uint16(job.ResponseStatus), ContentType: job.ResponseType, CapturedAt: job.CapturedAt}, nil
}

func validateInput(input EnqueueInput) error {
	if !nonZeroHash(input.JobID) || !nonZeroHash(input.OperationID) || input.OrganizationID == "" || len(input.OrganizationID) > 200 ||
		(input.ChainID != 8453 && input.ChainID != 84532) || input.LeadershipEpoch == 0 || input.DeliverBy == 0 ||
		input.ValidatedChainTime == 0 || input.ValidatedChainTime >= input.DeliverBy || input.ValidatedChainTime > uint64(^uint64(0)>>1) ||
		len(input.Body) > purchasespec.MaxRequestBodyBytes || len(input.CanonicalSpecJSON) == 0 || len(input.CanonicalSpecJSON) > 32<<20 ||
		input.Method == "" || len(input.Headers) > 256 {
		return ErrInvalidJob
	}
	if _, err := validateDestinationShape(input.URL); err != nil {
		return ErrInvalidJob
	}
	spec, err := purchasespec.ValidatePersisted(input.CanonicalSpecJSON, input.Body)
	if err != nil || spec.OrgID != input.OrganizationID || spec.Method != input.Method || spec.CanonicalURL != input.URL || input.Binding.CallID != input.JobID ||
		input.Offer.Accepted.Network != fmt.Sprintf("eip155:%d", input.ChainID) || !nonZeroHash(input.LockedTransactionHash) || !nonZeroAddress(input.Payer) {
		return ErrInvalidJob
	}
	return nil
}

func validateObservation(observation ChainObservation) error {
	if observation.Timestamp == 0 || observation.Timestamp > uint64(^uint64(0)>>1) || !nonZeroHash(observation.EvidenceDigest) || observation.ObservedAt.IsZero() {
		return ErrInvalidJob
	}
	return nil
}

func nonZeroHash(value string) bool {
	return hashPattern.MatchString(value) && value != "0x"+strings.Repeat("0", 64)
}
func nonZeroAddress(value string) bool {
	return addressPattern.MatchString(value) && value != "0x"+strings.Repeat("0", 40)
}

func (s *Service) validateFreshObservation(observation ChainObservation) error {
	if err := validateObservation(observation); err != nil {
		return ErrInvalidJob
	}
	now := s.config.Clock().UTC()
	observed := observation.ObservedAt.UTC()
	if observed.After(now.Add(5*time.Second)) || now.Sub(observed) > s.config.MaxObservationAge {
		return ErrInvalidJob
	}
	return nil
}

func newBoundRequest(ctx context.Context, input EnqueueInput) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, input.Method, input.URL, bytes.NewReader(input.Body))
	if err != nil {
		return nil, fmt.Errorf("%w: request: %v", ErrInvalidJob, err)
	}
	request.Header = input.Headers.Clone()
	request.Host = request.URL.Host
	return request, nil
}

func captureResponse(response *http.Response, attempt int, now time.Time) (StoredResponse, error) {
	if response == nil || response.Body == nil || response.StatusCode < 100 || response.StatusCode > 599 || attempt < 1 ||
		len(response.Header.Values("Payment-Response")) > 1 || len(response.Header.Values("Content-Type")) > 1 || len(response.Header.Values("Content-Encoding")) > 1 {
		return StoredResponse{}, ErrUnsafeResponse
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, MaxResponseBytes+1))
	if err != nil {
		return StoredResponse{}, fmt.Errorf("response read: %w", err)
	}
	if len(body) > MaxResponseBytes {
		return StoredResponse{}, ErrResponseTooBig
	}
	contentType := strings.TrimSpace(response.Header.Get("Content-Type"))
	if len(contentType) > 256 || !utf8.ValidString(contentType) {
		return StoredResponse{}, ErrUnsafeResponse
	}
	if contentType != "" {
		if _, _, err := mime.ParseMediaType(contentType); err != nil {
			return StoredResponse{}, ErrUnsafeResponse
		}
	}
	encoding := strings.ToLower(strings.TrimSpace(response.Header.Get("Content-Encoding")))
	if encoding != "" && encoding != "identity" {
		return StoredResponse{}, ErrUnsafeResponse
	}
	digestBytes := sha256.Sum256(body)
	return StoredResponse{Attempt: attempt, Status: response.StatusCode, ContentType: contentType, ContentEncoding: encoding,
		PaymentResponse: response.Header.Get("Payment-Response"), Body: body, Digest: "0x" + hex.EncodeToString(digestBytes[:]), ReceivedAt: now}, nil
}

func (s *Service) classifyResponse(job Job, response StoredResponse) (State, string, time.Time) {
	if response.Status == http.StatusTooManyRequests || response.Status >= 500 {
		if job.AttemptCount < s.config.MaxAttempts {
			return StateRetryWait, "SELLER_RETRYABLE_HTTP_" + strconv.Itoa(response.Status), s.config.Clock().UTC().Add(s.config.RetryDelay)
		}
		return StateDeadLetter, "SELLER_RETRIES_EXHAUSTED", time.Time{}
	}
	if response.Status < 200 || response.Status >= 300 {
		return StateMissing, "SELLER_TERMINAL_HTTP_" + strconv.Itoa(response.Status), time.Time{}
	}
	if response.ContentType == "" {
		return StateDeadLetter, "MISSING_CONTENT_TYPE", time.Time{}
	}
	if len(response.Body) == 0 {
		return StateDeadLetter, "EMPTY_RESPONSE", time.Time{}
	}
	decoded, err := escrowcall.DecodePaymentResponse(response.PaymentResponse, job.Offer, job.Binding.CallID)
	if err != nil || !decoded.Response.Success || decoded.Response.Transaction != job.LockedTransactionHash || decoded.Response.Payer != job.Payer {
		return StateDeadLetter, "INVALID_PAYMENT_RESPONSE", time.Time{}
	}
	rawExtension, err := json.Marshal(decoded.Response.Extensions["escrowCall"])
	var extension struct {
		ContentDigest string `json:"contentDigest"`
	}
	if err != nil || json.Unmarshal(rawExtension, &extension) != nil || extension.ContentDigest != response.Digest {
		return StateDeadLetter, "RESPONSE_DIGEST_MISMATCH", time.Time{}
	}
	return StateResponseStored, "RESPONSE_STORED", time.Time{}
}

func cloneInput(input EnqueueInput) EnqueueInput {
	input.Headers = input.Headers.Clone()
	if input.Headers == nil {
		input.Headers = make(http.Header)
	}
	input.Body = append([]byte(nil), input.Body...)
	input.CanonicalSpecJSON = append([]byte(nil), input.CanonicalSpecJSON...)
	return input
}

func errorCode(err error) string {
	if errors.Is(err, ErrResponseTooBig) {
		return "RESPONSE_TOO_LARGE"
	}
	if errors.Is(err, ErrUnsafeResponse) {
		return "UNSAFE_RESPONSE"
	}
	return "RESPONSE_READ_FAILED"
}
