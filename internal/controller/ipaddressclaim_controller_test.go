package controller

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ipamv1 "sigs.k8s.io/cluster-api/api/ipam/v1beta2"

	ipamv1alpha1 "github.com/evenh/cluster-api-ipam-provider-netbox/api/v1alpha1"
	nb "github.com/evenh/cluster-api-ipam-provider-netbox/internal/netbox"
)

func TestFetchPool(t *testing.T) {
	scheme := newControllerTestScheme(t)
	ctx := context.Background()

	namespacedPool := &ipamv1alpha1.NetBoxIPPool{
		ObjectMeta: metav1.ObjectMeta{Name: "pool", Namespace: "default"},
	}
	globalPool := &ipamv1alpha1.GlobalNetBoxIPPool{
		ObjectMeta: metav1.ObjectMeta{Name: "global-pool"},
	}

	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(namespacedPool, globalPool).Build()

	t.Run("fetches namespaced pool from claim namespace", func(t *testing.T) {
		handler := &netboxClaimHandler{
			Client: k8sClient,
			claim: &ipamv1.IPAddressClaim{
				ObjectMeta: metav1.ObjectMeta{Name: "claim", Namespace: "default"},
				Spec: ipamv1.IPAddressClaimSpec{
					PoolRef: ipamv1.IPPoolReference{
						Name:     "pool",
						Kind:     ipamv1alpha1.NetBoxIPPoolKind,
						APIGroup: ipamv1alpha1.GroupVersion.Group,
					},
				},
			},
		}

		pool, res, err := handler.FetchPool(ctx)
		if err != nil || res != nil {
			t.Fatalf("FetchPool() error = %v result = %#v", err, res)
		}
		if _, ok := pool.(*ipamv1alpha1.NetBoxIPPool); !ok {
			t.Fatalf("FetchPool() returned unexpected pool type %T", pool)
		}
	})

	t.Run("fetches global pool without namespace", func(t *testing.T) {
		handler := &netboxClaimHandler{
			Client: k8sClient,
			claim: &ipamv1.IPAddressClaim{
				ObjectMeta: metav1.ObjectMeta{Name: "claim", Namespace: "default"},
				Spec: ipamv1.IPAddressClaimSpec{
					PoolRef: ipamv1.IPPoolReference{
						Name:     "global-pool",
						Kind:     ipamv1alpha1.GlobalNetBoxIPPoolKind,
						APIGroup: ipamv1alpha1.GroupVersion.Group,
					},
				},
			},
		}

		pool, res, err := handler.FetchPool(ctx)
		if err != nil || res != nil {
			t.Fatalf("FetchPool() error = %v result = %#v", err, res)
		}
		if _, ok := pool.(*ipamv1alpha1.GlobalNetBoxIPPool); !ok {
			t.Fatalf("FetchPool() returned unexpected pool type %T", pool)
		}
	})
}

