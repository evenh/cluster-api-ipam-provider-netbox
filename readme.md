# Cluster API IPAM Provider for NetBox

`cluster-api-ipam-provider-netbox` is a Cluster API IPAM provider that allocates and releases IP addresses from NetBox.

## Status

The provider currently supports:

- namespaced `NetBoxIPPool`
- cluster-scoped `GlobalNetBoxIPPool`
- NetBox prefix lookup by ID or CIDR
- claim allocation and release through Cluster API `IPAddressClaim` and `IPAddress`
- dual-stack capable NetBox-backed address allocation
- live e2e verification with NetBox, Postgres, Valkey, kind, and Chainsaw

The provider-specific CRDs are published under `ipam.cluster.x-k8s.io/v1alpha1`.

## Repository Layout

- [api](/Users/evenholthe/projects/evenh/cluster-api-ipam-provider-netbox/api): provider API types and CRD generation inputs
- [cmd/main.go](/Users/evenholthe/projects/evenh/cluster-api-ipam-provider-netbox/cmd/main.go): manager entrypoint
- [internal/controller](/Users/evenholthe/projects/evenh/cluster-api-ipam-provider-netbox/internal/controller): pool reconcilers and NetBox claim adapter
- [internal/netbox](/Users/evenholthe/projects/evenh/cluster-api-ipam-provider-netbox/internal/netbox): repo-owned NetBox client
- [pkg/ipamutil](/Users/evenholthe/projects/evenh/cluster-api-ipam-provider-netbox/pkg/ipamutil): generic Cluster API IPAM claim reconciliation logic
- [test/e2e](/Users/evenholthe/projects/evenh/cluster-api-ipam-provider-netbox/test/e2e): testcontainers + Chainsaw e2e suite

## Prerequisites

- Go `1.26`
- Docker
- `kind`
- `kubectl`
- `golangci-lint`

The e2e suite uses a hardcoded kind context name: `kind-netbox-ipam-e2e`.

## Common Commands

```bash
make manifests
make generate
make test
go test -tags=e2e ./test/e2e -count=1 -v
golangci-lint run
```

## Development Notes

- Consult [LEARNINGS.md](/Users/evenholthe/projects/evenh/cluster-api-ipam-provider-netbox/LEARNINGS.md) before making meaningful changes.
- Use the repo-owned NetBox client in `internal/netbox`; do not add generated NetBox clients back in without a concrete reason.
- Direct YAML imports should use `go.yaml.in/yaml/v4`.
- Controller event recording should use `mgr.GetEventRecorder(...)` and `k8s.io/client-go/tools/events.EventRecorder`.

## Installation

Generate install artifacts:

```bash
make build-installer
```

The generated provider manifests are written to:

- [dist/ipam-components.yaml](/Users/evenholthe/projects/evenh/cluster-api-ipam-provider-netbox/dist/ipam-components.yaml)
- [dist/metadata.yaml](/Users/evenholthe/projects/evenh/cluster-api-ipam-provider-netbox/dist/metadata.yaml)

## Contributing

See [CONTRIBUTING.md](/Users/evenholthe/projects/evenh/cluster-api-ipam-provider-netbox/CONTRIBUTING.md).
