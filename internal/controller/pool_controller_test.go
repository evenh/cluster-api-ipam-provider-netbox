package controller

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ipamv1 "sigs.k8s.io/cluster-api/api/ipam/v1beta2"

	ipamv1alpha1 "github.com/evenh/cluster-api-ipam-provider-netbox/api/v1alpha1"
	"github.com/evenh/cluster-api-ipam-provider-netbox/internal/index"
	"github.com/evenh/cluster-api-ipam-provider-netbox/pkg/reconcileutil"
)

func TestReconcilePoolStatus(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		ipamv1.AddToScheme,
		ipamv1alpha1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatalf("AddToScheme() error = %v", err)
		}
	}

	t.Run("updates allocated count and adds finalizer", func(t *testing.T) {
		pool := &ipamv1alpha1.NetBoxIPPool{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "pool",
				Namespace:  "default",
				Generation: 3,
			},
			Spec: ipamv1alpha1.NetBoxIPPoolSpec{
				ClusterName: "test-cluster",
			},
		}
		address := &ipamv1.IPAddress{
			ObjectMeta: metav1.ObjectMeta{Name: "address-1", Namespace: "default"},
			Spec: ipamv1.IPAddressSpec{
				PoolRef: ipamv1.IPPoolReference{
					APIGroup: ipamv1alpha1.GroupVersion.Group,
					Kind:     ipamv1alpha1.NetBoxIPPoolKind,
					Name:     "pool",
				},
			},
		}
		other := &ipamv1.IPAddress{
			ObjectMeta: metav1.ObjectMeta{Name: "address-2", Namespace: "default"},
			Spec: ipamv1.IPAddressSpec{
				PoolRef: ipamv1.IPPoolReference{
					APIGroup: ipamv1alpha1.GroupVersion.Group,
					Kind:     ipamv1alpha1.NetBoxIPPoolKind,
					Name:     "other",
				},
			},
		}

		k8sClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithIndex(&ipamv1.IPAddress{}, index.IPAddressPoolRefCombinedField, index.IPAddressByCombinedPoolRef).
			WithObjects(address, other).
			Build()

		if err := reconcilePoolStatus(ctx, k8sClient, nil, pool, ipamv1alpha1.NetBoxIPPoolKind); err != nil {
			t.Fatalf("reconcilePoolStatus() error = %v", err)
		}
		if !containsString(pool.Finalizers, poolFinalizer) {
			t.Fatalf("expected finalizer %q, got %#v", poolFinalizer, pool.Finalizers)
		}
		if pool.Status.ObservedGeneration != 3 {
			t.Fatalf("unexpected observed generation: %d", pool.Status.ObservedGeneration)
		}
		if pool.Status.Addresses == nil || pool.Status.Addresses.Allocated != 1 {
			t.Fatalf("unexpected address status: %#v", pool.Status.Addresses)
		}
		if got := pool.Labels[clusterv1.ClusterNameLabel]; got != "test-cluster" {
			t.Fatalf("unexpected cluster label: %q", got)
		}
		if len(pool.Status.Conditions) != 1 || pool.Status.Conditions[0].Status != metav1.ConditionTrue {
			t.Fatalf("unexpected conditions: %#v", pool.Status.Conditions)
		}
	})

	t.Run("preserves existing move label when spec cluster name is empty", func(t *testing.T) {
		pool := &ipamv1alpha1.NetBoxIPPool{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pool",
				Namespace: "default",
				Labels: map[string]string{
					clusterv1.ClusterNameLabel: "existing-cluster",
				},
			},
		}

		k8sClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithIndex(&ipamv1.IPAddress{}, index.IPAddressPoolRefCombinedField, index.IPAddressByCombinedPoolRef).
			Build()

		if err := reconcilePoolStatus(ctx, k8sClient, nil, pool, ipamv1alpha1.NetBoxIPPoolKind); err != nil {
			t.Fatalf("reconcilePoolStatus() error = %v", err)
		}
		if got := pool.Labels[clusterv1.ClusterNameLabel]; got != "existing-cluster" {
			t.Fatalf("unexpected cluster label: %q", got)
		}
	})

	t.Run("blocks finalizer removal while addresses exist", func(t *testing.T) {
		now := metav1.NewTime(time.Now())
		pool := &ipamv1alpha1.NetBoxIPPool{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "pool",
				Namespace:         "default",
				Finalizers:        []string{poolFinalizer},
				DeletionTimestamp: &now,
			},
		}
		address := &ipamv1.IPAddress{
			ObjectMeta: metav1.ObjectMeta{Name: "address-1", Namespace: "default"},
			Spec: ipamv1.IPAddressSpec{
				PoolRef: ipamv1.IPPoolReference{
					APIGroup: ipamv1alpha1.GroupVersion.Group,
					Kind:     ipamv1alpha1.NetBoxIPPoolKind,
					Name:     "pool",
				},
			},
		}

		k8sClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithIndex(&ipamv1.IPAddress{}, index.IPAddressPoolRefCombinedField, index.IPAddressByCombinedPoolRef).
			WithObjects(address).
			Build()

		recorder := events.NewFakeRecorder(1)
		base := reconcileutil.ControllerBase{Recorder: recorder}
		err := reconcilePoolStatus(ctx, k8sClient, base, pool, ipamv1alpha1.NetBoxIPPoolKind)
		if err == nil || !strings.Contains(err.Error(), "still has 1 allocated IPAddresses") {
			t.Fatalf("unexpected error: %v", err)
		}
		if !containsString(pool.Finalizers, poolFinalizer) {
			t.Fatalf("expected finalizer to remain, got %#v", pool.Finalizers)
		}
		event := <-recorder.Events
		if !strings.Contains(event, "Warning") || !strings.Contains(event, reasonPoolInUse) {
			t.Fatalf("unexpected event: %q", event)
		}
	})

	t.Run("removes finalizer when deleting empty pool", func(t *testing.T) {
		now := metav1.NewTime(time.Now())
		pool := &ipamv1alpha1.NetBoxIPPool{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "pool",
				Namespace:         "default",
				Finalizers:        []string{poolFinalizer},
				DeletionTimestamp: &now,
			},
		}

		k8sClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithIndex(&ipamv1.IPAddress{}, index.IPAddressPoolRefCombinedField, index.IPAddressByCombinedPoolRef).
			Build()

		if err := reconcilePoolStatus(ctx, k8sClient, nil, pool, ipamv1alpha1.NetBoxIPPoolKind); err != nil {
			t.Fatalf("reconcilePoolStatus() error = %v", err)
		}
		if containsString(pool.Finalizers, poolFinalizer) {
			t.Fatalf("expected finalizer to be removed, got %#v", pool.Finalizers)
		}
	})
}

func containsString(values []string, target string) bool {
	return slices.Contains(values, target)
}
