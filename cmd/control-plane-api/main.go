package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gnanam1990/flowops/internal/ascpactivation"
	"github.com/gnanam1990/flowops/internal/ascpadaptation"
	"github.com/gnanam1990/flowops/internal/ascpadaptationsigner"
	"github.com/gnanam1990/flowops/internal/ascpagent"
	"github.com/gnanam1990/flowops/internal/ascpbearer"
	"github.com/gnanam1990/flowops/internal/ascpcapacity"
	"github.com/gnanam1990/flowops/internal/ascpexecauth"
	"github.com/gnanam1990/flowops/internal/ascpgovernanceobserver"
	"github.com/gnanam1990/flowops/internal/ascpintake"
	"github.com/gnanam1990/flowops/internal/ascporchestration"
	"github.com/gnanam1990/flowops/internal/ascpring6"
	"github.com/gnanam1990/flowops/internal/ascpsettlement"
	"github.com/gnanam1990/flowops/internal/ascpsignerbinding"
	"github.com/gnanam1990/flowops/internal/ascpworkflow"
	"github.com/gnanam1990/flowops/internal/controlapi"
	"github.com/gnanam1990/flowops/internal/controlplane"
	"github.com/gnanam1990/flowops/internal/directoryreader"
	"github.com/gnanam1990/flowops/internal/mcp"
	"github.com/gnanam1990/flowops/internal/reconciliation"
	"github.com/gnanam1990/flowops/internal/releaseadmission"
	"github.com/gnanam1990/flowops/pkg/pilotlimits"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	defaultAddress = "127.0.0.1:8080"
)

type startupConfig struct {
	address                   string
	databaseURL               string
	envelopeKeyID             string
	envelopeKey               ed25519.PrivateKey
	siteSessionKey            []byte
	reconciliation            string
	trustProxy                bool
	applyMigrations           bool
	operatorKey               []byte
	keeperCallbackKey         []byte
	mcpAllowedOrigins         []string
	observerRPCs              []reconciliation.RPCProvider
	observerConfig            reconciliation.Config
	observerInterval          time.Duration
	observerTimeout           time.Duration
	reconciliationInterval    time.Duration
	reconciliationTimeout     time.Duration
	baseMainnetRelease        *releaseadmission.Manifest
	signerReceiptKeys         []controlapi.BroadcastKey
	pilotLimits               *pilotlimits.Limits
	ascpDirectoryContract     string
	ascpAgentRegistryContract string
	ascpCallEscrowContract    string
	ascpSpendModuleContract   string
	ascpGovernanceFromBlock   uint64
	ascpDirectoryMaxAge       time.Duration
	ascpMaxActiveOperations   int
	ascpAdaptationSigner      string
	ascpAdaptationKeyID       string
	ascpAdaptationKeyEpoch    uint64
	ascpAdaptationHSM         string
	ascpAdaptationTimeout     time.Duration
	ascpAuthorityRules        []ascpworkflow.AuthorityRule
}

