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

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Allocated",type="integer",JSONPath=".status.addresses.allocated"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// NetBoxIPPool is the Schema for the netboxippools API.
type NetBoxIPPool struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec NetBoxIPPoolSpec `json:"spec"`
	Status NetBoxIPPoolStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NetBoxIPPoolList contains a list of NetBoxIPPool.
type NetBoxIPPoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxIPPool `json:"items"`
}

func (p *NetBoxIPPool) PoolSpec() *NetBoxIPPoolSpec {
	return &p.Spec
}

func (p *NetBoxIPPool) PoolStatus() *NetBoxIPPoolStatus {
	return &p.Status
}

func init() {
	SchemeBuilder.Register(&NetBoxIPPool{}, &NetBoxIPPoolList{})
}
