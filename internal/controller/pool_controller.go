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
	"fmt"
	"strconv"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ipamv1 "sigs.k8s.io/cluster-api/api/ipam/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	ipamv1alpha1 "github.com/evenh/cluster-api-ipam-provider-netbox/api/v1alpha1"
	"github.com/evenh/cluster-api-ipam-provider-netbox/internal/index"
	nb "github.com/evenh/cluster-api-ipam-provider-netbox/internal/netbox"
	"github.com/evenh/cluster-api-ipam-provider-netbox/pkg/reconcileutil"
)

const (
	poolFinalizer                = "ipam.cluster.x-k8s.io/pool-protection"
	reasonPoolInUse              = "PoolInUse"
	readyConditionType           = "Ready"
	reasonPoolReady              = "PoolReady"
	reasonPrefixResolutionFailed = "PrefixResolutionFailed"
	reasonDeletionBlocked        = "DeletionBlocked"
	reasonGatewayInRange         = "GatewayInAllocatableRange"
)

type statusPool interface {
	client.Object
	PoolSpec() *ipamv1alpha1.NetBoxIPPoolSpec
	PoolStatus() *ipamv1alpha1.NetBoxIPPoolStatus
}

func reconcilePoolStatus(
	ctx context.Context,
	c client.Client,
	recorder reconcileutil.EventRecorder,
	newClientFunc func(nb.ConnectionConfig) (nb.Client, error),
	requestTimeout time.Duration,
	pool statusPool,
	kind string,
) error {
	addresses, err := listAddressesInUse(ctx, c, pool.GetNamespace(), kind, pool.GetName())
	if err != nil {
		return err
	}

	status := pool.PoolStatus()
	previousObservedGeneration := status.ObservedGeneration
	status.ObservedGeneration = pool.GetGeneration()
	allocated, err := safeAllocatedCount(len(addresses))
	if err != nil {
		return err
	}
	status.Addresses = &ipamv1alpha1.NetBoxPoolStatusAddresses{Allocated: allocated}

	// Only re-resolve prefixes against NetBox when the pool spec has actually changed
	// since the last successful reconcile. Owned IPAddress churn (every claim allocation
	// or release) also triggers this reconciler, and re-resolving on every one of those
	// events would just move the N+1 NetBox call problem here from the claim controller.
	var resolveErr error
	if len(status.ResolvedPrefixes) == 0 || previousObservedGeneration != pool.GetGeneration() {
		resolveErr = resolvePoolPrefixes(ctx, c, recorder, newClientFunc, requestTimeout, pool)
	}
	setPoolReadyCondition(pool, resolveErr)
	ensureClusterNameLabel(pool, pool.PoolSpec().ClusterName)

	if pool.GetDeletionTimestamp().IsZero() {
		controllerutil.AddFinalizer(pool, poolFinalizer)
		return resolveErr
	}
	if len(addresses) > 0 {
		if recorder != nil {
			recorder.RecordWarning(
				pool,
				reasonPoolInUse,
				"BlockPoolDeletion",
				fmt.Sprintf("Pool deletion is blocked while %d IPAddresses are still allocated", len(addresses)),
			)
		}
		blockedErr := fmt.Errorf("pool still has %d allocated IPAddresses", len(addresses))
		setConditionFalse(pool, reasonDeletionBlocked, blockedErr.Error())
		return blockedErr
	}
	controllerutil.RemoveFinalizer(pool, poolFinalizer)
	return nil
}

// resolvePoolPrefixes resolves the pool's configured prefixes against NetBox and caches
// the result — prefix IDs plus each prefix's CIDR and resolved gateway — in the pool status, so
// claim reconciles can reuse it instead of resolving the same, spec-invariant prefixes and gateways
// on every single allocation attempt.
func resolvePoolPrefixes(
	ctx context.Context,
	c client.Client,
	recorder reconcileutil.EventRecorder,
	newClientFunc func(nb.ConnectionConfig) (nb.Client, error),
	requestTimeout time.Duration,
	pool statusPool,
) error {
	logger := ctrl.LoggerFrom(ctx)
	poolSpec := pool.PoolSpec()

	cfg, err := nb.LoadConnectionConfig(ctx, c, pool.GetNamespace(), poolSpec.ConnectionSecretRef)
	if err != nil {
		logger.Error(err, "load NetBox connection config failed")
		return nb.SanitizedError(err)
	}
	cfg.RequestTimeout = requestTimeout
	netboxClient, err := newClientFunc(cfg)
	if err != nil {
		logger.Error(err, "create NetBox client failed")
		return nb.SanitizedError(err)
	}
	prefixIDs, err := netboxClient.ResolvePrefixIDs(ctx, poolSpec.Prefixes)
	if err != nil {
		logger.Error(err, "resolve NetBox prefixes failed")
		return nb.SanitizedError(err)
	}

	details, err := resolvePrefixDetails(ctx, netboxClient, recorder, pool, prefixIDs)
	if err != nil {
		return err
	}

	pool.PoolStatus().ResolvedPrefixes = prefixIDs
	pool.PoolStatus().ResolvedPrefixDetails = details
	return nil
}

