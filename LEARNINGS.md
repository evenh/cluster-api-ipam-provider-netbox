# LEARNINGS.md

## Durable Learnings

### Repository workflow
- `LEARNINGS.md` must be consulted before each meaningful step.
- Only durable, reusable findings should be persisted here.

### Project decisions
- The provider-specific pool APIs use `ipam.cluster.x-k8s.io/v1alpha1`.
- Cluster API integration should target the latest Cluster API IPAM contract in use by the project.

### Confirmed environment facts
- The repository started effectively empty and must be bootstrapped from scratch.
- Local tooling already available includes `go`, `kubebuilder`, `controller-gen`, and `kustomize`.
- Commands that compile or inspect Go packages must use a repo-local `GOCACHE`, because the default macOS Go build cache path is not writable in the sandbox.
- `kubebuilder` successfully writes scaffold files before failing on dependency resolution, so it can still be used as a file generator if dependency updates are handled separately.
- `controller-gen paths=./...` will also traverse temporary nested scaffold modules under the repo root and can fail on their unrelated module state; generation commands should target the real package trees or those temp dirs should be removed intentionally.
- `sigs.k8s.io/cluster-api-ipam-provider-in-cluster v1.0.3` is not compatible with CAPI `v1.12.3`; it still imports removed `cluster-api` `v1beta1` packages.
- `go-netbox/v4` paginated list responses use concrete `int32` `Count` fields, not pointer counts.
- `go-netbox/v4` models tenant and VRF request fields as one-of wrappers and need `Int32AsASNRangeRequestTenant` / `Int32AsIPAddressRequestVrf` helpers for simple ID assignment.
- In this sandbox, envtest cannot bind a local control-plane port (`listen tcp 127.0.0.1:0: bind: operation not permitted`), so controller and envtest suites require elevated execution to run here.
