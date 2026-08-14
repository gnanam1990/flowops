# Ephemeral CI Runner

Status: active fallback while GitHub-hosted Actions is blocked by the repository
owner's billing/spending hold.

## Security posture

- The `go` job targets a repository-scoped, Linux ARM64 runner carrying the
  custom `flowops-ephemeral` label.
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

The self-hosted job runs the same formatting, vet, and race-enabled test steps
as the previous GitHub-hosted job. A green check is required before merge. The
module's independent clean-clone, Linux-container, and relevant live read-only
smoke tests remain separate evidence and are not replaced by this runner.

## Recovery

When GitHub-hosted Actions billing is restored, change `runs-on` back to
`ubuntu-latest` in an isolated CI pull request and require the hosted `go` check
to pass before merging that migration.
