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
	"k8s.io/utils/ptr"
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

type claimPool interface {
	client.Object
	PoolSpec() *ipamv1alpha1.NetBoxIPPoolSpec
}

type NetBoxProviderAdapter struct {
	NewClient func(nb.ConnectionConfig) (nb.Client, error)
}

type netboxClaimHandler struct {
	client.Client
	claim         *ipamv1.IPAddressClaim
	pool          claimPool
	newClientFunc func(nb.ConnectionConfig) (nb.Client, error)
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
		Client:        c,
		claim:         claim,
		newClientFunc: newClientFunc,
	}
}

func (h *netboxClaimHandler) FetchPool(ctx context.Context) (client.Object, *ctrl.Result, error) {
	switch h.claim.Spec.PoolRef.Kind {
	case ipamv1alpha1.NetBoxIPPoolKind:
		pool := &ipamv1alpha1.NetBoxIPPool{}
		if err := h.Get(ctx, types.NamespacedName{Namespace: h.claim.Namespace, Name: h.claim.Spec.PoolRef.Name}, pool); err != nil {
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

func (h *netboxClaimHandler) EnsureAddress(ctx context.Context, address *ipamv1.IPAddress) (*ctrl.Result, error) {
	if address.Spec.Address != "" {
		return nil, nil
	}

	poolSpec := h.pool.PoolSpec()
	cfg, err := nb.LoadConnectionConfig(ctx, h.Client, h.pool.GetNamespace(), poolSpec.ConnectionSecretRef)
	if err != nil {
		return nil, err
	}
	netboxClient, err := h.newClientFunc(cfg)
	if err != nil {
		return nil, err
	}

	claimUIDField := nb.ClaimUIDCustomField(poolSpec)
	if err := netboxClient.EnsureIPAddressCustomField(ctx, claimUIDField); err != nil {
		return nil, err
	}

	prefixIDs, err := netboxClient.ResolvePrefixIDs(ctx, poolSpec.Prefixes)
	if err != nil {
		return nil, err
	}

	metadata, err := nb.EffectivePoolMetadata(poolSpec.MetadataDefaults, h.claim)
	if err != nil {
		return nil, err
	}

	request := nb.AllocationRequest{
		Metadata:          metadata,
		OwnershipTag:      nb.OwnershipTag(poolSpec),
		ClaimUIDFieldName: claimUIDField,
		ClaimUID:          string(h.claim.GetUID()),
		Description:       fmt.Sprintf("%s/%s", h.claim.Namespace, h.claim.Name),
		Status:            nb.IPAddressStatus(poolSpec),
	}

	for _, prefixID := range prefixIDs {
		allocation, err := netboxClient.AllocateIPAddress(ctx, prefixID, request)
		if err != nil {
			if errors.Is(err, nb.ErrNoAvailableIP) {
				continue
			}
			return nil, err
		}
		address.Spec.Address = allocation.Address
		address.Spec.Prefix = ptr.To(allocation.Prefix)
		return nil, nil
	}

	return &ctrl.Result{RequeueAfter: poolExhaustedRequeueAfter}, fmt.Errorf("pool exhausted")
}

func (h *netboxClaimHandler) ReleaseAddress(ctx context.Context) (*ctrl.Result, error) {
	if h.pool == nil {
		return nil, nil
	}

	k8sIPAddress := &ipamv1.IPAddress{}
	key := types.NamespacedName{Namespace: h.claim.Namespace, Name: h.claim.Name}
	if err := h.Get(ctx, key, k8sIPAddress); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, err
		}
	}

	poolSpec := h.pool.PoolSpec()
	cfg, err := nb.LoadConnectionConfig(ctx, h.Client, h.pool.GetNamespace(), poolSpec.ConnectionSecretRef)
	if err != nil {
		return nil, err
	}
	netboxClient, err := h.newClientFunc(cfg)
	if err != nil {
		return nil, err
	}

	ipAddress, err := netboxClient.FindIPAddressByClaimUID(ctx, nb.OwnershipTag(poolSpec), nb.ClaimUIDCustomField(poolSpec), string(h.claim.GetUID()))
	if err != nil {
		return nil, err
	}
	if ipAddress == nil && k8sIPAddress.Name != "" {
		for _, candidate := range netboxAddressCandidates(k8sIPAddress) {
			ipAddress, err = netboxClient.FindIPAddressByAddress(ctx, nb.OwnershipTag(poolSpec), candidate)
			if err != nil {
				return nil, err
			}
			if ipAddress != nil {
				break
			}
		}
	}
	if ipAddress == nil {
		if k8sIPAddress.Name != "" {
			return &ctrl.Result{RequeueAfter: 5 * time.Second}, fmt.Errorf(
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
