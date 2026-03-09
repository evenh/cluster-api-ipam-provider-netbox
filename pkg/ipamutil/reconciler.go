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

package ipamutil

import (
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ipamv1 "sigs.k8s.io/cluster-api/api/ipam/v1beta2"
	clusterutil "sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/annotations"
	"sigs.k8s.io/cluster-api/util/patch"
	capipredicates "sigs.k8s.io/cluster-api/util/predicates"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	ctrlhandler "sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	ReleaseAddressFinalizer = "ipam.cluster.x-k8s.io/ReleaseAddress"
	ProtectAddressFinalizer = "ipam.cluster.x-k8s.io/ProtectAddress"
)

type ClaimReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	WatchFilterValue string
	Adapter          ProviderAdapter
}

type ProviderAdapter interface {
	SetupWithManager(context.Context, *ctrl.Builder) error
	ClaimHandlerFor(client.Client, *ipamv1.IPAddressClaim) ClaimHandler
}

type ClaimHandler interface {
	FetchPool(ctx context.Context) (client.Object, *ctrl.Result, error)
	EnsureAddress(ctx context.Context, address *ipamv1.IPAddress) (*ctrl.Result, error)
	ReleaseAddress(ctx context.Context) (*ctrl.Result, error)
}

func (r *ClaimReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	if r.Adapter == nil {
		return fmt.Errorf("adapter is nil")
	}
	if err := mgr.GetFieldIndexer().IndexField(ctx, &ipamv1.IPAddressClaim{}, "clusterName", indexClusterName); err != nil {
		return fmt.Errorf("register IPAddressClaim clusterName index: %w", err)
	}

	b := ctrl.NewControllerManagedBy(mgr).
		WithEventFilter(capipredicates.ResourceNotPausedAndHasFilterLabel(mgr.GetScheme(), ctrl.LoggerFrom(ctx), r.WatchFilterValue)).
		Watches(
			&clusterv1.Cluster{},
			ctrlhandler.EnqueueRequestsFromMapFunc(r.clusterToIPClaims),
			builder.WithPredicates(predicate.Funcs{
				UpdateFunc: func(e event.UpdateEvent) bool {
					oldCluster := e.ObjectOld.(*clusterv1.Cluster)
					newCluster := e.ObjectNew.(*clusterv1.Cluster)
					return annotations.IsPaused(oldCluster, oldCluster) && !annotations.IsPaused(newCluster, newCluster)
				},
				CreateFunc: func(e event.CreateEvent) bool {
					cluster := e.Object.(*clusterv1.Cluster)
					return !annotations.IsPaused(cluster, cluster)
				},
				DeleteFunc: func(e event.DeleteEvent) bool {
					cluster := e.Object.(*clusterv1.Cluster)
					return !annotations.IsPaused(cluster, cluster)
				},
			}),
		)

	if err := r.Adapter.SetupWithManager(ctx, b); err != nil {
		return err
	}
	return b.Complete(r)
}

