# LEARNINGS.md

## Durable Learnings

### Repository workflow
- `LEARNINGS.md` must be consulted before each meaningful step.
- Only durable, reusable findings should be persisted here.

### Project decisions
- The provider-specific pool APIs use `ipam.cluster.x-k8s.io/v1alpha1`.
- Cluster API integration should target the latest Cluster API IPAM contract in use by the project.
- The repository target remains Go `1.26`; Go `1.25.x` is only a temporary local toolchain override for Chainsaw-related tasks and should not be committed as the repo baseline.
- NetBox integration is implemented with repo-owned JSON clients; do not reintroduce `go-netbox` unless there is a concrete maintenance reason to accept generated-client coupling again.
- Direct YAML imports in this repo should use `go.yaml.in/yaml/v4`; do not add new direct uses of the deprecated `gopkg.in/yaml.v3` module.
- Shared controller plumbing in this repo should go through `pkg/reconcileutil.ControllerBase`; avoid duplicating `Client`/`Scheme`/`Recorder` fields or recorder helper methods per reconciler.
- Pool CRDs expose an optional `spec.clusterName`; the pool reconciler mirrors it into the standard `cluster.x-k8s.io/cluster-name` label so pools can participate in `clusterctl move`.

### Confirmed environment facts
- The repository started effectively empty and must be bootstrapped from scratch.
- Local tooling already available includes `go`, `kubebuilder`, `controller-gen`, and `kustomize`.
- Commands that compile or inspect Go packages must use a repo-local `GOCACHE`, because the default macOS Go build cache path is not writable in the sandbox.
- `kubebuilder` successfully writes scaffold files before failing on dependency resolution, so it can still be used as a file generator if dependency updates are handled separately.
- `controller-gen paths=./...` will also traverse temporary nested scaffold modules under the repo root and can fail on their unrelated module state; generation commands should target the real package trees or those temp dirs should be removed intentionally.
- The repository pins `controller-gen` to `v0.20.1`; generated CRDs and RBAC should be emitted with `bin/controller-gen`, not an older globally installed binary.
- Upgrading this repo from controller-runtime `v0.22.x` to `v0.23.x` does not require source changes for the `v0.23.0` release-note breakages, because the project does not currently use controller-runtime event recorders or webhook validator/defaulting interfaces.
- On controller-runtime `v0.23.x`, manager-scoped event recording should use `mgr.GetEventRecorder(...)` and `k8s.io/client-go/tools/events.EventRecorder`; `GetEventRecorderFor` is deprecated.
- `sigs.k8s.io/cluster-api-ipam-provider-in-cluster v1.0.3` is not compatible with CAPI `v1.12.3`; it still imports removed `cluster-api` `v1beta1` packages.
- All NetBox HTTP calls in this repo should set the shared custom `User-Agent` string from `internal/netbox.UserAgent`.
- In this sandbox, envtest cannot bind a local control-plane port (`listen tcp 127.0.0.1:0: bind: operation not permitted`), so controller and envtest suites require elevated execution to run here.
- Cluster API provider repositories still use `metadata.yaml` with `apiVersion: clusterctl.cluster.x-k8s.io/v1alpha3`; the release series should advertise the provider contract separately (`v1beta2` here).
- NetBox `v4.3-3.3.0` serves `/api/` as unauthenticated `403 Forbidden`; the e2e readiness check should treat that as healthy rather than waiting for `200`.
- NetBox bootstrap API tokens created via `SUPERUSER_API_TOKEN` must fit the backing `varchar(40)` limit.
- NetBox rejects unknown IP address custom field names in allocation payloads; any custom fields referenced by pool metadata or claim annotations must already exist in NetBox, so the e2e harness must seed them explicitly.
- Chainsaw `v0.2.14` configuration requires `metadata.name`; `skip delete` behavior is controlled by the CLI `--skip-delete` flag rather than `spec.cleanup`.
- The e2e harness runs successfully with the repo’s Go `1.26` baseline as long as `chainsaw` is already installed; the temporary Go `1.25.x` workaround is only needed for Chainsaw-specific tooling paths, not for executing the provider’s e2e suite.
