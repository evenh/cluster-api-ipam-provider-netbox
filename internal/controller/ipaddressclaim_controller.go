/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ipamv1 "sigs.k8s.io/cluster-api/api/ipam/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	ipamv1alpha1 "github.com/evenh/cluster-api-ipam-provider-netbox/api/v1alpha1"
	nb "github.com/evenh/cluster-api-ipam-provider-netbox/internal/netbox"
	ipamutil "github.com/evenh/cluster-api-ipam-provider-netbox/pkg/ipamutil"
	ipampredicates "github.com/evenh/cluster-api-ipam-provider-netbox/pkg/predicates"
)

const poolExhaustedRequeueAfter = 15 * time.Second
const missingAddressRequeueAfter = 5 * time.Second

type NetBoxProviderAdapter struct {
	NewClient      func(nb.ConnectionConfig) (nb.Client, error)
	RequestTimeout time.Duration
}

type netboxClaimHandler struct {
	client.Client

	claim          *ipamv1.IPAddressClaim
	pool           statusPool
	newClientFunc  func(nb.ConnectionConfig) (nb.Client, error)
	requestTimeout time.Duration
}

var _ ipamutil.ProviderAdapter = &NetBoxProviderAdapter{}
var _ ipamutil.ClaimHandler = &netboxClaimHandler{}

// +kubebuilder:rbac:groups=ipam.cluster.x-k8s.io,resources=ipaddressclaims,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=ipam.cluster.x-k8s.io,resources=ipaddressclaims/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ipam.cluster.x-k8s.io,resources=ipaddressclaims/finalizers,verbs=update
// +kubebuilder:rbac:groups=ipam.cluster.x-k8s.io,resources=ipaddresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ipam.cluster.x-k8s.io,resources=ipaddresses/finalizers,verbs=update
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

func (a *NetBoxProviderAdapter) SetupWithManager(_ context.Context, b *ctrl.Builder) error {
	b.For(&ipamv1.IPAddressClaim{}, builder.WithPredicates(
		predicate.Or(
			ipampredicates.ClaimReferencesPoolKind(ipamv1alpha1.GroupKindNetBoxIPPool),
			ipampredicates.ClaimReferencesPoolKind(ipamv1alpha1.GroupKindGlobalNetBoxIPPool),
		),
	)).
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Owns(&ipamv1.IPAddress{}, builder.WithPredicates(
			predicate.Or(
				ipampredicates.AddressReferencesPoolKind(ipamv1alpha1.GroupKindNetBoxIPPool),
				ipampredicates.AddressReferencesPoolKind(ipamv1alpha1.GroupKindGlobalNetBoxIPPool),
			),
		))
	return nil
}

func (a *NetBoxProviderAdapter) ClaimHandlerFor(c client.Client, claim *ipamv1.IPAddressClaim) ipamutil.ClaimHandler {
	newClientFunc := a.NewClient
	if newClientFunc == nil {
		newClientFunc = nb.NewClient
	}
	return &netboxClaimHandler{
		Client:         c,
		claim:          claim,
		newClientFunc:  newClientFunc,
		requestTimeout: a.RequestTimeout,
	}
}

func (h *netboxClaimHandler) FetchPool(ctx context.Context) (client.Object, *ctrl.Result, error) {
	switch h.claim.Spec.PoolRef.Kind {
	case ipamv1alpha1.NetBoxIPPoolKind:
		pool := &ipamv1alpha1.NetBoxIPPool{}
		if err := h.Get(
			ctx,
			types.NamespacedName{Namespace: h.claim.Namespace, Name: h.claim.Spec.PoolRef.Name},
			pool,
		); err != nil {
			return nil, nil, err
		}
		h.pool = pool
	case ipamv1alpha1.GlobalNetBoxIPPoolKind:
		pool := &ipamv1alpha1.GlobalNetBoxIPPool{}
		if err := h.Get(ctx, types.NamespacedName{Name: h.claim.Spec.PoolRef.Name}, pool); err != nil {
			return nil, nil, err
		}
		h.pool = pool
	default:
		return nil, nil, fmt.Errorf("unsupported pool kind %q", h.claim.Spec.PoolRef.Kind)
	}
	return h.pool, nil, nil
}