func main() {
	if err := run(context.Background()); err != nil {
		slog.Error("control plane stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) (returnErr error) {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if cfg.baseMainnetRelease != nil {
		admissionCtx, admissionCancel := context.WithTimeout(ctx, 15*time.Second)
		verifyErr := releaseadmission.VerifyCodeQuorum(admissionCtx, cfg.observerRPCs, *cfg.baseMainnetRelease, nil)
		admissionCancel()
		if verifyErr != nil {
			return fmt.Errorf("verify Base mainnet release bytecode: %w", verifyErr)
		}
	}
	db, err := sql.Open("pgx", cfg.databaseURL)
	if err != nil {
		return fmt.Errorf("open PostgreSQL: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	startupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := db.PingContext(startupCtx); err != nil {
		return fmt.Errorf("ping PostgreSQL: %w", err)
	}
	if cfg.applyMigrations {
		if err := controlapi.ApplyMigrations(startupCtx, db); err != nil {
			return err
		}
	}
	siteSessions, err := controlapi.NewSiteSessionCodec(cfg.siteSessionKey, 2*time.Minute, nil)
	if err != nil {
		return fmt.Errorf("create site session codec: %w", err)
	}
	store, err := controlapi.NewPostgresStore(db, siteSessions)
	if err != nil {
		return err
	}
	signerBindingStore, err := ascpsignerbinding.NewStore(db, cfg.observerConfig.ChainID)
	if err != nil {
		return fmt.Errorf("create ASCP signer binding store: %w", err)
	}
	workflowStore, err := ascpworkflow.NewPostgresStore(db)
	if err != nil {
		return fmt.Errorf("create ASCP proposal workflow store: %w", err)
	}
	var ascpAgentService *ascpagent.Service
	var ascpOrchestrationService *ascporchestration.Service
	var ascpActivationService *ascpactivation.Service
	if cfg.ascpDirectoryContract != "" {
		intakeStore, err := ascpintake.NewPostgresStore(db)
		if err != nil {
			return fmt.Errorf("create ASCP intake store: %w", err)
		}
		intakeService, err := ascpintake.New(intakeStore, nil, nil)
		if err != nil {
			return fmt.Errorf("create ASCP intake service: %w", err)
		}
		directoryResolver, err := directoryreader.NewMaterializedResolver(db, cfg.observerConfig.ChainID, cfg.ascpDirectoryContract, cfg.ascpDirectoryMaxAge, cfg.observerConfig.MaxFutureClockSkew)
		if err != nil {
			return fmt.Errorf("create ASCP directory resolver: %w", err)
		}
		adaptationStore, err := ascpadaptation.NewPostgresStore(db)
		if err != nil {
			return fmt.Errorf("create ASCP adaptation store: %w", err)
		}
		var adaptationService *ascpadaptation.Service
		if cfg.ascpAdaptationSigner != "" {
			adaptationBoundary, err := ascpring6.NewComponentBoundary("hsm", cfg.ascpAdaptationHSM, cfg.ascpAdaptationTimeout)
			if err != nil {
				return fmt.Errorf("create adaptation HSM boundary: %w", err)
			}
			defer func() { returnErr = errors.Join(returnErr, adaptationBoundary.Close()) }()
			if err := adaptationBoundary.Check(startupCtx); err != nil {
				return fmt.Errorf("check adaptation HSM boundary: %w", err)
			}
			hsm, err := ascpring6.NewUnixHSM(adaptationBoundary)
			if err != nil {
				return err
			}
			digestSigner, err := ascpadaptationsigner.New(hsm, cfg.ascpAdaptationKeyID, cfg.ascpAdaptationKeyEpoch, cfg.ascpAdaptationSigner)
			if err != nil {
				return err
			}
			issuer, err := ascpadaptation.NewIssuer(digestSigner, nil)
			if err != nil {
				return err
			}
			adaptationService, err = ascpadaptation.NewService(issuer, adaptationStore)
			if err != nil {
				return err
			}
		}
		ascpAgentService, err = ascpagent.New(ascpagent.Config{
			Intake: intakeService, Reader: intakeStore, Directory: directoryResolver,
			DirectoryContract: cfg.ascpDirectoryContract, ChainID: cfg.observerConfig.ChainID,
			Asset: cfg.observerConfig.EscrowAsset, SchemeVersion: 1,
			AdaptationSigner: cfg.ascpAdaptationSigner, Adaptations: adaptationStore,
		})
		if err != nil {
			return fmt.Errorf("create ASCP agent service: %w", err)
		}
		localRevalidator, err := ascpexecauth.NewLocalRevalidator(cfg.ascpDirectoryMaxAge)
		if err != nil {
			return fmt.Errorf("create ASCP execution revalidator: %w", err)
		}
		capacityGate, err := ascpcapacity.NewPostgresGate(cfg.ascpMaxActiveOperations)
		if err != nil {
			return fmt.Errorf("create ASCP capacity gate: %w", err)
		}
		executionStore, err := ascpexecauth.NewPostgresStore(db, localRevalidator, capacityGate)
		if err != nil {
			return fmt.Errorf("create ASCP execution authorization store: %w", err)
		}
		orchestrationStore, err := ascporchestration.NewPostgresStore(db)
		if err != nil {
			return fmt.Errorf("create ASCP orchestration store: %w", err)
		}
		ascpOrchestrationService, err = ascporchestration.New(ascporchestration.Config{
			DatabaseStore: orchestrationStore, Authorization: executionStore,
			EscrowContract: cfg.observerConfig.EscrowContract,
			SettleWindow:   time.Duration(cfg.observerConfig.EscrowReleaseWindow) * time.Second,
			Adaptations:    adaptationService,
		})
		if err != nil {
			return fmt.Errorf("create ASCP orchestration service: %w", err)
		}
		activationStore, err := ascpbearer.NewActivationStore(db)
		if err != nil {
			return fmt.Errorf("create ASCP activation store: %w", err)
		}
		ascpActivationService, err = ascpactivation.New(ascpactivation.Config{
			Authorizations: ascpOrchestrationService,
			Bindings:       signerBindingStore,
			Store:          activationStore,
		})
		if err != nil {
			return fmt.Errorf("create ASCP activation service: %w", err)
		}
	} else {
		slog.Warn("durable ASCP agent intake is disabled", "reason", "FLOWOPS_ASCP_DIRECTORY_CONTRACT is unset")
	}
	eventJournal, err := controlplane.OpenPostgresJournal(startupCtx, db)
	if err != nil {
		return err
	}
	policyProvider, err := controlapi.NewPostgresPolicyProvider(db)
	if err != nil {
		return err
	}
	reconciliationEngine, err := reconciliation.Open(cfg.reconciliation, cfg.observerConfig)
	if err != nil {
		return fmt.Errorf("open reconciliation state: %w", err)
	}
	defer reconciliationEngine.Close()

	lifecycle, err := controlplane.New(controlplane.Config{
		PolicyProvider: policyProvider, Journal: eventJournal,
		FreezeGate: controlapi.AgentFreezeGate{Store: store}, ChainGate: reconciliationEngine,
		ApprovalTTL: 15 * time.Minute, AuthorizationTTL: 5 * time.Minute,
		RequestIDSource:       func() (string, error) { return randomID("req") },
		AuthorizationIDSource: func() (string, error) { return randomID("auth") },
		NonceSource:           func() (string, error) { return randomNonce() },
		EnvelopeKeyID:         cfg.envelopeKeyID, EnvelopePrivateKey: cfg.envelopeKey,
		PilotLimits: cfg.pilotLimits,
	})
	if err != nil {
		return fmt.Errorf("create lifecycle: %w", err)
	}
	if _, err := lifecycle.SweepExpired(startupCtx); err != nil {
		return fmt.Errorf("sweep expired approvals at startup: %w", err)
	}
	escrowRegistrar, err := controlapi.NewEscrowRegistrar(lifecycle, reconciliationEngine, nil)
	if err != nil {
		return fmt.Errorf("create escrow registrar: %w", err)
	}
	var signerBroadcasts controlapi.BroadcastRegistrar
	var signerEscrowBroadcasts controlapi.EscrowBroadcastRegistrar
	if len(cfg.signerReceiptKeys) > 0 {
		keys, err := controlapi.NewStaticBroadcastKeys(cfg.signerReceiptKeys)
		if err != nil {
			return fmt.Errorf("create customer signer receipt key registry: %w", err)
		}
		signerBroadcasts, err = controlapi.NewSignerBroadcastRegistrar(lifecycle, keys, reconciliationEngine, nil)
		if err != nil {
			return fmt.Errorf("create customer signer broadcast registrar: %w", err)
		}
		signerEscrowBroadcasts, err = controlapi.NewSignerEscrowBroadcastRegistrar(lifecycle, keys, reconciliationEngine, nil)
		if err != nil {
			return fmt.Errorf("create customer signer escrow broadcast registrar: %w", err)
		}
	}
	observers, err := reconciliation.NewObserverSet(cfg.observerConfig.ChainID, cfg.observerRPCs, nil, nil)
	if err != nil {
		return fmt.Errorf("create Base observer set: %w", err)
	}
	var governanceObserver *ascpgovernanceobserver.Observer
	if cfg.ascpGovernanceFromBlock != 0 {
		governanceObserver, err = ascpgovernanceobserver.New(ascpgovernanceobserver.Config{
			Observers: observers, Quorum: cfg.observerConfig.ObserverQuorum,
			FinalizedConfirmations: cfg.observerConfig.ReorgLookback + 1, FromBlock: cfg.ascpGovernanceFromBlock,
			CallEscrowContract: cfg.ascpCallEscrowContract, SpendModuleContract: cfg.ascpSpendModuleContract,
			DirectoryContract: cfg.ascpDirectoryContract,
		})
		if err != nil {
			return fmt.Errorf("create ASCP governance receipt observer: %w", err)
		}
	}
	workflowOptions := make([]ascpworkflow.Option, 0, 1)
	if len(cfg.ascpAuthorityRules) != 0 {
		authorityVerifier, authorityErr := ascpworkflow.NewAuthorityVerifier(
			cfg.ascpAuthorityRules, cfg.observerConfig.ObserverQuorum,
		)
		if authorityErr != nil {
			return fmt.Errorf("create ASCP chain authority verifier: %w", authorityErr)
		}
		workflowOptions = append(workflowOptions, ascpworkflow.WithGovernanceActionGate(authorityVerifier))
	}
	workflowService, err := ascpworkflow.New(workflowStore, governanceObserver, nil, nil, workflowOptions...)
	if err != nil {
		return fmt.Errorf("create ASCP proposal workflow service: %w", err)
	}
	var governanceWorker *ascpgovernanceobserver.Worker
	if governanceObserver != nil {
		governanceWorker, err = ascpgovernanceobserver.NewWorker(workflowStore, workflowService, ascpgovernanceobserver.WorkerConfig{
			Interval: cfg.reconciliationInterval, QueryTimeout: cfg.reconciliationTimeout, BatchSize: 1000,
			OnCycle: func(cycle ascpgovernanceobserver.WorkerCycle) {
				slog.Info("ASCP governance receipt cycle completed", "pending", cycle.Pending,
					"completed", cycle.Completed, "deferred", cycle.Deferred, "rejected", cycle.Rejected)
			},
		})
		if err != nil {
			return fmt.Errorf("create ASCP governance receipt worker: %w", err)
		}
	} else {
		slog.Warn("ASCP chain governance workflows are disabled", "reason", "governance contract tuple is unset")
	}
	observerSupervisor, err := reconciliation.NewSupervisor(observers, reconciliationEngine, reconciliation.SupervisorConfig{
		Interval: cfg.observerInterval, ObservationTimeout: cfg.observerTimeout,
		OnResult: func(status reconciliation.ChainStatus, result reconciliation.SnapshotResult) {
			slog.Info("Base observer snapshot persisted", "state", status.State, "responding", len(result.Observations), "failed", len(result.Failures), "required", status.RequiredObserverQuorum)
		},
	})
	if err != nil {
		return fmt.Errorf("create Base observer supervisor: %w", err)
	}
	reconciliationWorker, err := reconciliation.NewWorker(observers, reconciliationEngine, reconciliation.WorkerConfig{
		Interval: cfg.reconciliationInterval, QueryTimeout: cfg.reconciliationTimeout,
		OnCycle: func(cycle reconciliation.WorkerCycle) {
			slog.Info("Base reconciliation cycle completed", "examined", cycle.Examined, "receiptCandidates", cycle.ReceiptCandidates, "settled", cycle.Settled, "reverted", cycle.Reverted, "finalityConfirmed", cycle.FinalityConfirmed, "reorgsReopened", cycle.ReorgsReopened, "escrowCandidates", cycle.EscrowCandidates, "escrowConfirmed", cycle.EscrowConfirmed, "escrowReverted", cycle.EscrowReverted, "escrowFinalized", cycle.EscrowFinalized, "escrowReorgs", cycle.EscrowReorgs, "deferred", cycle.Deferred, "skippedForChain", cycle.SkippedForChain)
		},
	})
	if err != nil {
		return fmt.Errorf("create Base reconciliation worker: %w", err)
	}
	ascpSettlementStore, err := ascpsettlement.NewPostgresStore(db)
	if err != nil {
		return fmt.Errorf("create ASCP settlement store: %w", err)
	}
	ascpSettlementReader, err := ascpsettlement.NewReader(ascpsettlement.ReaderConfig{
		Observers: observers, Quorum: cfg.observerConfig.ObserverQuorum,
		SafeConfirmations:      cfg.observerConfig.MinConfirmations,
		FinalizedConfirmations: cfg.observerConfig.ReorgLookback + 1,
	})
	if err != nil {
		return fmt.Errorf("create ASCP settlement reader: %w", err)
	}
	ascpSettlementWorker, err := ascpsettlement.NewWorker(ascpSettlementStore, ascpSettlementReader, ascpsettlement.WorkerConfig{
		Interval: cfg.reconciliationInterval, QueryTimeout: cfg.reconciliationTimeout, BatchSize: 100,
		OnCycle: func(cycle ascpsettlement.WorkerCycle) {
			slog.Info("ASCP settlement cycle completed", "pending", cycle.Pending, "applied", cycle.Applied,
				"finalityChecks", cycle.FinalityChecks, "canonicalConfirmed", cycle.CanonicalConfirmed,
				"reorgsRecovered", cycle.ReorgsRecovered, "deferred", cycle.Deferred)
		},
	})
	if err != nil {
		return fmt.Errorf("create ASCP settlement worker: %w", err)
	}
	api, err := controlapi.NewServer(controlapi.ServerConfig{
		Store: store, Lifecycle: lifecycle, Chain: reconciliationEngine, SiteSessions: siteSessions,
		OperatorControlKey: cfg.operatorKey, KeeperCallbackKey: cfg.keeperCallbackKey,
		SignerBroadcasts: signerBroadcasts, SignerEscrowBroadcasts: signerEscrowBroadcasts, Escrow: escrowRegistrar,
		Reconciliation:     reconciliationEngine,
		ASCPAgent:          ascpAgentService,
		ASCPFlow:           ascpOrchestrationService,
		ASCPActivation:     ascpActivationService,
		ASCPSignerBindings: signerBindingStore,
		ASCPWorkflows:      workflowService,
		ASCPSettlement:     ascpSettlementRegistrar{store: ascpSettlementStore},
	})
	if err != nil {
		return err
	}
	mcpServer, err := mcp.NewServer(mcp.Config{Delegate: api, AllowedOrigins: cfg.mcpAllowedOrigins, MaxRequestBytes: 2 * 1024 * 1024})
	if err != nil {
		return fmt.Errorf("create MCP server: %w", err)
	}
	httpServer := &http.Server{
		Addr: cfg.address, Handler: enforceTransportSecurity(publicHandler(api, mcpServer), cfg.trustProxy),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second,
		MaxHeaderBytes: 32 * 1024,
	}
	serverErrors := make(chan error, 1)
	go func() {
		slog.Info("control plane listening", "address", cfg.address, "chainId", cfg.observerConfig.ChainID, "observerCount", len(cfg.observerRPCs))
		err := httpServer.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
		close(serverErrors)
	}()

	shutdownSignal, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	maintenanceErrors := make(chan error, 1)
	go func() {
		maintenanceErrors <- runExpirySweeper(shutdownSignal, lifecycle, 30*time.Second)
	}()
	observerErrors := make(chan error, 1)
	go func() {
		observerErrors <- observerSupervisor.Run(shutdownSignal)
	}()
	reconciliationErrors := make(chan error, 1)
	go func() {
		reconciliationErrors <- reconciliationWorker.Run(shutdownSignal)
	}()
	ascpSettlementErrors := make(chan error, 1)
	go func() {
		ascpSettlementErrors <- ascpSettlementWorker.Run(shutdownSignal)
	}()
	var governanceErrors chan error
	if governanceWorker != nil {
		governanceErrors = make(chan error, 1)
		go func() {
			governanceErrors <- governanceWorker.Run(shutdownSignal)
		}()
	}
	select {
	case <-shutdownSignal.Done():
	case err := <-serverErrors:
		if err != nil {
			return fmt.Errorf("serve control-plane API: %w", err)
		}
	case err := <-maintenanceErrors:
		if err != nil {
			return err
		}
	case err := <-observerErrors:
		if err != nil {
			return err
		}
	case err := <-reconciliationErrors:
		if err != nil {
			return err
		}
	case err := <-ascpSettlementErrors:
		if err != nil {
			return fmt.Errorf("run ASCP settlement worker: %w", err)
		}
	case err := <-governanceErrors:
		if err != nil {
			return fmt.Errorf("run ASCP governance receipt worker: %w", err)
		}
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	return httpServer.Shutdown(shutdownCtx)
}

func runExpirySweeper(ctx context.Context, lifecycle *controlplane.Lifecycle, interval time.Duration) error {
	if lifecycle == nil || interval <= 0 {
		return errors.New("expiry sweeper requires a lifecycle and positive interval")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, err := lifecycle.SweepExpired(ctx); err != nil {
				return fmt.Errorf("sweep expired approvals: %w", err)
			}
		}
	}
}

func loadConfig() (startupConfig, error) {
	key, err := decodePrivateKey(os.Getenv("FLOWOPS_ENVELOPE_PRIVATE_KEY_B64"))
	if err != nil {
		return startupConfig{}, err
	}
	siteSessionKey, err := decodeSymmetricKey("FLOWOPS_SITE_SESSION_KEY_B64", os.Getenv("FLOWOPS_SITE_SESSION_KEY_B64"))
	if err != nil {
		return startupConfig{}, err
	}
	cfg := startupConfig{
		address: strings.TrimSpace(os.Getenv("FLOWOPS_CONTROL_ADDR")), databaseURL: strings.TrimSpace(os.Getenv("FLOWOPS_DATABASE_URL")),
		envelopeKeyID: strings.TrimSpace(os.Getenv("FLOWOPS_ENVELOPE_KEY_ID")),
		envelopeKey:   key, siteSessionKey: siteSessionKey,
		reconciliation:            strings.TrimSpace(os.Getenv("FLOWOPS_RECONCILIATION_JOURNAL")),
		mcpAllowedOrigins:         splitMCPOrigins(os.Getenv("FLOWOPS_MCP_ALLOWED_ORIGINS")),
		ascpDirectoryContract:     strings.ToLower(strings.TrimSpace(os.Getenv("FLOWOPS_ASCP_DIRECTORY_CONTRACT"))),
		ascpAgentRegistryContract: strings.ToLower(strings.TrimSpace(os.Getenv("FLOWOPS_ASCP_AGENT_REGISTRY_CONTRACT"))),
		ascpCallEscrowContract:    strings.ToLower(strings.TrimSpace(os.Getenv("FLOWOPS_ASCP_CALL_ESCROW_CONTRACT"))),
		ascpSpendModuleContract:   strings.ToLower(strings.TrimSpace(os.Getenv("FLOWOPS_ASCP_SPEND_MODULE_CONTRACT"))),
		ascpAdaptationSigner:      strings.ToLower(strings.TrimSpace(os.Getenv("FLOWOPS_ASCP_ADAPTATION_SIGNER_ADDRESS"))),
		ascpAdaptationKeyID:       strings.TrimSpace(os.Getenv("FLOWOPS_ASCP_ADAPTATION_KEY_ID")),
		ascpAdaptationHSM:         strings.TrimSpace(os.Getenv("FLOWOPS_ASCP_ADAPTATION_HSM_SOCKET")),
	}
	ascpDirectoryMaxAge, err := parseDurationEnv("FLOWOPS_ASCP_DIRECTORY_MAX_AGE", os.Getenv("FLOWOPS_ASCP_DIRECTORY_MAX_AGE"), time.Minute)
	if err != nil {
		return startupConfig{}, err
	}
	if ascpDirectoryMaxAge > 5*time.Minute {
		return startupConfig{}, errors.New("FLOWOPS_ASCP_DIRECTORY_MAX_AGE cannot exceed 5m")
	}
	cfg.ascpDirectoryMaxAge = ascpDirectoryMaxAge
	maxActiveRaw := strings.TrimSpace(os.Getenv("FLOWOPS_ASCP_MAX_ACTIVE_OPERATIONS"))
	if maxActiveRaw == "" {
		maxActiveRaw = "1000"
	}
	maxActive, err := strconv.Atoi(maxActiveRaw)
	if err != nil || maxActive < 1 || maxActive > 100000 || strconv.Itoa(maxActive) != maxActiveRaw {
		return startupConfig{}, errors.New("FLOWOPS_ASCP_MAX_ACTIVE_OPERATIONS must be a canonical integer from 1 through 100000")
	}
	cfg.ascpMaxActiveOperations = maxActive
	adaptationTimeoutRaw := os.Getenv("FLOWOPS_ASCP_ADAPTATION_HSM_TIMEOUT")
	ascpAdaptationTimeout, err := parseDurationEnv("FLOWOPS_ASCP_ADAPTATION_HSM_TIMEOUT", adaptationTimeoutRaw, 3*time.Second)
	if err != nil {
		return startupConfig{}, err
	}
	cfg.ascpAdaptationTimeout = ascpAdaptationTimeout
	operatorKey, err := decodeSymmetricKey("FLOWOPS_OPERATOR_CONTROL_KEY_B64", os.Getenv("FLOWOPS_OPERATOR_CONTROL_KEY_B64"))
	if err != nil {
		return startupConfig{}, err
	}
	cfg.operatorKey = operatorKey
	keeperCallbackKey, err := decodeSymmetricKey("FLOWOPS_ASCP_KEEPER_CALLBACK_KEY_B64", os.Getenv("FLOWOPS_ASCP_KEEPER_CALLBACK_KEY_B64"))
	if err != nil {
		return startupConfig{}, err
	}
	if subtle.ConstantTimeCompare(operatorKey, keeperCallbackKey) == 1 || subtle.ConstantTimeCompare(siteSessionKey, keeperCallbackKey) == 1 {
		return startupConfig{}, errors.New("ASCP keeper callback key must be distinct from operator and site-session keys")
	}
	cfg.keeperCallbackKey = keeperCallbackKey
	signerReceiptKeys, err := parseSignerKeys(os.Getenv("FLOWOPS_SIGNER_RECEIPT_KEYS_JSON"))
	if err != nil {
		return startupConfig{}, err
	}
	cfg.signerReceiptKeys = signerReceiptKeys
	pilot, err := pilotlimits.Compile(pilotlimits.Config{
		MaxPerActionAtomic:   strings.TrimSpace(os.Getenv("FLOWOPS_PILOT_MAX_PER_ACTION_ATOMIC")),
		MaxOutstandingAtomic: strings.TrimSpace(os.Getenv("FLOWOPS_PILOT_MAX_OUTSTANDING_ATOMIC")),
	})
	if err != nil {
		return startupConfig{}, fmt.Errorf("pilot limits: %w", err)
	}
	cfg.pilotLimits = pilot
	observerRuntime, err := loadObserverRuntimeConfig()
	if err != nil {
		return startupConfig{}, err
	}
	cfg.observerRPCs = observerRuntime.providers
	cfg.observerConfig = observerRuntime.engine
	cfg.observerInterval = observerRuntime.interval
	cfg.observerTimeout = observerRuntime.timeout
	cfg.reconciliationInterval = observerRuntime.reconciliationInterval
	cfg.reconciliationTimeout = observerRuntime.reconciliationTimeout
	cfg.baseMainnetRelease = observerRuntime.releaseManifest
	authorityRulesRaw := strings.TrimSpace(os.Getenv("FLOWOPS_ASCP_CHAIN_AUTHORITY_RULES_JSON"))
	if authorityRulesRaw != "" {
		cfg.ascpAuthorityRules, err = ascpworkflow.ParseAuthorityRules(authorityRulesRaw)
		if err != nil {
			return startupConfig{}, fmt.Errorf("FLOWOPS_ASCP_CHAIN_AUTHORITY_RULES_JSON: %w", err)
		}
		for _, rule := range cfg.ascpAuthorityRules {
			if rule.ChainID != cfg.observerConfig.ChainID {
				return startupConfig{}, errors.New("ASCP chain authority rules must use the configured observer chain")
			}
		}
	}
	if cfg.ascpDirectoryContract != "" {
		if len(cfg.ascpDirectoryContract) != 42 || !common.IsHexAddress(cfg.ascpDirectoryContract) || common.HexToAddress(cfg.ascpDirectoryContract) == (common.Address{}) {
			return startupConfig{}, errors.New("FLOWOPS_ASCP_DIRECTORY_CONTRACT must be a non-zero canonical address")
		}
		if cfg.observerConfig.EscrowAsset == "" {
			return startupConfig{}, errors.New("FLOWOPS_ASCP_DIRECTORY_CONTRACT requires the reviewed escrow deployment tuple")
		}
	}
	if cfg.ascpAgentRegistryContract != "" && !canonicalContract(cfg.ascpAgentRegistryContract) {
		return startupConfig{}, errors.New("FLOWOPS_ASCP_AGENT_REGISTRY_CONTRACT must be a non-zero canonical address")
	}
	governanceFromBlockRaw := strings.TrimSpace(os.Getenv("FLOWOPS_ASCP_GOVERNANCE_FROM_BLOCK"))
	governanceConfigured := cfg.ascpAgentRegistryContract != "" || cfg.ascpCallEscrowContract != "" || cfg.ascpSpendModuleContract != "" || governanceFromBlockRaw != ""
	if governanceConfigured {
		if cfg.ascpDirectoryContract == "" || !canonicalContract(cfg.ascpCallEscrowContract) ||
			!canonicalContract(cfg.ascpSpendModuleContract) || cfg.ascpCallEscrowContract == cfg.ascpSpendModuleContract ||
			cfg.ascpCallEscrowContract == cfg.ascpDirectoryContract || cfg.ascpSpendModuleContract == cfg.ascpDirectoryContract ||
			(cfg.ascpAgentRegistryContract != "" && (cfg.ascpAgentRegistryContract == cfg.ascpDirectoryContract ||
				cfg.ascpAgentRegistryContract == cfg.ascpCallEscrowContract || cfg.ascpAgentRegistryContract == cfg.ascpSpendModuleContract)) {
			return startupConfig{}, errors.New("governance observation requires distinct canonical ASCP contract addresses")
		}
		fromBlock, err := strconv.ParseUint(governanceFromBlockRaw, 10, 64)
		if err != nil || fromBlock == 0 || strconv.FormatUint(fromBlock, 10) != governanceFromBlockRaw {
			return startupConfig{}, errors.New("FLOWOPS_ASCP_GOVERNANCE_FROM_BLOCK must be a positive canonical integer")
		}
		cfg.ascpGovernanceFromBlock = fromBlock
	}
	if cfg.ascpAdaptationSigner != "" && (len(cfg.ascpAdaptationSigner) != 42 || !common.IsHexAddress(cfg.ascpAdaptationSigner) || common.HexToAddress(cfg.ascpAdaptationSigner) == (common.Address{})) {
		return startupConfig{}, errors.New("FLOWOPS_ASCP_ADAPTATION_SIGNER_ADDRESS must be a non-zero canonical address")
	}
	adaptationEpochRaw := strings.TrimSpace(os.Getenv("FLOWOPS_ASCP_ADAPTATION_KEY_EPOCH"))
	adaptationConfigured := cfg.ascpAdaptationSigner != "" || cfg.ascpAdaptationKeyID != "" || adaptationEpochRaw != "" || cfg.ascpAdaptationHSM != "" || strings.TrimSpace(adaptationTimeoutRaw) != ""
	if adaptationConfigured {
		if cfg.ascpDirectoryContract == "" || cfg.ascpAdaptationSigner == "" || !ascpring6.ValidIdentifier(cfg.ascpAdaptationKeyID) || !ascpring6.ValidSocketPath(cfg.ascpAdaptationHSM) || cfg.ascpAdaptationTimeout < time.Second || cfg.ascpAdaptationTimeout > 10*time.Second {
			return startupConfig{}, errors.New("adaptation signing requires the directory, signer, key ID, epoch, secure HSM socket, and a 1s through 10s timeout")
		}
		epoch, err := strconv.ParseUint(adaptationEpochRaw, 10, 64)
		if err != nil || epoch == 0 || strconv.FormatUint(epoch, 10) != adaptationEpochRaw {
			return startupConfig{}, errors.New("FLOWOPS_ASCP_ADAPTATION_KEY_EPOCH must be a positive canonical integer")
		}
		cfg.ascpAdaptationKeyEpoch = epoch
	}
	if cfg.observerConfig.ChainID == 8453 {
		if err := cfg.pilotLimits.RequireInitialBaseMainnetProfile(); err != nil {
			return startupConfig{}, err
		}
		if observerRuntime.releaseManifest == nil {
			return startupConfig{}, errors.New("Base mainnet release admission is unavailable")
		}
		if !canonicalContract(cfg.ascpAgentRegistryContract) || cfg.ascpCallEscrowContract != cfg.observerConfig.EscrowContract {
			return startupConfig{}, errors.New("Base mainnet requires the complete ASCP contract tuple and exact reconciliation escrow binding")
		}
		if err := releaseadmission.BindRuntime(*observerRuntime.releaseManifest, releaseadmission.RuntimeBindings{
			EscrowAsset: cfg.observerConfig.EscrowAsset, DirectoryContract: cfg.ascpDirectoryContract,
			AgentRegistry: cfg.ascpAgentRegistryContract, CallEscrow: cfg.ascpCallEscrowContract,
			SpendModule: cfg.ascpSpendModuleContract, PilotPerAction: cfg.pilotLimits.MaxPerActionAtomic(),
			PilotOutstanding: cfg.pilotLimits.MaxOutstandingAtomic(), GovernanceFromBlock: cfg.ascpGovernanceFromBlock,
			SettlementWindowSeconds: cfg.observerConfig.EscrowReleaseWindow,
		}); err != nil {
			return startupConfig{}, err
		}
	}
	trustProxy, err := parseStrictBool("FLOWOPS_TRUST_PROXY_HEADERS", os.Getenv("FLOWOPS_TRUST_PROXY_HEADERS"))
	if err != nil {
		return startupConfig{}, err
	}
	cfg.trustProxy = trustProxy
	applyMigrations, err := parseStrictBoolDefault("FLOWOPS_APPLY_MIGRATIONS", os.Getenv("FLOWOPS_APPLY_MIGRATIONS"), true)
	if err != nil {
		return startupConfig{}, err
	}
	cfg.applyMigrations = applyMigrations
	if cfg.address == "" {
		port := strings.TrimSpace(os.Getenv("PORT"))
		if port == "" {
			cfg.address = defaultAddress
		} else {
			parsedPort, err := strconv.ParseUint(port, 10, 16)
			if err != nil || parsedPort == 0 {
				return startupConfig{}, errors.New("PORT must be a positive TCP port")
			}
			cfg.address = "0.0.0.0:" + port
		}
	}
	if err := validateListenAddress(cfg.address, cfg.trustProxy); err != nil {
		return startupConfig{}, err
	}
	if cfg.databaseURL == "" || cfg.envelopeKeyID == "" || cfg.reconciliation == "" {
		return startupConfig{}, errors.New("database, signing, session, operator-control, observer, and reconciliation configuration are required")
	}
	return cfg, nil
}

func canonicalContract(value string) bool {
	return len(value) == 42 && common.IsHexAddress(value) && value == strings.ToLower(value) &&
		common.HexToAddress(value) != (common.Address{})
}

func splitMCPOrigins(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	values := strings.Split(value, ",")
	for index := range values {
		values[index] = strings.TrimSpace(values[index])
	}
	return values
}

func publicHandler(api, mcpServer http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpServer)
	mux.Handle("/", api)
	return mux
}

func decodeSymmetricKey(name, value string) ([]byte, error) {
	if strings.TrimSpace(value) == "" {
		return nil, fmt.Errorf("%s is required", name)
	}
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(raw) != 32 {
		return nil, fmt.Errorf("%s must encode exactly 32 bytes", name)
	}
	return append([]byte(nil), raw...), nil
}

func validateListenAddress(address string, trustProxy bool) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("FLOWOPS_CONTROL_ADDR must be a host:port listen address: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return errors.New("FLOWOPS_CONTROL_ADDR host must be an IP address")
	}
	if !ip.IsLoopback() && !trustProxy {
		return errors.New("non-loopback FLOWOPS_CONTROL_ADDR requires FLOWOPS_TRUST_PROXY_HEADERS=true")
	}
	return nil
}

