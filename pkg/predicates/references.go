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

package predicates

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ipamv1 "sigs.k8s.io/cluster-api/api/ipam/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

func ClaimReferencesPoolKind(gk metav1.GroupKind) predicate.Funcs {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return claimReferencesPoolKind(gk, e.Object)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return claimReferencesPoolKind(gk, e.Object)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return claimReferencesPoolKind(gk, e.ObjectNew)
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return claimReferencesPoolKind(gk, e.Object)
		},
	}
}

func AddressReferencesPoolKind(gk metav1.GroupKind) predicate.Funcs {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return addressReferencesPoolKind(gk, e.Object)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return addressReferencesPoolKind(gk, e.Object)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return addressReferencesPoolKind(gk, e.ObjectNew)
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return addressReferencesPoolKind(gk, e.Object)
		},
	}
}

func claimReferencesPoolKind(gk metav1.GroupKind, obj client.Object) bool {
	claim, ok := obj.(*ipamv1.IPAddressClaim)
	if !ok {
		return false
	}
	return claim.Spec.PoolRef.Kind == gk.Kind && claim.Spec.PoolRef.APIGroup == gk.Group
}

func addressReferencesPoolKind(gk metav1.GroupKind, obj client.Object) bool {
	address, ok := obj.(*ipamv1.IPAddress)
	if !ok {
		return false
	}
	return address.Spec.PoolRef.Kind == gk.Kind && address.Spec.PoolRef.APIGroup == gk.Group
}
