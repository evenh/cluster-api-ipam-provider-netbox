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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ipamv1 "sigs.k8s.io/cluster-api/api/ipam/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	ipamv1alpha1 "github.com/evenh/cluster-api-ipam-provider-netbox/api/v1alpha1"
	"github.com/evenh/cluster-api-ipam-provider-netbox/internal/index"
)

const poolFinalizer = "ipam.cluster.x-k8s.io/pool-protection"

type statusPool interface {
	client.Object
	PoolSpec() *ipamv1alpha1.NetBoxIPPoolSpec
	PoolStatus() *ipamv1alpha1.NetBoxIPPoolStatus
}

func reconcilePoolStatus(ctx context.Context, c client.Client, pool statusPool, kind string) error {
	addresses, err := listAddressesInUse(ctx, c, pool.GetNamespace(), kind, pool.GetName())
	if err != nil {
		return err
	}

	status := pool.PoolStatus()
	status.ObservedGeneration = pool.GetGeneration()
	status.Addresses = &ipamv1alpha1.NetBoxPoolStatusAddresses{Allocated: int32(len(addresses))}
	status.Conditions = []metav1.Condition{{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "PoolReady",
		Message:            "pool is ready to serve claims",
		ObservedGeneration: pool.GetGeneration(),
	}}
	ensureClusterNameLabel(pool, pool.PoolSpec().ClusterName)

	if pool.GetDeletionTimestamp().IsZero() {
		controllerutil.AddFinalizer(pool, poolFinalizer)
		return nil
	}
	if len(addresses) > 0 {
		return fmt.Errorf("pool still has %d allocated IPAddresses", len(addresses))
	}
	controllerutil.RemoveFinalizer(pool, poolFinalizer)
	return nil
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

func listAddressesInUse(ctx context.Context, c client.Client, namespace, kind, name string) ([]ipamv1.IPAddress, error) {
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