func TestEnsureAddress(t *testing.T) {
	ctx := context.Background()
	scheme := newControllerTestScheme(t)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "netbox", Namespace: "default"},
		Data: map[string][]byte{
			nb.SecretKeyURL:   []byte("https://netbox.example.com"),
			nb.SecretKeyToken: []byte("token"),
		},
	}
	pool := &ipamv1alpha1.NetBoxIPPool{
		ObjectMeta: metav1.ObjectMeta{Name: "pool", Namespace: "default"},
		Spec: ipamv1alpha1.NetBoxIPPoolSpec{
			ConnectionSecretRef: ipamv1alpha1.NamespacedSecretReference{Name: "netbox"},
			Prefixes: []ipamv1alpha1.NetBoxPrefixReference{
				{ID: int32Ptr(100)},
				{ID: int32Ptr(200)},
			},
			MetadataDefaults: ipamv1alpha1.NetBoxMetadata{
				TenantID: int32Ptr(5),
				VRFID:    int32Ptr(7),
				DNSName:  "pool.example.com",
				Tags:     []string{"pool-tag"},
				CustomFields: map[string]string{
					"source": "pool",
				},
			},
		},
	}
	claim := &ipamv1.IPAddressClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "claim",
			Namespace: "default",
			UID:       types.UID("claim-uid"),
			Annotations: map[string]string{
				nb.AnnotationDNSName:      "claim.example.com",
				nb.AnnotationTags:         "claim-tag",
				nb.AnnotationCustomFields: `{"owner":"claim"}`,
			},
		},
		Spec: ipamv1.IPAddressClaimSpec{
			PoolRef: ipamv1.IPPoolReference{
				Name:     "pool",
				Kind:     ipamv1alpha1.NetBoxIPPoolKind,
				APIGroup: ipamv1alpha1.GroupVersion.Group,
			},
		},
	}

	t.Run("allocates address using resolved prefixes and merged metadata", func(t *testing.T) {
		testEnsureAddressAllocatesUsingResolvedPrefixes(ctx, t, scheme, secret, pool, claim)
	})
	t.Run("does not reallocate when address is already set", func(t *testing.T) {
		testEnsureAddressSkipsWhenAlreadySet(ctx, t, scheme, secret, pool, claim)
	})
	t.Run("reuses an existing NetBox IP for this claim UID instead of reallocating", func(t *testing.T) {
		testEnsureAddressReusesExistingNetBoxIP(ctx, t, scheme, secret, pool, claim)
	})
	t.Run("uses the pool's cached resolved prefixes instead of resolving again", func(t *testing.T) {
		testEnsureAddressUsesCachedResolvedPrefixes(ctx, t, scheme, secret, pool, claim)
	})
	t.Run("requeues when every prefix is exhausted", func(t *testing.T) {
		testEnsureAddressRequeuesWhenExhausted(ctx, t, scheme, secret, pool, claim)
	})
}

func testEnsureAddressAllocatesUsingResolvedPrefixes(
	ctx context.Context,
	t *testing.T,
	scheme *runtime.Scheme,
	secret *corev1.Secret,
	pool *ipamv1alpha1.NetBoxIPPool,
	claim *ipamv1.IPAddressClaim,
) {
	t.Helper()

	fakeNetBox := &fakeNetBoxClient{
		resolvedPrefixIDs: []int32{100, 200},
		allocations: map[int32]fakeAllocationResult{
			100: {
				address: &nb.AllocatedAddress{
					ID:      42,
					Address: "10.0.0.5",
					Prefix:  24,
					DNSName: "claim.example.com",
				},
			},
		},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret.DeepCopy(), pool.DeepCopy()).Build()
	handler := &netboxClaimHandler{
		Client: k8sClient,
		claim:  claim.DeepCopy(),
		pool:   pool.DeepCopy(),
		newClientFunc: func(nb.ConnectionConfig) (nb.Client, error) {
			return fakeNetBox, nil
		},
	}

	address := &ipamv1.IPAddress{}
	res, err := handler.EnsureAddress(ctx, address)
	if err != nil || res != nil {
		t.Fatalf("EnsureAddress() error = %v result = %#v", err, res)
	}
	if address.Spec.Address != "10.0.0.5" {
		t.Fatalf("unexpected address: %#v", address.Spec)
	}
	if address.Spec.Prefix == nil || *address.Spec.Prefix != 24 {
		t.Fatalf("unexpected prefix: %#v", address.Spec.Prefix)
	}
	if len(fakeNetBox.allocateCalls) != 1 || fakeNetBox.allocateCalls[0] != 100 {
		t.Fatalf("unexpected allocate calls: %#v", fakeNetBox.allocateCalls)
	}
	if fakeNetBox.ensureFieldName != nb.DefaultClaimUIDCustomField {
		t.Fatalf("unexpected custom field check: %q", fakeNetBox.ensureFieldName)
	}
	if fakeNetBox.lastRequest == nil {
		t.Fatal("expected allocation request to be recorded")
	}
	if fakeNetBox.lastRequest.Metadata.DNSName != "claim.example.com" {
		t.Fatalf("unexpected dns name: %#v", fakeNetBox.lastRequest.Metadata)
	}
	if len(fakeNetBox.lastRequest.Metadata.Tags) != 1 || fakeNetBox.lastRequest.Metadata.Tags[0] != "claim-tag" {
		t.Fatalf("unexpected tags: %#v", fakeNetBox.lastRequest.Metadata.Tags)
	}
	if fakeNetBox.lastRequest.Metadata.CustomFields["source"] != "pool" ||
		fakeNetBox.lastRequest.Metadata.CustomFields["owner"] != "claim" {
		t.Fatalf("unexpected custom fields: %#v", fakeNetBox.lastRequest.Metadata.CustomFields)
	}
	if fakeNetBox.lastRequest.ClaimUID != "claim-uid" {
		t.Fatalf("unexpected claim uid: %q", fakeNetBox.lastRequest.ClaimUID)
	}
}

