// Package ascpagent owns the agent-facing application boundary for durable
// ASCP intake. It derives every trusted identity and deployment term before
// delegating to the lower-level intake transaction.
package ascpagent

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gnanam1990/flowops/internal/ascpintake"
	"github.com/gnanam1990/flowops/pkg/purchasespec"
	"github.com/gnanam1990/flowops/pkg/sellerquote"
)

var (
	ErrInvalidIdentity  = errors.New("authenticated ASCP agent identity is invalid")
	ErrInvalidRequest   = errors.New("ASCP agent intake request is invalid")
	ErrUnsupportedTerms = errors.New("seller quote uses unsupported deployment terms")
)

type Identity struct {
	OrganizationID string
	AgentID        string
}

type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// CreateRequest contains only untrusted agent input. Organization, agent,
// directory contract, current directory evidence, chain, asset and scheme are
// deliberately absent or independently constrained by the service.
type CreateRequest struct {
	TaskID               string                        `json:"taskId"`
	Method               string                        `json:"method"`
	URL                  string                        `json:"url"`
	RequestBodyBase64    string                        `json:"requestBodyBase64,omitempty"`
	Headers              []Header                      `json:"headers,omitempty"`
	ResponseContract     purchasespec.ResponseContract `json:"responseContract"`
	Category             string                        `json:"category"`
	ReasonRef            *purchasespec.ReasonRef       `json:"reasonRef,omitempty"`
	SellerQuote          sellerquote.Quote             `json:"sellerQuote"`
	SellerQuoteSignature string                        `json:"sellerQuoteSignature"`
}

type DirectoryResolver interface {
	EvidenceForQuote(context.Context, sellerquote.Quote) (string, sellerquote.DirectoryEvidence, error)
}

type Config struct {
	Intake            *ascpintake.Service
	Reader            ascpintake.Reader
	Directory         DirectoryResolver
	DirectoryContract string
	ChainID           uint64
	Asset             string
	SchemeVersion     uint16
}

type Service struct {
	intake            *ascpintake.Service
	reader            ascpintake.Reader
	directory         DirectoryResolver
	directoryContract string
	chainID           string
	asset             string
	schemeVersion     uint16
}

func New(cfg Config) (*Service, error) {
	if cfg.Intake == nil || cfg.Reader == nil || cfg.Directory == nil ||
		(cfg.ChainID != 8453 && cfg.ChainID != 84532) || cfg.SchemeVersion == 0 ||
		len(cfg.DirectoryContract) != 42 || strings.ToLower(cfg.DirectoryContract) != cfg.DirectoryContract || !common.IsHexAddress(cfg.DirectoryContract) || common.HexToAddress(cfg.DirectoryContract) == (common.Address{}) ||
		len(cfg.Asset) != 42 || strings.ToLower(cfg.Asset) != cfg.Asset || !common.IsHexAddress(cfg.Asset) || common.HexToAddress(cfg.Asset) == (common.Address{}) {
		return nil, errors.New("valid durable intake, reader, directory resolver, chain, asset, and scheme are required")
	}
	return &Service{
		intake: cfg.Intake, reader: cfg.Reader, directory: cfg.Directory, directoryContract: cfg.DirectoryContract,
		chainID: fmt.Sprint(cfg.ChainID), asset: cfg.Asset, schemeVersion: cfg.SchemeVersion,
	}, nil
}

func (s *Service) Create(ctx context.Context, identity Identity, idempotencyKey string, request CreateRequest) (ascpintake.Operation, error) {
	if !validIdentity(identity) {
		return ascpintake.Operation{}, ErrInvalidIdentity
	}
	body, err := base64.StdEncoding.Strict().DecodeString(request.RequestBodyBase64)
	if err != nil {
		return ascpintake.Operation{}, fmt.Errorf("%w: requestBodyBase64", ErrInvalidRequest)
	}
	headers := make([]purchasespec.Header, len(request.Headers))
	for index, header := range request.Headers {
		headers[index] = purchasespec.Header{Name: header.Name, Value: header.Value}
	}
	spec, err := purchasespec.Build(purchasespec.Input{
		OrgID: identity.OrganizationID, AgentID: identity.AgentID, TaskID: request.TaskID,
		Method: request.Method, URL: request.URL, Body: body, Headers: headers,
		Response: request.ResponseContract, Category: request.Category, ReasonRef: request.ReasonRef,
	})
	if err != nil {
		return ascpintake.Operation{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	quote := request.SellerQuote
	if quote.PurchaseSpecHash != spec.PurchaseSpecHash || quote.ChainID != s.chainID || quote.Asset != s.asset || quote.SchemeVersion != s.schemeVersion {
		return ascpintake.Operation{}, ErrUnsupportedTerms
	}
	intakeRequest := ascpintake.Request{
		OrganizationID: identity.OrganizationID, ActorID: identity.AgentID, IdempotencyKey: idempotencyKey,
		DirectoryContract: s.directoryContract, Quote: quote, Signature: request.SellerQuoteSignature,
		Expected:              sellerquote.ExpectedTerms{PurchaseSpecHash: spec.PurchaseSpecHash, SchemeVersion: s.schemeVersion, ChainID: s.chainID, Asset: s.asset},
		CanonicalPurchaseSpec: spec.CanonicalJSON, RequestBody: body,
	}
	if replayed, found, err := s.intake.Replay(ctx, intakeRequest); err != nil || found {
		return replayed, err
	}
	directoryContract, evidence, err := s.directory.EvidenceForQuote(ctx, quote)
	if err != nil {
		if replayed, found, replayErr := s.intake.Replay(ctx, intakeRequest); replayErr != nil || found {
			return replayed, replayErr
		}
		return ascpintake.Operation{}, err
	}
	if directoryContract != s.directoryContract {
		return ascpintake.Operation{}, ErrUnsupportedTerms
	}
	intakeRequest.Evidence = evidence
	return s.intake.Create(ctx, intakeRequest)
}

func (s *Service) Get(ctx context.Context, identity Identity, operationID string) (ascpintake.Operation, error) {
	if !validIdentity(identity) || !validOperationID(operationID) {
		return ascpintake.Operation{}, ErrInvalidIdentity
	}
	return s.reader.Get(ctx, identity.OrganizationID, identity.AgentID, operationID)
}

func validIdentity(identity Identity) bool {
	return validIdentifier(identity.OrganizationID) && validIdentifier(identity.AgentID)
}

func validIdentifier(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for index, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || index > 0 && (character == '.' || character == '_' || character == ':' || character == '-')) {
			return false
		}
	}
	return true
}

func validOperationID(value string) bool {
	if len(value) != 66 || !strings.HasPrefix(value, "0x") || strings.ToLower(value) != value || value == "0x"+strings.Repeat("0", 64) {
		return false
	}
	for _, character := range value[2:] {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}
