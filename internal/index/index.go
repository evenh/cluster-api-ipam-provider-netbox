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

package index

import (
	"context"
	"fmt"

	ipamv1 "sigs.k8s.io/cluster-api/api/ipam/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

const (
	IPAddressPoolRefCombinedField      = "index.poolRef"
	IPAddressClaimPoolRefCombinedField = "index.poolRef"
)

func SetupIndexes(ctx context.Context, mgr manager.Manager) error {
	if err := mgr.GetCache().
		IndexField(ctx, &ipamv1.IPAddress{}, IPAddressPoolRefCombinedField, IPAddressByCombinedPoolRef); err != nil {
		return err
	}
	return mgr.GetCache().
		IndexField(ctx, &ipamv1.IPAddressClaim{}, IPAddressClaimPoolRefCombinedField, ipAddressClaimByCombinedPoolRef)
}

func IPAddressByCombinedPoolRef(obj client.Object) []string {
	ip, ok := obj.(*ipamv1.IPAddress)
	if !ok {
		panic(fmt.Sprintf("expected IPAddress but got %T", obj))
	}
	return []string{IPPoolRefValue(ip.Spec.PoolRef)}
}

func ipAddressClaimByCombinedPoolRef(obj client.Object) []string {
	claim, ok := obj.(*ipamv1.IPAddressClaim)
	if !ok {
		panic(fmt.Sprintf("expected IPAddressClaim but got %T", obj))
	}
	return []string{IPPoolRefValue(claim.Spec.PoolRef)}
}

func IPPoolRefValue(ref ipamv1.IPPoolReference) string {
	return fmt.Sprintf("%s/%s", ref.Kind, ref.Name)
}
