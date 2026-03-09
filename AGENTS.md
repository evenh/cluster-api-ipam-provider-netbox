# AGENTS.md

## Purpose
- This repository implements a Cluster API IPAM provider for NetBox.
- The codebase should track modern Go, modern controller-runtime patterns, and the latest supported Cluster API IPAM contract.

## Workflow Rules
- Consult `LEARNINGS.md` before each meaningful step: exploration, design changes, code edits, test runs, or release-related work.
- Persist durable findings to `LEARNINGS.md` as soon as they are confirmed.
- Record only material learnings: reusable facts, validated pitfalls, environment constraints, compatibility notes, or decisions that affect future work.
- Do not turn `LEARNINGS.md` into a task journal or progress log.

## Engineering Standards
- Use idiomatic Go and controller-runtime patterns.
- Prefer explicit, typed APIs over stringly typed configuration.
- Keep provider-specific APIs in `ipam.cluster.x-k8s.io/v1alpha1` unless intentionally versioned forward.
- Target Cluster API IPAM `v1beta2` for `IPAddress` and `IPAddressClaim` integration.
- Default to strict TLS; allow opt-in insecure NetBox access only where explicitly configured.
- Favor deterministic ownership and cleanup semantics for NetBox resources.

## Testing Standards
- Every functional change requires tests.
- Prefer unit tests for pure logic, `envtest` for controller/webhook behavior, and declarative Chainsaw scenarios for end-to-end behavior.
- End-to-end tests should use real NetBox dependencies via testcontainers or equivalent ephemeral infrastructure.
- Validate both IPv4 and IPv6 behavior where the feature is expected to support dual stack.

## Delivery Standards
- Keep install artifacts compatible with Cluster API provider conventions, including Kustomize-based manifests and clusterctl metadata.
- Keep dependency updates intentional and pinned.
- Document compatibility assumptions in `LEARNINGS.md` when discovered or changed.
