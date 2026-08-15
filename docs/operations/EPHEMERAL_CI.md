# Ephemeral CI Runner

Status: inactive fallback; hosted GitHub Actions runs on `ubuntu-24.04-arm`

## Security posture

- If reactivated, the `go` job targets a repository-scoped, Linux ARM64 runner
  carrying the custom `flowops-ephemeral` label.
- The runner is registered with GitHub's `--ephemeral` option and automatically
  deregisters after exactly one job.
- Each runner executes inside a fresh local Linux virtual machine/container and
  its filesystem is destroyed after the job.
- The disposable Linux image must provide CA certificates, Git, `jq`, and a C
  toolchain (`build-essential`). Go race detection needs the C toolchain, and
  the checked-in Base Sepolia evidence validator and its negative mutations use
  `jq`; a runner without either dependency is not a conforming FlowOps runner.
- Go dependency caching is disabled for the disposable runner. A 2026-08-11 PR
  run installed Go successfully but then stalled in the Actions cache restore;
  an ephemeral one-job filesystem provides no local-cache reuse worth making
  that external cache service part of the merge gate.
- The workflow has `contents: read`, checkout credential persistence is
  disabled, and no production, wallet, RPC, facilitator, or application secret
  is exposed to CI.
- Fork pull requests are refused by the job-level repository-origin condition.
- Runner registration tokens are obtained at runtime, never printed, written to
  the repository, or committed. GitHub expires them after one hour.

## Gate

The active hosted job runs formatting, vet, race-enabled Go tests, Solidity
checks, readiness mutations, and dashboard checks on `ubuntu-24.04-arm`. A
green check is required before merge. If this fallback is reactivated, its
self-hosted job must run the same workflow steps. The
module's independent clean-clone, Linux-container, and relevant live read-only
smoke tests remain separate evidence and are not replaced by this runner.

## Recovery

Reactivating this fallback requires an isolated CI pull request that restores
the custom runner labels, verifies the ephemeral registration and teardown
procedure, and passes the complete `go` job before merge. Do not silently move
wallet, RPC, facilitator, or application secrets onto the fallback runner.