func testEnsureAddressSkipsWhenAlreadySet(
	ctx context.Context,
	t *testing.T,
	scheme *runtime.Scheme,
	secret *corev1.Secret,
	pool *ipamv1alpha1.NetBoxIPPool,
	claim *ipamv1.IPAddressClaim,
) {
	t.Helper()

	fakeNetBox := &fakeNetBoxClient{
		resolvedPrefixIDs: []int32{100, 200},
		allocations: map[int32]fakeAllocationResult{
			100: {
				address: &nb.AllocatedAddress{ID: 42, Address: "10.0.0.5", Prefix: 24},
			},
		},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret.DeepCopy(), pool.DeepCopy()).Build()
	handler := &netboxClaimHandler{
		Client: k8sClient,
		claim:  claim.DeepCopy(),
		pool:   pool.DeepCopy(),
		newClientFunc: func(nb.ConnectionConfig) (nb.Client, error) {
			return fakeNetBox, nil
		},
	}

	address := &ipamv1.IPAddress{
		Spec: ipamv1.IPAddressSpec{
			Address: "10.0.0.9",
			Prefix:  int32Ptr(24),
		},
	}
	res, err := handler.EnsureAddress(ctx, address)
	if err != nil || res != nil {
		t.Fatalf("EnsureAddress() error = %v result = %#v", err, res)
	}
	if address.Spec.Address != "10.0.0.9" {
		t.Fatalf("unexpected address mutation: %#v", address.Spec)
	}
	if address.Spec.Prefix == nil || *address.Spec.Prefix != 24 {
		t.Fatalf("unexpected prefix mutation: %#v", address.Spec.Prefix)
	}
	if len(fakeNetBox.allocateCalls) != 0 {
		t.Fatalf("unexpected allocate calls: %#v", fakeNetBox.allocateCalls)
	}
	if fakeNetBox.ensureFieldName != "" {
		t.Fatalf("unexpected custom field check: %q", fakeNetBox.ensureFieldName)
	}
	if fakeNetBox.lastRequest != nil {
		t.Fatalf("unexpected allocation request: %#v", fakeNetBox.lastRequest)
	}
}

func testEnsureAddressReusesExistingNetBoxIP(
	ctx context.Context,
	t *testing.T,
	scheme *runtime.Scheme,
	secret *corev1.Secret,
	pool *ipamv1alpha1.NetBoxIPPool,
	claim *ipamv1.IPAddressClaim,
) {
	t.Helper()

	fakeNetBox := &fakeNetBoxClient{
		resolvedPrefixIDs: []int32{100, 200},
		findResult:        &nb.AllocatedAddress{ID: 42, Address: "10.0.0.5", Prefix: 24},
		allocations: map[int32]fakeAllocationResult{
			100: {address: &nb.AllocatedAddress{ID: 99, Address: "10.0.0.99", Prefix: 24}},
		},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret.DeepCopy(), pool.DeepCopy()).Build()
	handler := &netboxClaimHandler{
		Client: k8sClient,
		claim:  claim.DeepCopy(),
		pool:   pool.DeepCopy(),
		newClientFunc: func(nb.ConnectionConfig) (nb.Client, error) {
			return fakeNetBox, nil
		},
	}

	address := &ipamv1.IPAddress{}
	res, err := handler.EnsureAddress(ctx, address)
	if err != nil || res != nil {
		t.Fatalf("EnsureAddress() error = %v result = %#v", err, res)
	}
	if address.Spec.Address != "10.0.0.5" {
		t.Fatalf("expected the pre-existing NetBox IP to be reused, got: %#v", address.Spec)
	}
	if address.Spec.Prefix == nil || *address.Spec.Prefix != 24 {
		t.Fatalf("unexpected prefix: %#v", address.Spec.Prefix)
	}
	if len(fakeNetBox.allocateCalls) != 0 {
		t.Fatalf("expected no new allocation when an existing IP is found, got: %#v", fakeNetBox.allocateCalls)
	}
	if fakeNetBox.ensureFieldName != "" {
		t.Fatalf("expected no custom field check when reusing an existing IP, got: %q", fakeNetBox.ensureFieldName)
	}
}

