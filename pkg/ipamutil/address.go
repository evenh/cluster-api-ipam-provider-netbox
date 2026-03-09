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

package ipamutil

import (
	stderrors "errors"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ipamv1 "sigs.k8s.io/cluster-api/api/ipam/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func NewIPAddress(claim *ipamv1.IPAddressClaim, pool client.Object) ipamv1.IPAddress {
	poolGVK := pool.GetObjectKind().GroupVersionKind()

	return ipamv1.IPAddress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      claim.Name,
			Namespace: claim.Namespace,
		},
		Spec: ipamv1.IPAddressSpec{
			ClaimRef: ipamv1.IPAddressClaimReference{
				Name: claim.Name,
			},
			PoolRef: ipamv1.IPPoolReference{
				APIGroup: poolGVK.Group,
				Kind:     poolGVK.Kind,
				Name:     pool.GetName(),
			},
		},
	}
}

func ensureIPAddressOwnerReferences(
	scheme *runtime.Scheme,
	address *ipamv1.IPAddress,
	claim *ipamv1.IPAddressClaim,
	pool client.Object,
) error {
	if err := controllerutil.SetControllerReference(claim, address, scheme); err != nil {
		var alreadyOwnedErr *controllerutil.AlreadyOwnedError
		if !stderrors.As(err, &alreadyOwnedErr) {
			return errors.Wrap(err, "failed to update address claim owner reference")
		}
	}

	if err := controllerutil.SetOwnerReference(pool, address, scheme); err != nil {
		return errors.Wrap(err, "failed to update address pool owner reference")
	}

	var poolRefIdx int
	poolGVK := pool.GetObjectKind().GroupVersionKind()
	for i, ownerRef := range address.GetOwnerReferences() {
		if ownerRef.APIVersion == poolGVK.GroupVersion().String() &&
			ownerRef.Kind == poolGVK.Kind &&
			ownerRef.Name == pool.GetName() {
			poolRefIdx = i
		}
	}

	isController := false
	blockOwnerDeletion := true
	address.OwnerReferences[poolRefIdx].Controller = &isController
	address.OwnerReferences[poolRefIdx].BlockOwnerDeletion = &blockOwnerDeletion
	return nil
}
