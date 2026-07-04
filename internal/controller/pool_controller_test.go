package controller

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ipamv1 "sigs.k8s.io/cluster-api/api/ipam/v1beta2"

	ipamv1alpha1 "github.com/evenh/cluster-api-ipam-provider-netbox/api/v1alpha1"
	"github.com/evenh/cluster-api-ipam-provider-netbox/internal/index"
	nb "github.com/evenh/cluster-api-ipam-provider-netbox/internal/netbox"
	"github.com/evenh/cluster-api-ipam-provider-netbox/pkg/reconcileutil"
)

var errNetBoxUnreachable = errors.New("netbox unreachable")

func stubNewClient(nb.ConnectionConfig) (nb.Client, error) {
	return &fakeNetBoxClient{resolvedPrefixIDs: []int32{1}}, nil
}

func newPoolTestSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "netbox", Namespace: "default"},
		Data: map[string][]byte{
			nb.SecretKeyURL:   []byte("https://netbox.example.com"),
			nb.SecretKeyToken: []byte("token"),
		},
	}
}

func TestReconcilePoolStatus(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		corev1.AddToScheme,
		ipamv1.AddToScheme,
		ipamv1alpha1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatalf("AddToScheme() error = %v", err)
		}
	}

	t.Run("updates allocated count and adds finalizer", func(t *testing.T) {
		testReconcilePoolStatusUpdatesAllocatedCount(ctx, t, scheme)
	})
	t.Run("skips re-resolving prefixes when the pool spec generation is unchanged", func(t *testing.T) {
		testReconcilePoolStatusSkipsUnchangedGeneration(ctx, t, scheme)
	})
	t.Run("reports Ready=False when prefix resolution fails", func(t *testing.T) {
		testReconcilePoolStatusReportsPrefixResolutionFailure(ctx, t, scheme)
	})
	t.Run("preserves existing move label when spec cluster name is empty", func(t *testing.T) {
		testReconcilePoolStatusPreservesMoveLabel(ctx, t, scheme)
	})
	t.Run("blocks finalizer removal while addresses exist", func(t *testing.T) {
		testReconcilePoolStatusBlocksFinalizerRemoval(ctx, t, scheme)
	})
	t.Run("removes finalizer when deleting empty pool", func(t *testing.T) {
		testReconcilePoolStatusRemovesFinalizer(ctx, t, scheme)
	})
	t.Run("caches resolved gateway and warns when it is in range", func(t *testing.T) {
		testReconcilePoolStatusCachesGateway(ctx, t, scheme)
	})
	t.Run("reports Ready=False when a gateway has a mismatched family", func(t *testing.T) {
		testReconcilePoolStatusGatewayFamilyMismatch(ctx, t, scheme)
	})
}