func testEnsureAddressUsesCachedResolvedPrefixes(
	ctx context.Context,
	t *testing.T,
	scheme *runtime.Scheme,
	secret *corev1.Secret,
	pool *ipamv1alpha1.NetBoxIPPool,
	claim *ipamv1.IPAddressClaim,
) {
	t.Helper()

	fakeNetBox := &fakeNetBoxClient{
		allocations: map[int32]fakeAllocationResult{
			200: {address: &nb.AllocatedAddress{ID: 42, Address: "10.0.0.5", Prefix: 24}},
		},
	}
	cachedPool := pool.DeepCopy()
	cachedPool.Status.ResolvedPrefixes = []int32{200}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(secret.DeepCopy(), cachedPool.DeepCopy()).
		Build()
	handler := &netboxClaimHandler{
		Client: k8sClient,
		claim:  claim.DeepCopy(),
		pool:   cachedPool,
		newClientFunc: func(nb.ConnectionConfig) (nb.Client, error) {
			return fakeNetBox, nil
		},
	}

	address := &ipamv1.IPAddress{}
	res, err := handler.EnsureAddress(ctx, address)
	if err != nil || res != nil {
		t.Fatalf("EnsureAddress() error = %v result = %#v", err, res)
	}
	if fakeNetBox.resolveCalls != 0 {
		t.Fatalf(
			"expected ResolvePrefixIDs not to be called when status.resolvedPrefixes is cached, got %d calls",
			fakeNetBox.resolveCalls,
		)
	}
	if len(fakeNetBox.allocateCalls) != 1 || fakeNetBox.allocateCalls[0] != 200 {
		t.Fatalf("expected allocation against the cached prefix 200, got: %#v", fakeNetBox.allocateCalls)
	}
}

func testEnsureAddressRequeuesWhenExhausted(
	ctx context.Context,
	t *testing.T,
	scheme *runtime.Scheme,
	secret *corev1.Secret,
	pool *ipamv1alpha1.NetBoxIPPool,
	claim *ipamv1.IPAddressClaim,
) {
	t.Helper()

	fakeNetBox := &fakeNetBoxClient{
		resolvedPrefixIDs: []int32{100, 200},
		allocations: map[int32]fakeAllocationResult{
			100: {err: nb.ErrNoAvailableIP},
			200: {err: nb.ErrNoAvailableIP},
		},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret.DeepCopy(), pool.DeepCopy()).Build()
	handler := &netboxClaimHandler{
		Client: k8sClient,
		claim:  claim.DeepCopy(),
		pool:   pool.DeepCopy(),
		newClientFunc: func(nb.ConnectionConfig) (nb.Client, error) {
			return fakeNetBox, nil
		},
	}

	res, err := handler.EnsureAddress(ctx, &ipamv1.IPAddress{})
	if err == nil {
		t.Fatal("expected exhaustion error")
	}
	if res == nil || res.RequeueAfter != poolExhaustedRequeueAfter {
		t.Fatalf("unexpected result: %#v", res)
	}
	if len(fakeNetBox.allocateCalls) != 2 {
		t.Fatalf("unexpected allocate calls: %#v", fakeNetBox.allocateCalls)
	}
}