func (h *netboxClaimHandler) EnsureAddress(
	ctx context.Context,
	address *ipamv1.IPAddress,
) (_ *ctrl.Result, err error) {
	if address.Spec.Address != "" {
		return nil, nil
	}

	logger := ctrl.LoggerFrom(ctx)
	defer func() {
		if err != nil {
			logger.Error(err, "ensure NetBox address failed")
			err = nb.SanitizedError(err)
		}
	}()

	poolSpec := h.pool.PoolSpec()
	cfg, err := nb.LoadConnectionConfig(ctx, h.Client, h.pool.GetNamespace(), poolSpec.ConnectionSecretRef)
	if err != nil {
		return nil, err
	}
	cfg.RequestTimeout = h.requestTimeout
	netboxClient, err := h.newClientFunc(cfg)
	if err != nil {
		return nil, err
	}

	ownershipTag := nb.OwnershipTag(poolSpec)
	claimUIDField := nb.ClaimUIDCustomField(poolSpec)

	// Idempotency: a previous reconcile may have allocated a NetBox IP for this claim
	// and then crashed or failed before that allocation could be persisted onto the
	// IPAddress object (address.Spec.Address would still read "" here in that case).
	// Reuse the existing NetBox record instead of allocating a second, orphaned one.
	existing, err := netboxClient.FindIPAddressByClaimUID(ctx, ownershipTag, claimUIDField, string(h.claim.GetUID()))
	if err != nil {
		return nil, err
	}
	if existing != nil {
		address.Spec.Address = existing.Address
		prefix := existing.Prefix
		address.Spec.Prefix = &prefix
		return nil, nil
	}

	if customFieldErr := netboxClient.EnsureIPAddressCustomField(ctx, claimUIDField); customFieldErr != nil {
		return nil, customFieldErr
	}

	// Prefer the pool's cached prefix resolution (kept warm by the pool reconciler) over
	// resolving CIDRs against NetBox on every claim reconcile. Fall back to a live
	// resolution when the cache isn't warm yet (e.g. a brand new pool).
	prefixIDs := h.pool.PoolStatus().ResolvedPrefixes
	if len(prefixIDs) == 0 {
		prefixIDs, err = netboxClient.ResolvePrefixIDs(ctx, poolSpec.Prefixes)
		if err != nil {
			return nil, err
		}
	}

	metadata, err := nb.EffectivePoolMetadata(poolSpec.MetadataDefaults, h.claim)
	if err != nil {
		return nil, err
	}

	request := nb.AllocationRequest{
		Metadata:          metadata,
		OwnershipTag:      ownershipTag,
		ClaimUIDFieldName: claimUIDField,
		ClaimUID:          string(h.claim.GetUID()),
		Description:       fmt.Sprintf("%s/%s", h.claim.Namespace, h.claim.Name),
		Status:            nb.IPAddressStatus(poolSpec),
	}

	for _, prefixID := range prefixIDs {
		allocation, allocationErr := netboxClient.AllocateIPAddress(ctx, prefixID, request)
		if allocationErr != nil {
			if errors.Is(allocationErr, nb.ErrNoAvailableIP) {
				continue
			}
			return nil, allocationErr
		}
		address.Spec.Address = allocation.Address
		prefix := allocation.Prefix
		address.Spec.Prefix = &prefix
		return nil, nil
	}

	return &ctrl.Result{RequeueAfter: poolExhaustedRequeueAfter}, errors.New("pool exhausted")
}

func (h *netboxClaimHandler) ReleaseAddress(ctx context.Context) (_ *ctrl.Result, err error) {
	if h.pool == nil {
		return nil, nil
	}

	logger := ctrl.LoggerFrom(ctx)
	defer func() {
		if err != nil {
			logger.Error(err, "release NetBox address failed")
			err = nb.SanitizedError(err)
		}
	}()

	k8sIPAddress := &ipamv1.IPAddress{}
	key := types.NamespacedName{Namespace: h.claim.Namespace, Name: h.claim.Name}
	if getErr := h.Get(ctx, key, k8sIPAddress); getErr != nil && !apierrors.IsNotFound(getErr) {
		return nil, getErr
	}

	poolSpec := h.pool.PoolSpec()
	cfg, err := nb.LoadConnectionConfig(ctx, h.Client, h.pool.GetNamespace(), poolSpec.ConnectionSecretRef)
	if err != nil {
		return nil, err
	}
	cfg.RequestTimeout = h.requestTimeout
	netboxClient, err := h.newClientFunc(cfg)
	if err != nil {
		return nil, err
	}

	ipAddress, err := h.findExistingNetBoxIP(ctx, netboxClient, poolSpec, k8sIPAddress)
	if err != nil {
		return nil, err
	}
	if ipAddress == nil {
		if k8sIPAddress.Name != "" {
			return &ctrl.Result{RequeueAfter: missingAddressRequeueAfter}, fmt.Errorf(
				"unable to locate NetBox IP for claim %s/%s with uid %s and address candidates %v",
				h.claim.Namespace,
				h.claim.Name,
				h.claim.GetUID(),
				netboxAddressCandidates(k8sIPAddress),
			)
		}
		return nil, nil
	}
	return nil, netboxClient.DeleteIPAddress(ctx, ipAddress.ID)
}

// findExistingNetBoxIP looks up the NetBox IP address owned by this claim, first by the
// claim UID custom field and, if that misses (e.g. the field was added after the address
// was allocated), by matching the address recorded on the Kubernetes IPAddress object.
func (h *netboxClaimHandler) findExistingNetBoxIP(
	ctx context.Context,
	netboxClient nb.Client,
	poolSpec *ipamv1alpha1.NetBoxIPPoolSpec,
	k8sIPAddress *ipamv1.IPAddress,
) (*nb.AllocatedAddress, error) {
	ipAddress, err := netboxClient.FindIPAddressByClaimUID(
		ctx,
		nb.OwnershipTag(poolSpec),
		nb.ClaimUIDCustomField(poolSpec),
		string(h.claim.GetUID()),
	)
	if err != nil || ipAddress != nil || k8sIPAddress.Name == "" {
		return ipAddress, err
	}

	for _, candidate := range netboxAddressCandidates(k8sIPAddress) {
		ipAddress, err = netboxClient.FindIPAddressByAddress(ctx, nb.OwnershipTag(poolSpec), candidate)
		if err != nil {
			return nil, err
		}
		if ipAddress != nil {
			return ipAddress, nil
		}
	}
	return nil, nil
}

func netboxAddressCandidates(address *ipamv1.IPAddress) []string {
	if address == nil || address.Spec.Address == "" {
		return nil
	}

	candidates := []string{}
	if address.Spec.Prefix != nil {
		candidates = append(candidates, fmt.Sprintf("%s/%d", address.Spec.Address, *address.Spec.Prefix))
	}
	candidates = append(candidates, address.Spec.Address)
	return candidates
}
