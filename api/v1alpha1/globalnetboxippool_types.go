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
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Allocated",type="integer",JSONPath=".status.addresses.allocated"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// GlobalNetBoxIPPool is the Schema for the globalnetboxippools API.
type GlobalNetBoxIPPool struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec GlobalNetBoxIPPoolSpec `json:"spec"`
	Status NetBoxIPPoolStatus `json:"status,omitempty"`
}

// GlobalNetBoxIPPoolSpec defines the desired state of GlobalNetBoxIPPool.
type GlobalNetBoxIPPoolSpec struct {
	NetBoxIPPoolSpec `json:",inline"`
}

// +kubebuilder:object:root=true

// GlobalNetBoxIPPoolList contains a list of GlobalNetBoxIPPool.
type GlobalNetBoxIPPoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GlobalNetBoxIPPool `json:"items"`
}

func (p *GlobalNetBoxIPPool) PoolSpec() *NetBoxIPPoolSpec {
	return &p.Spec.NetBoxIPPoolSpec
}

func (p *GlobalNetBoxIPPool) PoolStatus() *NetBoxIPPoolStatus {
	return &p.Status
}

func init() {
	SchemeBuilder.Register(&GlobalNetBoxIPPool{}, &GlobalNetBoxIPPoolList{})
}
