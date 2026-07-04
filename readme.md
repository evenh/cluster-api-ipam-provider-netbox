# Cluster API IPAM Provider for NetBox

`cluster-api-ipam-provider-netbox` connects Cluster API IP address claims to NetBox-backed address allocation.

It is intended for environments where NetBox is the source of truth for prefixes and allocated addresses, while Cluster API remains the source of truth for machine lifecycle.

## What It Does

The provider is an allocator, not an importer: it never discovers pre-existing NetBox IP addresses and represents them in Kubernetes.

- allocates addresses for Cluster API `IPAddressClaim` objects and creates the matching Cluster API `IPAddress` object
- creates a NetBox IP address record for every accepted claim, and deletes it when the claim is deleted — this is the provider's actual write path against NetBox
- creates missing NetBox tags on demand (including the ownership tag used to find and clean up its own addresses later)
- resolves NetBox prefixes by ID or by CIDR, but never creates them — the prefixes must already exist in NetBox
- checks for (but does not create) the NetBox custom field used to track the owning `IPAddressClaim`'s UID
- optionally populates `IPAddress.spec.gateway` so consumers read the gateway from the pool instead of hardcoding it — resolved per prefix from a NetBox custom field, with static pool/per-prefix fallbacks
- supports both namespaced and cluster-scoped pools
- supports IPv4 and IPv6 allocation flows
- sets the Cluster API IPAM `Ready` condition on claims (`AllocationFailed`/`PoolNotReady`/`PoolExhausted` on failure), per the [IPAM provider contract](https://cluster-api.sigs.k8s.io/developer/providers/contracts/ipam)

The provider-specific APIs are published under `ipam.cluster.x-k8s.io/v1alpha1`.

## Before You Start: NetBox Prerequisites

For each prefix a pool will allocate from:

1. The prefix itself must already exist in NetBox.
2. A text custom field must exist on the IPAM > IP Address object type to track the claim UID (`cluster_api_claim_uid` by default, configurable per pool via `spec.claimUIDCustomField`).

The provider creates tags and IP address records as needed, but will not create prefixes or the custom field definition — pool reconciliation fails until both exist.

### Optional: gateway resolution

To have the provider set `IPAddress.spec.gateway` (so consumers such as CAPV read the gateway from the pool rather than hardcoding a static `gateway4`), configure a gateway source. Resolution is per prefix, in priority order:

1. **NetBox prefix custom field** — a text custom field on the IPAM > **Prefix** object type, named by `spec.gatewayCustomField` (default `gateway`). This keeps NetBox the source of truth. Unlike the claim UID field it is **optional**: a prefix with no value simply falls through to the static fallbacks, so pool reconciliation does not fail if it is absent. Set `spec.gatewayCustomField: ""` to disable NetBox gateway lookups entirely.
2. **Per-prefix static** — `spec.prefixes[].gateway`.
3. **Pool-level static** — `spec.gateway`, applied to allocations whose address family matches.

Leave all three unset to omit `spec.gateway` (the default; pre-gateway behaviour is preserved). A resolved gateway must be a valid IP of the same family as the prefix, or the pool goes `Ready=False` (`PrefixResolutionFailed`); a gateway that falls inside the prefix's allocatable range is allowed but raises a warning event, since NetBox could otherwise hand it out as a normal address unless it is reserved.

## Pool Types

- `NetBoxIPPool`: namespaced. Only `IPAddressClaim`s in the same namespace may reference it. `spec.connectionSecretRef.namespace` must be left empty — the pool's own namespace is always used, so a namespaced pool cannot read a Secret from another namespace.
- `GlobalNetBoxIPPool`: cluster-scoped. `IPAddressClaim`s in any namespace may reference it. Because it has no owning namespace, `spec.connectionSecretRef.namespace` must be set explicitly.

Both pool types point claims at one or more NetBox prefixes and provide shared metadata defaults (DNS name, tags, tenant, VRF, custom fields) for allocated addresses. A claim can override those defaults for itself using `ipam.netbox.cluster.x-k8s.io/*` annotations — see the samples below.

See [config/samples](config/samples) for worked examples of both pool types, the connection Secret they expect, and an `IPAddressClaim` demonstrating the annotation overrides.

## Container Images

Images are published to `ghcr.io/evenh/cluster-api-ipam-provider-netbox`:

- `latest` and `sha-<commit>`: built from every push to `main`
- `<version>` and `<major>.<minor>` (e.g. `0.1.0`, `0.1`): built from `v*` tags

## Installation

Each [GitHub Release](https://github.com/evenh/cluster-api-ipam-provider-netbox/releases) ships `ipam-components.yaml` and `metadata.yaml` for the corresponding image tag — apply `ipam-components.yaml` directly, or use it with `clusterctl`.

To build install manifests for a different image (e.g. a `main` build, or a local one):

```bash
make build-installer IMG=ghcr.io/evenh/cluster-api-ipam-provider-netbox:latest
```

This generates `dist/ipam-components.yaml` and `dist/metadata.yaml`.

## Project Status

The repository includes:

- unit and envtest-backed controller tests
- live e2e tests using NetBox, Postgres, Valkey, kind, and Chainsaw (`make test-e2e`)
- a `make e2e-up` / `make e2e-test-reuse` / `make e2e-down` workflow for iterating against a standing NetBox+kind environment instead of provisioning one on every run
- a repo-owned NetBox client rather than generated client bindings

## Contributing

For local setup, test commands, development workflow, and repository conventions, see [CONTRIBUTING.md](CONTRIBUTING.md).