func parseStrictBool(name, value string) (bool, error) {
	return parseStrictBoolDefault(name, value, false)
}

func parseStrictBoolDefault(name, value string, defaultValue bool) (bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultValue, nil
	}
	if value == "false" {
		return false, nil
	}
	if value == "true" {
		return true, nil
	}
	return false, fmt.Errorf("%s must be true or false", name)
}

func enforceTransportSecurity(next http.Handler, trustProxy bool) http.Handler {
	if !trustProxy {
		return next
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/health" && firstForwardedValue(request.Header.Get("X-Forwarded-Proto")) != "https" {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte(`{"error":{"code":"HTTPS_REQUIRED","message":"secure transport is required"}}`))
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func firstForwardedValue(value string) string {
	first, _, _ := strings.Cut(value, ",")
	return strings.ToLower(strings.TrimSpace(first))
}

func decodePrivateKey(value string) (ed25519.PrivateKey, error) {
	if strings.TrimSpace(value) == "" {
		return nil, errors.New("FLOWOPS_ENVELOPE_PRIVATE_KEY_B64 is required")
	}
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return nil, errors.New("FLOWOPS_ENVELOPE_PRIVATE_KEY_B64 must encode one Ed25519 private key")
	}
	canonical := ed25519.NewKeyFromSeed(raw[:ed25519.SeedSize])
	if subtle.ConstantTimeCompare(raw, canonical) != 1 {
		return nil, errors.New("FLOWOPS_ENVELOPE_PRIVATE_KEY_B64 is not a canonical Ed25519 private key")
	}
	return ed25519.PrivateKey(append([]byte(nil), raw...)), nil
}

func randomID(prefix string) (string, error) { return controlapiRandomID(prefix) }

func randomNonce() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "0x" + hex.EncodeToString(raw), nil
}

func controlapiRandomID(prefix string) (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(raw), nil
}