func (r *ClaimReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	log := ctrl.LoggerFrom(ctx)

	claim := &ipamv1.IPAddressClaim{}
	if err := r.Get(ctx, req.NamespacedName, claim); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	var cluster *clusterv1.Cluster
	var err error
	if claim.Spec.ClusterName != "" {
		cluster, err = clusterutil.GetClusterByName(ctx, r.Client, claim.Namespace, claim.Spec.ClusterName)
	} else if _, ok := claim.GetLabels()[clusterv1.ClusterNameLabel]; ok {
		cluster, err = clusterutil.GetClusterFromMetadata(ctx, r.Client, claim.ObjectMeta)
	}
	if err != nil {
		if !apierrors.IsNotFound(err) {
			log.Error(err, "error fetching cluster linked to IPAddressClaim")
			return ctrl.Result{}, err
		}
		if claim.DeletionTimestamp.IsZero() {
			log.Info("IPAddressClaim linked to a cluster that is not found, skipping reconciliation")
			return ctrl.Result{}, nil
		}
	}
	if cluster != nil && annotations.IsPaused(cluster, cluster) {
		log.Info("IPAddressClaim linked to a paused cluster, skipping reconciliation")
		return ctrl.Result{}, nil
	}

	patchHelper, err := patch.NewHelper(claim, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}
	defer func() {
		if err := patchHelper.Patch(ctx, claim); err != nil {
			reterr = kerrors.NewAggregate([]error{reterr, err})
		}
	}()

	if controllerutil.AddFinalizer(claim, ReleaseAddressFinalizer) {
		return ctrl.Result{}, nil
	}

	handler := r.Adapter.ClaimHandlerFor(r.Client, claim)
	pool, res, err := handler.FetchPool(ctx)
	if err != nil || res != nil {
		if apierrors.IsNotFound(err) {
			if !claim.DeletionTimestamp.IsZero() {
				return r.reconcileDelete(ctx, claim, handler)
			}
			return ctrl.Result{}, nil
		}
		return unwrapResult(res), errors.Wrap(err, "fetch pool")
	}
	if pool == nil {
		return ctrl.Result{}, errors.New("pool is nil")
	}
	if annotations.HasPaused(pool) {
		log.Info("IPAddressClaim references a paused Pool, skipping reconciliation", "IPAddressClaim", claim.GetName(), "Pool", pool.GetName())
		return ctrl.Result{}, nil
	}
	if !claim.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, claim, handler)
	}

	address := NewIPAddress(claim, pool)
	operationResult, err := controllerutil.CreateOrPatch(ctx, r.Client, &address, func() error {
		if res, err = handler.EnsureAddress(ctx, &address); err != nil {
			return err
		}
		if err := ensureIPAddressOwnerReferences(r.Scheme, &address, claim, pool); err != nil {
			return errors.Wrap(err, "ensure owner references")
		}
		if val, ok := claim.Labels[clusterv1.ClusterNameLabel]; ok {
			if address.Labels == nil {
				address.Labels = map[string]string{}
			}
			address.Labels[clusterv1.ClusterNameLabel] = val
		}
		_ = controllerutil.AddFinalizer(&address, ProtectAddressFinalizer)
		return nil
	})
	if res != nil || err != nil {
		if err != nil {
			err = errors.Wrap(err, "create or patch address")
		}
		return unwrapResult(res), err
	}

	err = wait.PollUntilContextTimeout(ctx, 5*time.Millisecond, 5*time.Second, true, func(ctx context.Context) (bool, error) {
		key := client.ObjectKeyFromObject(&address)
		if err := r.Client.Get(ctx, key, &ipamv1.IPAddress{}); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		return true, nil
	})
	if err != nil {
		return ctrl.Result{}, errors.Wrapf(err, "wait for IPAddress %s/%s cache visibility", address.Namespace, address.Name)
	}

	log.Info(fmt.Sprintf("IPAddress %s/%s (%s) has been %s", address.Namespace, address.Name, address.Spec.Address, operationResult))
	claim.Status.AddressRef = ipamv1.IPAddressReference{Name: address.Name}
	return ctrl.Result{}, nil
}

func (r *ClaimReconciler) reconcileDelete(ctx context.Context, claim *ipamv1.IPAddressClaim, handler ClaimHandler) (ctrl.Result, error) {
	if res, err := handler.ReleaseAddress(ctx); err != nil {
		return unwrapResult(res), fmt.Errorf("release address: %w", err)
	}

	address := &ipamv1.IPAddress{}
	key := types.NamespacedName{Namespace: claim.Namespace, Name: claim.Name}
	if err := r.Client.Get(ctx, key, address); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, errors.Wrap(err, "fetch address")
	}
	if address.Name != "" {
		patch := client.MergeFrom(address.DeepCopy())
		if controllerutil.RemoveFinalizer(address, ProtectAddressFinalizer) {
			if err := r.Client.Patch(ctx, address, patch); err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, errors.Wrap(err, "remove address finalizer")
			}
		}
		if err := r.Client.Delete(ctx, address); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	}

	controllerutil.RemoveFinalizer(claim, ReleaseAddressFinalizer)
	return ctrl.Result{}, nil
}

func (r *ClaimReconciler) clusterToIPClaims(_ context.Context, cluster client.Object) []reconcile.Request {
	requests := []reconcile.Request{}
	claims := &ipamv1.IPAddressClaimList{}
	if err := r.List(context.Background(), claims, client.MatchingFields{"clusterName": cluster.GetName()}); err != nil {
		return requests
	}
	for _, claim := range claims.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&claim)})
	}
	return requests
}

func indexClusterName(o client.Object) []string {
	claim, ok := o.(*ipamv1.IPAddressClaim)
	if !ok {
		return nil
	}
	if claim.Spec.ClusterName != "" {
		return []string{claim.Spec.ClusterName}
	}
	if clusterName, ok := claim.Labels[clusterv1.ClusterNameLabel]; ok {
		return []string{clusterName}
	}
	return nil
}

func unwrapResult(result *ctrl.Result) ctrl.Result {
	if result == nil {
		return ctrl.Result{}
	}
	return *result
}
