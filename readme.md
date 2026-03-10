# Cluster API IPAM Provider for NetBox

`cluster-api-ipam-provider-netbox` connects Cluster API IP address claims to NetBox-backed address allocation.

It is intended for environments where NetBox is the source of truth for prefixes and allocated addresses, while Cluster API remains the source of truth for machine lifecycle.

## What It Does

- allocates addresses for Cluster API `IPAddressClaim` objects
- creates matching Cluster API `IPAddress` objects
- releases addresses in NetBox when claims are deleted
- supports both namespaced and cluster-scoped pools
- resolves NetBox prefixes by ID or by CIDR
- supports IPv4 and IPv6 allocation flows

The provider-specific APIs are published under `ipam.cluster.x-k8s.io/v1alpha1`.

## Pool Types

- `NetBoxIPPool`: namespaced pool configuration
- `GlobalNetBoxIPPool`: cluster-scoped pool configuration

Both pool types allow you to point claims at one or more NetBox prefixes and provide shared metadata defaults for allocated addresses.

## Installation Artifacts

Build the install manifests with:

```bash
make build-installer
```

Generated artifacts:

- [ipam-components.yaml](/Users/evenholthe/projects/evenh/cluster-api-ipam-provider-netbox/dist/ipam-components.yaml)
- [metadata.yaml](/Users/evenholthe/projects/evenh/cluster-api-ipam-provider-netbox/dist/metadata.yaml)

## Project Status

The repository includes:

- unit and envtest-backed controller tests
- live e2e tests using NetBox, Postgres, Valkey, kind, and Chainsaw
- a repo-owned NetBox client rather than generated client bindings

## Contributing

For local setup, test commands, development workflow, and repository conventions, see [CONTRIBUTING.md](/Users/evenholthe/projects/evenh/cluster-api-ipam-provider-netbox/CONTRIBUTING.md).