func TestReleaseAddress(t *testing.T) {
	ctx := context.Background()
	scheme := newControllerTestScheme(t)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "netbox", Namespace: "default"},
		Data: map[string][]byte{
			nb.SecretKeyURL:   []byte("https://netbox.example.com"),
			nb.SecretKeyToken: []byte("token"),
		},
	}
	pool := &ipamv1alpha1.NetBoxIPPool{
		ObjectMeta: metav1.ObjectMeta{Name: "pool", Namespace: "default"},
		Spec: ipamv1alpha1.NetBoxIPPoolSpec{
			ConnectionSecretRef: ipamv1alpha1.NamespacedSecretReference{Name: "netbox"},
		},
	}
	claim := &ipamv1.IPAddressClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "claim",
			Namespace: "default",
			UID:       types.UID("claim-uid"),
		},
	}

	t.Run("deletes the remote IP when claim-owned address exists", func(t *testing.T) {
		fakeNetBox := &fakeNetBoxClient{
			findResult: &nb.AllocatedAddress{ID: 42, Address: "10.0.0.5", Prefix: 24},
		}
		address := &ipamv1.IPAddress{
			ObjectMeta: metav1.ObjectMeta{Name: "claim", Namespace: "default"},
			Spec: ipamv1.IPAddressSpec{
				Address: "10.0.0.5",
				Prefix:  int32Ptr(24),
			},
		}
		k8sClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(secret.DeepCopy(), pool.DeepCopy(), address).
			Build()
		handler := &netboxClaimHandler{
			Client: k8sClient,
			claim:  claim.DeepCopy(),
			pool:   pool.DeepCopy(),
			newClientFunc: func(nb.ConnectionConfig) (nb.Client, error) {
				return fakeNetBox, nil
			},
		}

		res, err := handler.ReleaseAddress(ctx)
		if err != nil || res != nil {
			t.Fatalf("ReleaseAddress() error = %v result = %#v", err, res)
		}
		if len(fakeNetBox.deleteCalls) != 1 || fakeNetBox.deleteCalls[0] != 42 {
			t.Fatalf("unexpected delete calls: %#v", fakeNetBox.deleteCalls)
		}
	})

	t.Run("falls back to address lookup when claim uid lookup misses", func(t *testing.T) {
		fakeNetBox := &fakeNetBoxClient{
			findByAddressResult: &nb.AllocatedAddress{ID: 51, Address: "10.0.0.6", Prefix: 24},
		}
		address := &ipamv1.IPAddress{
			ObjectMeta: metav1.ObjectMeta{Name: "claim", Namespace: "default"},
			Spec: ipamv1.IPAddressSpec{
				Address: "10.0.0.6",
				Prefix:  int32Ptr(24),
			},
		}
		k8sClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(secret.DeepCopy(), pool.DeepCopy(), address).
			Build()
		handler := &netboxClaimHandler{
			Client: k8sClient,
			claim:  claim.DeepCopy(),
			pool:   pool.DeepCopy(),
			newClientFunc: func(nb.ConnectionConfig) (nb.Client, error) {
				return fakeNetBox, nil
			},
		}

		res, err := handler.ReleaseAddress(ctx)
		if err != nil || res != nil {
			t.Fatalf("ReleaseAddress() error = %v result = %#v", err, res)
		}
		if len(fakeNetBox.findByAddressCalls) != 1 || fakeNetBox.findByAddressCalls[0] != "10.0.0.6/24" {
			t.Fatalf("unexpected address lookup calls: %#v", fakeNetBox.findByAddressCalls)
		}
		if len(fakeNetBox.deleteCalls) != 1 || fakeNetBox.deleteCalls[0] != 51 {
			t.Fatalf("unexpected delete calls: %#v", fakeNetBox.deleteCalls)
		}
	})

	t.Run("does nothing when remote IP is already gone", func(t *testing.T) {
		fakeNetBox := &fakeNetBoxClient{}
		k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret.DeepCopy(), pool.DeepCopy()).Build()
		handler := &netboxClaimHandler{
			Client: k8sClient,
			claim:  claim.DeepCopy(),
			pool:   pool.DeepCopy(),
			newClientFunc: func(nb.ConnectionConfig) (nb.Client, error) {
				return fakeNetBox, nil
			},
		}

		if _, err := handler.ReleaseAddress(ctx); err != nil {
			t.Fatalf("ReleaseAddress() error = %v", err)
		}
		if len(fakeNetBox.deleteCalls) != 0 {
			t.Fatalf("unexpected delete calls: %#v", fakeNetBox.deleteCalls)
		}
	})
}