// resolvePrefixDetails fetches each resolved prefix's CIDR and custom fields from NetBox and derives
// its gateway. prefixIDs is aligned index-for-index with poolSpec.Prefixes (ResolvePrefixIDs emits
// exactly one ID per configured reference, in order), so the per-prefix static gateway is read from
// the matching spec entry.
func resolvePrefixDetails(
	ctx context.Context,
	netboxClient nb.Client,
	recorder reconcileutil.EventRecorder,
	pool statusPool,
	prefixIDs []int32,
) ([]ipamv1alpha1.ResolvedPrefix, error) {
	logger := ctrl.LoggerFrom(ctx)
	poolSpec := pool.PoolSpec()

	fetched, err := netboxClient.GetPrefixDetails(ctx, prefixIDs)
	if err != nil {
		logger.Error(err, "get NetBox prefix details failed")
		return nil, nb.SanitizedError(err)
	}

	resolved, inRangeIDs, err := buildResolvedPrefixes(poolSpec, prefixIDs, fetched)
	if err != nil {
		logger.Error(err, "resolve gateway failed")
		return nil, err
	}
	if recorder != nil {
		for _, id := range inRangeIDs {
			recorder.RecordWarning(
				pool,
				reasonGatewayInRange,
				"ResolveGateway",
				fmt.Sprintf(
					"Gateway for prefix %d lies within its allocatable range; "+
						"mark it reserved in NetBox so it is not handed out as a normal address",
					id,
				),
			)
		}
	}
	return resolved, nil
}

// buildResolvedPrefixes derives the cached ResolvedPrefix entries (CIDR + gateway) for prefixIDs,
// which are aligned index-for-index with poolSpec.Prefixes (ResolvePrefixIDs emits one ID per
// configured reference, in order). It returns the resolved entries plus the IDs whose gateway falls
// inside the prefix's allocatable range (advisory only, surfaced as warnings by the pool
// reconciler). A gateway that is an invalid IP or a family mismatch is returned as an error.
func buildResolvedPrefixes(
	poolSpec *ipamv1alpha1.NetBoxIPPoolSpec,
	prefixIDs []int32,
	fetched []nb.PrefixDetail,
) ([]ipamv1alpha1.ResolvedPrefix, []int32, error) {
	detailByID := make(map[int32]nb.PrefixDetail, len(fetched))
	for _, detail := range fetched {
		detailByID[detail.ID] = detail
	}

	resolved := make([]ipamv1alpha1.ResolvedPrefix, 0, len(prefixIDs))
	var inRangeIDs []int32
	for i, id := range prefixIDs {
		var perPrefixStatic string
		if i < len(poolSpec.Prefixes) {
			perPrefixStatic = poolSpec.Prefixes[i].Gateway
		}
		detail := detailByID[id]
		customFieldValue := nb.CustomFieldString(detail.CustomFields, poolSpec.GatewayCustomField)

		gateway, inRange, err := nb.ResolveGateway(detail.CIDR, customFieldValue, perPrefixStatic, poolSpec.Gateway)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve gateway for prefix %d: %w", id, err)
		}
		if inRange {
			inRangeIDs = append(inRangeIDs, id)
		}
		resolved = append(resolved, ipamv1alpha1.ResolvedPrefix{ID: id, CIDR: detail.CIDR, Gateway: gateway})
	}
	return resolved, inRangeIDs, nil
}

func setPoolReadyCondition(pool statusPool, resolveErr error) {
	if resolveErr != nil {
		setConditionFalse(pool, reasonPrefixResolutionFailed, resolveErr.Error())
		return
	}
	pool.PoolStatus().Conditions = []metav1.Condition{{
		Type:               readyConditionType,
		Status:             metav1.ConditionTrue,
		Reason:             reasonPoolReady,
		Message:            "pool is ready to serve claims",
		ObservedGeneration: pool.GetGeneration(),
	}}
}

func setConditionFalse(pool statusPool, reason, message string) {
	pool.PoolStatus().Conditions = []metav1.Condition{{
		Type:               readyConditionType,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: pool.GetGeneration(),
	}}
}

func ensureClusterNameLabel(pool client.Object, clusterName string) {
	if clusterName == "" {
		return
	}
	labels := pool.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[clusterv1.ClusterNameLabel] = clusterName
	pool.SetLabels(labels)
}

func listAddressesInUse(
	ctx context.Context,
	c client.Client,
	namespace, kind, name string,
) ([]ipamv1.IPAddress, error) {
	list := &ipamv1.IPAddressList{}
	opts := []client.ListOption{
		client.MatchingFields{
			index.IPAddressPoolRefCombinedField: index.IPPoolRefValue(ipamv1.IPPoolReference{
				APIGroup: ipamv1alpha1.GroupVersion.Group,
				Kind:     kind,
				Name:     name,
			}),
		},
	}
	if namespace != "" {
		opts = append(opts, client.InNamespace(namespace))
	}
	if err := c.List(ctx, list, opts...); err != nil {
		return nil, err
	}
	return list.Items, nil
}

func ignoreNotFound(err error) error {
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func safeAllocatedCount(count int) (int32, error) {
	value, err := strconv.ParseInt(strconv.FormatInt(int64(count), 10), 10, 32)
	if err != nil {
		return 0, fmt.Errorf("convert allocated address count %d to int32: %w", count, err)
	}

	return int32(value), nil
}
