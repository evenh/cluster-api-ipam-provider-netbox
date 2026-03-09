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

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

const (
	NetBoxIPPoolKind       = "NetBoxIPPool"
	GlobalNetBoxIPPoolKind = "GlobalNetBoxIPPool"
)

// NamespacedSecretReference points at the secret containing NetBox connection details.
type NamespacedSecretReference struct {
	Name string `json:"name"`
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// NetBoxPrefixReference identifies a NetBox prefix by ID or CIDR.
type NetBoxPrefixReference struct {
	// +optional
	ID *int32 `json:"id,omitempty"`
	// +optional
	CIDR string `json:"cidr,omitempty"`
	// +optional
	VRFID *int32 `json:"vrfID,omitempty"`
}

// NetBoxMetadata carries the supported v1 metadata mapping surface.
type NetBoxMetadata struct {
	// +optional
	TenantID *int32 `json:"tenantID,omitempty"`
	// +optional
	VRFID *int32 `json:"vrfID,omitempty"`
	// +optional
	DNSName string `json:"dnsName,omitempty"`
	// +optional
	Tags []string `json:"tags,omitempty"`
	// +optional
	CustomFields map[string]string `json:"customFields,omitempty"`
}

// NetBoxIPPoolSpec defines the desired state shared by both pool types.
type NetBoxIPPoolSpec struct {
	ConnectionSecretRef NamespacedSecretReference `json:"connectionSecretRef"`
	Prefixes            []NetBoxPrefixReference   `json:"prefixes"`
	// +optional
	MetadataDefaults NetBoxMetadata `json:"metadataDefaults,omitempty"`
	// +optional
	OwnershipTag string `json:"ownershipTag,omitempty"`
	// +optional
	ClaimUIDCustomField string `json:"claimUIDCustomField,omitempty"`
	// +optional
	IPAddressStatus string `json:"ipAddressStatus,omitempty"`
}

// NetBoxPoolStatusAddresses summarises current Kubernetes-side allocations.
type NetBoxPoolStatusAddresses struct {
	Allocated int32 `json:"allocated"`
}

// NetBoxIPPoolStatus defines the observed state shared by both pool types.
type NetBoxIPPoolStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	Addresses *NetBoxPoolStatusAddresses `json:"addresses,omitempty"`
	// +optional
	ResolvedPrefixes []int32 `json:"resolvedPrefixes,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

func (s *NetBoxIPPoolStatus) SetConditions(conditions []metav1.Condition) {
	s.Conditions = conditions
}

func (s *NetBoxIPPoolStatus) GetConditions() []metav1.Condition {
	return s.Conditions
}
