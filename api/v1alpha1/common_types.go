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

// NamespacedSecretReference points at the Secret containing NetBox connection details.
// The Secret must contain a "url" and a "token" key, and may optionally contain
// "insecureSkipVerify" ("true"/"false") and a PEM-encoded "caBundle".
type NamespacedSecretReference struct {
	// Name is the Secret name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// Namespace is the Secret namespace. For namespaced pools this must be left empty; the pool's
	// own namespace is always used, so a namespaced pool cannot read a Secret from another namespace.
	// Global pools are cluster-scoped and must set this explicitly.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// NetBoxPrefixReference identifies a NetBox prefix by ID or CIDR.
// +kubebuilder:validation:XValidation:rule="has(self.id) != has(self.cidr)",message="exactly one of id or cidr must be set"
// +kubebuilder:validation:XValidation:rule="!has(self.vrfID) || has(self.cidr)",message="vrfID can only be set when cidr is used"
// +kubebuilder:validation:XValidation:rule="!has(self.cidr) || self.cidr.contains('/')",message="cidr must include a prefix length, e.g. 10.0.0.0/24"
type NetBoxPrefixReference struct {
	// ID is the NetBox prefix primary key. Use this when the backing prefix object is already known.
	// +optional
	// +kubebuilder:validation:Minimum=1
	ID *int32 `json:"id,omitempty"`
	// CIDR is the prefix in CIDR notation. The provider resolves it to a unique, already-existing
	// NetBox prefix before allocation; it does not create the prefix.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=43
	CIDR string `json:"cidr,omitempty"`
	// VRFID narrows CIDR resolution to a specific NetBox VRF. It only applies when CIDR is used.
	// +optional
	// +kubebuilder:validation:Minimum=1
	VRFID *int32 `json:"vrfID,omitempty"`
}

// NetBoxMetadata carries the supported v1 metadata mapping surface.
type NetBoxMetadata struct {
	// TenantID sets the NetBox tenant on allocated IP addresses.
	// +optional
	// +kubebuilder:validation:Minimum=1
	TenantID *int32 `json:"tenantID,omitempty"`
	// VRFID sets the NetBox VRF on allocated IP addresses.
	// +optional
	// +kubebuilder:validation:Minimum=1
	VRFID *int32 `json:"vrfID,omitempty"`
	// DNSName sets the DNS name stored on the allocated IP address.
	// +optional
	DNSName string `json:"dnsName,omitempty"`
	// Tags are NetBox tags to attach to allocated IP addresses. Claim annotations can override this list.
	// +listType=set
	// +optional
	Tags []string `json:"tags,omitempty"`
	// CustomFields maps NetBox custom field names to string values. Each field must already exist in
	// NetBox on the ipam.ipaddress object type. Claim annotations can add to or override these values.
	// +optional
	CustomFields map[string]string `json:"customFields,omitempty"`
}

// NetBoxIPPoolSpec defines the desired state shared by both pool types.
type NetBoxIPPoolSpec struct {
	// ClusterName associates the pool with a Cluster API Cluster for clusterctl move. When set, the
	// controller mirrors it to the standard cluster.x-k8s.io/cluster-name label on the pool.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])?$`
	ClusterName string `json:"clusterName,omitempty"`
	// ConnectionSecretRef points at the Secret that contains the NetBox connection details.
	ConnectionSecretRef NamespacedSecretReference `json:"connectionSecretRef"`
	// Prefixes lists the candidate NetBox prefixes to allocate from. Each prefix must already exist in
	// NetBox; the provider only resolves and allocates from them, it never creates prefixes. Prefixes
	// are tried in order until an address is allocated or all options are exhausted.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=32
	Prefixes []NetBoxPrefixReference `json:"prefixes"`
	// MetadataDefaults defines the default NetBox fields applied to each allocation before claim-level overrides.
	// +optional
	MetadataDefaults NetBoxMetadata `json:"metadataDefaults,omitempty"`
	// OwnershipTag is added to every allocated NetBox IP address so the provider can find and clean it up
	// later. The tag is created in NetBox automatically if it does not already exist.
	// +optional
	// +kubebuilder:default="cluster-api-ipam-provider-netbox"
	// +kubebuilder:validation:MinLength=1
	OwnershipTag string `json:"ownershipTag,omitempty"`
	// ClaimUIDCustomField is the NetBox custom field used to store the Kubernetes IPAddressClaim UID.
	// This field must already exist in NetBox as a text custom field on the ipam.ipaddress object type;
	// the provider checks for it but does not create it.
	// +optional
	// +kubebuilder:default="cluster_api_claim_uid"
	// +kubebuilder:validation:MinLength=1
	ClaimUIDCustomField string `json:"claimUIDCustomField,omitempty"`
	// IPAddressStatus is the NetBox status assigned to newly allocated IP addresses.
	// +optional
	// +kubebuilder:default="active"
	// +kubebuilder:validation:Enum=active;reserved;deprecated;dhcp;slaac
	IPAddressStatus string `json:"ipAddressStatus,omitempty"`
}

// NetBoxPoolStatusAddresses summarises current Kubernetes-side allocations.
type NetBoxPoolStatusAddresses struct {
	// Allocated is the number of Kubernetes IPAddress objects currently referencing the pool.
	Allocated int32 `json:"allocated"`
}

// NetBoxIPPoolStatus defines the observed state shared by both pool types.
type NetBoxIPPoolStatus struct {
	// ObservedGeneration is the most recent metadata.generation processed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Addresses summarises the current number of Kubernetes-side allocations using this pool.
	// +optional
	Addresses *NetBoxPoolStatusAddresses `json:"addresses,omitempty"`
	// ResolvedPrefixes reports the concrete NetBox prefix IDs that the controller resolved for this pool.
	// +optional
	ResolvedPrefixes []int32 `json:"resolvedPrefixes,omitempty"`
	// Conditions reports the current reconciliation state of the pool.
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
