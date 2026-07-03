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

// Package v1alpha1 contains API Schema definitions for the ipam v1alpha1 API group.
//
// # Two pool kinds
//
// NetBoxIPPool is namespaced: it can only be referenced by IPAddressClaims in its own namespace.
// GlobalNetBoxIPPool is cluster-scoped: it can be referenced by IPAddressClaims in any namespace, and
// must set connectionSecretRef.namespace explicitly since it has no owning namespace of its own.
// Both allocate from pre-existing NetBox prefixes and never create the prefixes themselves.
//
// # What the provider writes to NetBox versus what it expects to already exist
//
// The provider creates and deletes a NetBox IP address record for each accepted/deleted
// IPAddressClaim, and creates missing NetBox tags (including OwnershipTag) on demand. It does not
// create NetBox prefixes or the ClaimUIDCustomField custom field definition; both must already exist
// in NetBox before a pool can allocate addresses.
//
// # Claim-level overrides
//
// A claim can override NetBoxIPPoolSpec.MetadataDefaults for its own allocation using annotations on
// the IPAddressClaim, all under the "ipam.netbox.cluster.x-k8s.io/" prefix:
//
//   - tenant-id: overrides MetadataDefaults.TenantID (integer)
//   - vrf-id: overrides MetadataDefaults.VRFID (integer)
//   - dns-name: overrides MetadataDefaults.DNSName
//   - tags: overrides MetadataDefaults.Tags (comma-separated)
//   - custom-fields: merges into MetadataDefaults.CustomFields (JSON object of string values)
//
// +kubebuilder:object:generate=true
// +groupName=ipam.cluster.x-k8s.io
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is group version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: "ipam.cluster.x-k8s.io", Version: "v1alpha1"}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme.
	SchemeBuilder = runtime.NewSchemeBuilder(func(s *runtime.Scheme) error {
		metav1.AddToGroupVersion(s, GroupVersion)
		return nil
	})

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

var (
	GroupKindNetBoxIPPool = metav1.GroupKind{
		Group: GroupVersion.Group,
		Kind:  NetBoxIPPoolKind,
	}
	GroupKindGlobalNetBoxIPPool = metav1.GroupKind{
		Group: GroupVersion.Group,
		Kind:  GlobalNetBoxIPPoolKind,
	}
)
