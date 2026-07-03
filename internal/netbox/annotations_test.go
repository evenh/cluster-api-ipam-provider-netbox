package netbox

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ipamv1 "sigs.k8s.io/cluster-api/api/ipam/v1beta2"

	ipamv1alpha1 "github.com/evenh/cluster-api-ipam-provider-netbox/api/v1alpha1"
)

func TestEffectivePoolMetadata(t *testing.T) {
	t.Run("returns pool defaults when claim has no annotations", func(t *testing.T) {
		poolDefaults := ipamv1alpha1.NetBoxMetadata{
			TenantID: int32Ptr(10),
			VRFID:    int32Ptr(20),
			DNSName:  "pool.example.com",
			Tags:     []string{"pool", "default"},
			CustomFields: map[string]string{
				"source": "pool",
			},
		}

		metadata, err := EffectivePoolMetadata(poolDefaults, &ipamv1.IPAddressClaim{})
		if err != nil {
			t.Fatalf("EffectivePoolMetadata() error = %v", err)
		}

		if metadata.TenantID == nil || *metadata.TenantID != 10 {
			t.Fatalf("unexpected tenant id: %#v", metadata.TenantID)
		}
		if metadata.VRFID == nil || *metadata.VRFID != 20 {
			t.Fatalf("unexpected vrf id: %#v", metadata.VRFID)
		}
		if metadata.DNSName != "pool.example.com" {
			t.Fatalf("unexpected dns name: %q", metadata.DNSName)
		}
		if len(metadata.Tags) != 2 || metadata.Tags[0] != "pool" || metadata.Tags[1] != "default" {
			t.Fatalf("unexpected tags: %#v", metadata.Tags)
		}
		if metadata.CustomFields["source"] != "pool" {
			t.Fatalf("unexpected custom fields: %#v", metadata.CustomFields)
		}

		metadata.Tags[0] = "changed"
		metadata.CustomFields["source"] = "changed"
		if poolDefaults.Tags[0] != "pool" {
			t.Fatalf("pool defaults tags were mutated: %#v", poolDefaults.Tags)
		}
		if poolDefaults.CustomFields["source"] != "pool" {
			t.Fatalf("pool defaults custom fields were mutated: %#v", poolDefaults.CustomFields)
		}
	})

	t.Run("claim annotations override and merge metadata", func(t *testing.T) {
		poolDefaults := ipamv1alpha1.NetBoxMetadata{
			TenantID: int32Ptr(10),
			VRFID:    int32Ptr(20),
			DNSName:  "pool.example.com",
			Tags:     []string{"pool"},
			CustomFields: map[string]string{
				"source": "pool",
			},
		}
		claim := &ipamv1.IPAddressClaim{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					AnnotationTenantID:     "11",
					AnnotationVRFID:        "21",
					AnnotationDNSName:      "claim.example.com",
					AnnotationTags:         "claim, extra",
					AnnotationCustomFields: `{"owner":"claim"}`,
				},
			},
		}

		metadata, err := EffectivePoolMetadata(poolDefaults, claim)
		if err != nil {
			t.Fatalf("EffectivePoolMetadata() error = %v", err)
		}

		if metadata.TenantID == nil || *metadata.TenantID != 11 {
			t.Fatalf("unexpected tenant id: %#v", metadata.TenantID)
		}
		if metadata.VRFID == nil || *metadata.VRFID != 21 {
			t.Fatalf("unexpected vrf id: %#v", metadata.VRFID)
		}
		if metadata.DNSName != "claim.example.com" {
			t.Fatalf("unexpected dns name: %q", metadata.DNSName)
		}
		if len(metadata.Tags) != 2 || metadata.Tags[0] != "claim" || metadata.Tags[1] != "extra" {
			t.Fatalf("unexpected tags: %#v", metadata.Tags)
		}
		if metadata.CustomFields["source"] != "pool" || metadata.CustomFields["owner"] != "claim" {
			t.Fatalf("unexpected custom fields: %#v", metadata.CustomFields)
		}
	})

	t.Run("returns error for invalid numeric annotation", func(t *testing.T) {
		claim := &ipamv1.IPAddressClaim{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					AnnotationTenantID: "invalid",
				},
			},
		}

		if _, err := EffectivePoolMetadata(ipamv1alpha1.NetBoxMetadata{}, claim); err == nil {
			t.Fatal("expected parse error for invalid tenant id")
		}
	})

	t.Run("returns error for invalid custom fields annotation", func(t *testing.T) {
		claim := &ipamv1.IPAddressClaim{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					AnnotationCustomFields: "{invalid",
				},
			},
		}

		if _, err := EffectivePoolMetadata(ipamv1alpha1.NetBoxMetadata{}, claim); err == nil {
			t.Fatal("expected parse error for invalid custom fields")
		}
	})
}

func TestDefaults(t *testing.T) {
	spec := &ipamv1alpha1.NetBoxIPPoolSpec{}
	if got := OwnershipTag(spec); got != DefaultOwnershipTag {
		t.Fatalf("OwnershipTag() = %q, want %q", got, DefaultOwnershipTag)
	}
	if got := ClaimUIDCustomField(spec); got != DefaultClaimUIDCustomField {
		t.Fatalf("ClaimUIDCustomField() = %q, want %q", got, DefaultClaimUIDCustomField)
	}
	if got := IPAddressStatus(spec); got != DefaultIPAddressStatus {
		t.Fatalf("IPAddressStatus() = %q, want %q", got, DefaultIPAddressStatus)
	}

	spec.OwnershipTag = "custom-tag"
	spec.ClaimUIDCustomField = "claim_uid"
	spec.IPAddressStatus = "reserved"
	if got := OwnershipTag(spec); got != "custom-tag" {
		t.Fatalf("OwnershipTag() = %q, want custom-tag", got)
	}
	if got := ClaimUIDCustomField(spec); got != "claim_uid" {
		t.Fatalf("ClaimUIDCustomField() = %q, want claim_uid", got)
	}
	if got := IPAddressStatus(spec); got != "reserved" {
		t.Fatalf("IPAddressStatus() = %q, want reserved", got)
	}
}

func int32Ptr(v int32) *int32 {
	return new(v)
}
