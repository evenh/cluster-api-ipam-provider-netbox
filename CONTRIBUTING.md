# Contributing

## Before You Start

- Read [AGENTS.md](AGENTS.md).
- Read [LEARNINGS.md](LEARNINGS.md) before making meaningful changes.
- Persist durable, reusable findings back to `LEARNINGS.md`.

## Repository Layout

- [api](api): provider API types and CRD generation inputs
- [cmd/main.go](cmd/main.go): manager entrypoint
- [internal/controller](internal/controller): pool reconcilers and NetBox-specific claim handling
- [internal/netbox](internal/netbox): repo-owned NetBox client and metadata logic
- [pkg/ipamutil](pkg/ipamutil): generic Cluster API IPAM claim reconciliation
- [pkg/reconcileutil](pkg/reconcileutil): shared controller plumbing
- [test/e2e](test/e2e): live e2e harness and Chainsaw scenarios

## Local Requirements

- Go `1.26.4`
- Docker
- `kind`
- `kubectl`
- `golangci-lint`

## Development Workflow

1. Generate code and manifests when API or RBAC changes:

```bash
make generate
make manifests
```

2. Run lint:

```bash
make lint
```

3. Run unit, integration, and envtest-backed suites:

```bash
make test
```

4. Run the live e2e suite when changing reconciliation, NetBox integration, manifests, or lifecycle behavior:

```bash
make test-e2e
```

To iterate on the e2e suite without re-provisioning NetBox and kind on every run, use `make e2e-up` once, then `make e2e-test-reuse` repeatedly, then `make e2e-down` when done.

## E2E Notes

- The e2e suite creates and uses a hardcoded kind context: `kind-netbox-ipam-e2e`.
- NetBox is started with testcontainers together with Postgres and Valkey.
- The e2e suite is intentionally slow because it bootstraps a full live NetBox stack and a management cluster; use `make e2e-up`/`make e2e-test-reuse` (see above) when iterating.
- Chainsaw runs as a `go.mod` tool dependency (`go tool chainsaw`), not a separately downloaded binary.

## Coding Expectations

- Prefer idiomatic controller-runtime patterns.
- Keep provider-specific APIs in `ipam.cluster.x-k8s.io/v1alpha1`.
- Use the repo-owned client in `internal/netbox`.
- Use `go.yaml.in/yaml/v4` for direct YAML parsing.
- Use `mgr.GetEventRecorder(...)` and `k8s.io/client-go/tools/events.EventRecorder` for controller events.
- Keep changes DRY. Shared controller plumbing belongs in `pkg/reconcileutil.ControllerBase` instead of duplicated reconciler-local code.

## Commits

- Use logical, focused commits.
- Use conventional commit messages.
- Do not push directly from automation unless explicitly requested.