type fakeNetBoxClient struct {
	resolvedPrefixIDs   []int32
	resolveErr          error
	resolveCalls        int
	ensureFieldName     string
	ensureErr           error
	allocations         map[int32]fakeAllocationResult
	allocateCalls       []int32
	lastRequest         *nb.AllocationRequest
	findResult          *nb.AllocatedAddress
	findErr             error
	findArgs            []string
	findByAddressResult *nb.AllocatedAddress
	findByAddressErr    error
	findByAddressCalls  []string
	deleteCalls         []int32
	deleteErr           error
}

type fakeAllocationResult struct {
	address *nb.AllocatedAddress
	err     error
}

func (f *fakeNetBoxClient) ResolvePrefixIDs(
	_ context.Context,
	_ []ipamv1alpha1.NetBoxPrefixReference,
) ([]int32, error) {
	f.resolveCalls++
	return f.resolvedPrefixIDs, f.resolveErr
}

func (f *fakeNetBoxClient) EnsureIPAddressCustomField(_ context.Context, fieldName string) error {
	f.ensureFieldName = fieldName
	return f.ensureErr
}

func (f *fakeNetBoxClient) AllocateIPAddress(
	_ context.Context,
	prefixID int32,
	req nb.AllocationRequest,
) (*nb.AllocatedAddress, error) {
	f.allocateCalls = append(f.allocateCalls, prefixID)
	f.lastRequest = &req
	result, ok := f.allocations[prefixID]
	if !ok {
		return nil, nb.ErrNoAvailableIP
	}
	return result.address, result.err
}

func (f *fakeNetBoxClient) FindIPAddressByClaimUID(
	_ context.Context,
	ownershipTag, fieldName, claimUID string,
) (*nb.AllocatedAddress, error) {
	f.findArgs = []string{ownershipTag, fieldName, claimUID}
	return f.findResult, f.findErr
}

func (f *fakeNetBoxClient) FindIPAddressByAddress(
	_ context.Context,
	_ string,
	address string,
) (*nb.AllocatedAddress, error) {
	f.findByAddressCalls = append(f.findByAddressCalls, address)
	return f.findByAddressResult, f.findByAddressErr
}

func (f *fakeNetBoxClient) DeleteIPAddress(_ context.Context, id int32) error {
	f.deleteCalls = append(f.deleteCalls, id)
	return f.deleteErr
}

func newControllerTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

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
	return scheme
}

func int32Ptr(v int32) *int32 {
	return new(v)
}

var _ nb.Client = (*fakeNetBoxClient)(nil)

func TestEnsureAddressPropagatesClientErrors(t *testing.T) {
	ctx := context.Background()
	scheme := newControllerTestScheme(t)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "netbox", Namespace: "default"},
		Data: map[string][]byte{
			nb.SecretKeyURL:   []byte("https://netbox.example.com"),
			nb.SecretKeyToken: []byte("token"),
		},
	}
	pool := &ipamv1alpha1.NetBoxIPPool{
		ObjectMeta: metav1.ObjectMeta{Name: "pool", Namespace: "default"},
		Spec: ipamv1alpha1.NetBoxIPPoolSpec{
			ConnectionSecretRef: ipamv1alpha1.NamespacedSecretReference{Name: "netbox"},
		},
	}
	claim := &ipamv1.IPAddressClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "claim", Namespace: "default", UID: types.UID("claim-uid")},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret, pool).Build()

	handler := &netboxClaimHandler{
		Client: k8sClient,
		claim:  claim,
		pool:   pool,
		newClientFunc: func(nb.ConnectionConfig) (nb.Client, error) {
			return &fakeNetBoxClient{ensureErr: errors.New("missing field")}, nil
		},
	}

	if _, err := handler.EnsureAddress(ctx, &ipamv1.IPAddress{}); err == nil {
		t.Fatal("expected error from NetBox client")
	}
}