func testReconcilePoolStatusCachesGateway(ctx context.Context, t *testing.T, scheme *runtime.Scheme) {
	t.Helper()

	pool := &ipamv1alpha1.NetBoxIPPool{
		ObjectMeta: metav1.ObjectMeta{Name: "pool", Namespace: "default", Generation: 1},
		Spec: ipamv1alpha1.NetBoxIPPoolSpec{
			ConnectionSecretRef: ipamv1alpha1.NamespacedSecretReference{Name: "netbox"},
			GatewayCustomField:  "gateway",
			Prefixes:            []ipamv1alpha1.NetBoxPrefixReference{{ID: int32Ptr(1)}},
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithIndex(&ipamv1.IPAddress{}, index.IPAddressPoolRefCombinedField, index.IPAddressByCombinedPoolRef).
		WithObjects(newPoolTestSecret()).
		Build()

	newClientFunc := func(nb.ConnectionConfig) (nb.Client, error) {
		return &fakeNetBoxClient{
			resolvedPrefixIDs: []int32{1},
			prefixDetails: []nb.PrefixDetail{{
				ID:           1,
				CIDR:         "10.0.0.0/24",
				CustomFields: map[string]any{"gateway": "10.0.0.1"},
			}},
		}, nil
	}

	recorder := events.NewFakeRecorder(1)
	base := reconcileutil.ControllerBase{Recorder: recorder}
	if err := reconcilePoolStatus(
		ctx,
		k8sClient,
		base,
		newClientFunc,
		time.Second,
		pool,
		ipamv1alpha1.NetBoxIPPoolKind,
	); err != nil {
		t.Fatalf("reconcilePoolStatus() error = %v", err)
	}

	if len(pool.Status.ResolvedPrefixDetails) != 1 {
		t.Fatalf("expected 1 resolved prefix detail, got %#v", pool.Status.ResolvedPrefixDetails)
	}
	got := pool.Status.ResolvedPrefixDetails[0]
	if got.ID != 1 || got.CIDR != "10.0.0.0/24" || got.Gateway != "10.0.0.1" {
		t.Fatalf("unexpected resolved prefix detail: %#v", got)
	}
	event := <-recorder.Events
	if !strings.Contains(event, "Warning") || !strings.Contains(event, reasonGatewayInRange) {
		t.Fatalf("expected an in-range gateway warning, got: %q", event)
	}
}

func testReconcilePoolStatusGatewayFamilyMismatch(ctx context.Context, t *testing.T, scheme *runtime.Scheme) {
	t.Helper()

	pool := &ipamv1alpha1.NetBoxIPPool{
		ObjectMeta: metav1.ObjectMeta{Name: "pool", Namespace: "default", Generation: 1},
		Spec: ipamv1alpha1.NetBoxIPPoolSpec{
			ConnectionSecretRef: ipamv1alpha1.NamespacedSecretReference{Name: "netbox"},
			Prefixes:            []ipamv1alpha1.NetBoxPrefixReference{{ID: int32Ptr(1), Gateway: "2001:db8::1"}},
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithIndex(&ipamv1.IPAddress{}, index.IPAddressPoolRefCombinedField, index.IPAddressByCombinedPoolRef).
		WithObjects(newPoolTestSecret()).
		Build()

	newClientFunc := func(nb.ConnectionConfig) (nb.Client, error) {
		return &fakeNetBoxClient{
			resolvedPrefixIDs: []int32{1},
			prefixDetails:     []nb.PrefixDetail{{ID: 1, CIDR: "10.0.0.0/24"}},
		}, nil
	}

	err := reconcilePoolStatus(ctx, k8sClient, nil, newClientFunc, time.Second, pool, ipamv1alpha1.NetBoxIPPoolKind)
	if err == nil {
		t.Fatal("expected reconcilePoolStatus() to fail on a family-mismatched gateway")
	}
	if len(pool.Status.Conditions) != 1 || pool.Status.Conditions[0].Reason != reasonPrefixResolutionFailed {
		t.Fatalf("expected Ready=False/%s, got: %#v", reasonPrefixResolutionFailed, pool.Status.Conditions)
	}
}

func testReconcilePoolStatusUpdatesAllocatedCount(ctx context.Context, t *testing.T, scheme *runtime.Scheme) {
	t.Helper()

	pool := &ipamv1alpha1.NetBoxIPPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "pool",
			Namespace:  "default",
			Generation: 3,
		},
		Spec: ipamv1alpha1.NetBoxIPPoolSpec{
			ClusterName:         "test-cluster",
			ConnectionSecretRef: ipamv1alpha1.NamespacedSecretReference{Name: "netbox"},
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
		WithObjects(newPoolTestSecret(), address, other).
		Build()

	if err := reconcilePoolStatus(
		ctx,
		k8sClient,
		nil,
		stubNewClient,
		time.Second,
		pool,
		ipamv1alpha1.NetBoxIPPoolKind,
	); err != nil {
		t.Fatalf("reconcilePoolStatus() error = %v", err)
	}
	if !slices.Contains(pool.Finalizers, poolFinalizer) {
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
	if !slices.Equal(pool.Status.ResolvedPrefixes, []int32{1}) {
		t.Fatalf("expected resolved prefixes to be cached, got: %#v", pool.Status.ResolvedPrefixes)
	}
}

func testReconcilePoolStatusSkipsUnchangedGeneration(ctx context.Context, t *testing.T, scheme *runtime.Scheme) {
	t.Helper()

	pool := &ipamv1alpha1.NetBoxIPPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "pool",
			Namespace:  "default",
			Generation: 1,
		},
		Spec: ipamv1alpha1.NetBoxIPPoolSpec{
			ConnectionSecretRef: ipamv1alpha1.NamespacedSecretReference{Name: "netbox"},
		},
		Status: ipamv1alpha1.NetBoxIPPoolStatus{
			ObservedGeneration: 1,
			ResolvedPrefixes:   []int32{42},
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithIndex(&ipamv1.IPAddress{}, index.IPAddressPoolRefCombinedField, index.IPAddressByCombinedPoolRef).
		Build()

	calls := 0
	newClientFunc := func(nb.ConnectionConfig) (nb.Client, error) {
		calls++
		return &fakeNetBoxClient{resolvedPrefixIDs: []int32{999}}, nil
	}

	if err := reconcilePoolStatus(
		ctx,
		k8sClient,
		nil,
		newClientFunc,
		time.Second,
		pool,
		ipamv1alpha1.NetBoxIPPoolKind,
	); err != nil {
		t.Fatalf("reconcilePoolStatus() error = %v", err)
	}
	if calls != 0 {
		t.Fatalf("expected no NetBox client construction when generation is unchanged, got %d calls", calls)
	}
	if !slices.Equal(pool.Status.ResolvedPrefixes, []int32{42}) {
		t.Fatalf("expected cached resolved prefixes to be preserved, got: %#v", pool.Status.ResolvedPrefixes)
	}
}

func testReconcilePoolStatusReportsPrefixResolutionFailure(ctx context.Context, t *testing.T, scheme *runtime.Scheme) {
	t.Helper()

	pool := &ipamv1alpha1.NetBoxIPPool{
		ObjectMeta: metav1.ObjectMeta{Name: "pool", Namespace: "default"},
		Spec: ipamv1alpha1.NetBoxIPPoolSpec{
			ConnectionSecretRef: ipamv1alpha1.NamespacedSecretReference{Name: "netbox"},
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithIndex(&ipamv1.IPAddress{}, index.IPAddressPoolRefCombinedField, index.IPAddressByCombinedPoolRef).
		WithObjects(newPoolTestSecret()).
		Build()

	newClientFunc := func(nb.ConnectionConfig) (nb.Client, error) {
		return &fakeNetBoxClient{resolveErr: errNetBoxUnreachable}, nil
	}

	err := reconcilePoolStatus(ctx, k8sClient, nil, newClientFunc, time.Second, pool, ipamv1alpha1.NetBoxIPPoolKind)
	if err == nil {
		t.Fatal("expected reconcilePoolStatus() to return the resolution error")
	}
	if !slices.Contains(pool.Finalizers, poolFinalizer) {
		t.Fatalf("expected finalizer to still be added despite the resolution failure, got %#v", pool.Finalizers)
	}
	if len(pool.Status.Conditions) != 1 || pool.Status.Conditions[0].Status != metav1.ConditionFalse {
		t.Fatalf("expected a Ready=False condition, got: %#v", pool.Status.Conditions)
	}
	if pool.Status.Conditions[0].Reason != reasonPrefixResolutionFailed {
		t.Fatalf("unexpected condition reason: %q", pool.Status.Conditions[0].Reason)
	}
}

func testReconcilePoolStatusPreservesMoveLabel(ctx context.Context, t *testing.T, scheme *runtime.Scheme) {
	t.Helper()

	pool := &ipamv1alpha1.NetBoxIPPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pool",
			Namespace: "default",
			Labels: map[string]string{
				clusterv1.ClusterNameLabel: "existing-cluster",
			},
		},
		Spec: ipamv1alpha1.NetBoxIPPoolSpec{
			ConnectionSecretRef: ipamv1alpha1.NamespacedSecretReference{Name: "netbox"},
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithIndex(&ipamv1.IPAddress{}, index.IPAddressPoolRefCombinedField, index.IPAddressByCombinedPoolRef).
		WithObjects(newPoolTestSecret()).
		Build()

	if err := reconcilePoolStatus(
		ctx,
		k8sClient,
		nil,
		stubNewClient,
		time.Second,
		pool,
		ipamv1alpha1.NetBoxIPPoolKind,
	); err != nil {
		t.Fatalf("reconcilePoolStatus() error = %v", err)
	}
	if got := pool.Labels[clusterv1.ClusterNameLabel]; got != "existing-cluster" {
		t.Fatalf("unexpected cluster label: %q", got)
	}
}

func testReconcilePoolStatusBlocksFinalizerRemoval(ctx context.Context, t *testing.T, scheme *runtime.Scheme) {
	t.Helper()

	now := metav1.NewTime(time.Now())
	pool := &ipamv1alpha1.NetBoxIPPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "pool",
			Namespace:         "default",
			Finalizers:        []string{poolFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: ipamv1alpha1.NetBoxIPPoolSpec{
			ConnectionSecretRef: ipamv1alpha1.NamespacedSecretReference{Name: "netbox"},
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
		WithObjects(newPoolTestSecret(), address).
		Build()

	recorder := events.NewFakeRecorder(1)
	base := reconcileutil.ControllerBase{Recorder: recorder}
	err := reconcilePoolStatus(ctx, k8sClient, base, stubNewClient, time.Second, pool, ipamv1alpha1.NetBoxIPPoolKind)
	if err == nil || !strings.Contains(err.Error(), "still has 1 allocated IPAddresses") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !slices.Contains(pool.Finalizers, poolFinalizer) {
		t.Fatalf("expected finalizer to remain, got %#v", pool.Finalizers)
	}
	event := <-recorder.Events
	if !strings.Contains(event, "Warning") || !strings.Contains(event, reasonPoolInUse) {
		t.Fatalf("unexpected event: %q", event)
	}
	if len(pool.Status.Conditions) != 1 || pool.Status.Conditions[0].Reason != reasonDeletionBlocked {
		t.Fatalf("expected a DeletionBlocked condition, got: %#v", pool.Status.Conditions)
	}
}

func testReconcilePoolStatusRemovesFinalizer(ctx context.Context, t *testing.T, scheme *runtime.Scheme) {
	t.Helper()

	now := metav1.NewTime(time.Now())
	pool := &ipamv1alpha1.NetBoxIPPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "pool",
			Namespace:         "default",
			Finalizers:        []string{poolFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: ipamv1alpha1.NetBoxIPPoolSpec{
			ConnectionSecretRef: ipamv1alpha1.NamespacedSecretReference{Name: "netbox"},
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithIndex(&ipamv1.IPAddress{}, index.IPAddressPoolRefCombinedField, index.IPAddressByCombinedPoolRef).
		WithObjects(newPoolTestSecret()).
		Build()

	if err := reconcilePoolStatus(
		ctx,
		k8sClient,
		nil,
		stubNewClient,
		time.Second,
		pool,
		ipamv1alpha1.NetBoxIPPoolKind,
	); err != nil {
		t.Fatalf("reconcilePoolStatus() error = %v", err)
	}
	if slices.Contains(pool.Finalizers, poolFinalizer) {
		t.Fatalf("expected finalizer to be removed, got %#v", pool.Finalizers)
	}
}
