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

	kerrors "k8s.io/apimachinery/pkg/util/errors"
	ipamv1 "sigs.k8s.io/cluster-api/api/ipam/v1beta2"
	"sigs.k8s.io/cluster-api/util/patch"
	capipredicates "sigs.k8s.io/cluster-api/util/predicates"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/log"

	ipamv1alpha1 "github.com/evenh/cluster-api-ipam-provider-netbox/api/v1alpha1"
	nb "github.com/evenh/cluster-api-ipam-provider-netbox/internal/netbox"
	"github.com/evenh/cluster-api-ipam-provider-netbox/pkg/reconcileutil"
)

// GlobalNetBoxIPPoolReconciler reconciles a GlobalNetBoxIPPool object.
type GlobalNetBoxIPPoolReconciler struct {
	reconcileutil.ControllerBase

	WatchFilterValue string
	NewClient        func(nb.ConnectionConfig) (nb.Client, error)
}

// +kubebuilder:rbac:groups=ipam.cluster.x-k8s.io,resources=globalnetboxippools,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ipam.cluster.x-k8s.io,resources=globalnetboxippools/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ipam.cluster.x-k8s.io,resources=globalnetboxippools/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

func (r *GlobalNetBoxIPPoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	logger := log.FromContext(ctx)
	pool := &ipamv1alpha1.GlobalNetBoxIPPool{}
	if err := r.Get(ctx, req.NamespacedName, pool); err != nil {
		return ctrl.Result{}, ignoreNotFound(err)
	}

	patchHelper, err := patch.NewHelper(pool, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}
	defer func() {
		if patchErr := patchHelper.Patch(ctx, pool); patchErr != nil {
			reterr = kerrors.NewAggregate([]error{reterr, patchErr})
		}
	}()

	newClientFunc := r.NewClient
	if newClientFunc == nil {
		newClientFunc = nb.NewClient
	}

	if err = reconcilePoolStatus(
		ctx,
		r.Client,
		r,
		newClientFunc,
		pool,
		ipamv1alpha1.GlobalNetBoxIPPoolKind,
	); err != nil {
		logger.Error(err, "reconcile global pool")
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *GlobalNetBoxIPPoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&ipamv1alpha1.GlobalNetBoxIPPool{}, builder.WithPredicates(
			capipredicates.ResourceNotPausedAndHasFilterLabel(mgr.GetScheme(), mgr.GetLogger(), r.WatchFilterValue),
		)).
		Owns(&ipamv1.IPAddress{}).
		Named("globalnetboxippool").
		Complete(r)
}
