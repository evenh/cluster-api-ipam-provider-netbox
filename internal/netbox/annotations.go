/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the License.
*/

package netbox

import (
	"encoding/json"
	"fmt"
	"maps"
	"strconv"
	"strings"

	ipamv1 "sigs.k8s.io/cluster-api/api/ipam/v1beta2"

	ipamv1alpha1 "github.com/evenh/cluster-api-ipam-provider-netbox/api/v1alpha1"
)

const (
	AnnotationPrefix              = "ipam.netbox.cluster.x-k8s.io/"
	AnnotationTenantID            = AnnotationPrefix + "tenant-id"
	AnnotationVRFID               = AnnotationPrefix + "vrf-id"
	AnnotationDNSName             = AnnotationPrefix + "dns-name"
	AnnotationTags                = AnnotationPrefix + "tags"
	AnnotationCustomFields        = AnnotationPrefix + "custom-fields"
	DefaultOwnershipTag           = "cluster-api-ipam-provider-netbox"
	DefaultClaimUIDCustomField    = "cluster_api_claim_uid"
	DefaultIPAddressStatus        = "active"
	ClaimUIDDescriptionFieldLabel = "claimUID"
)

type EffectiveMetadata struct {
	TenantID     *int32
	VRFID        *int32
	DNSName      string
	Tags         []string
	CustomFields map[string]string
}

func EffectivePoolMetadata(
	poolDefaults ipamv1alpha1.NetBoxMetadata,
	claim *ipamv1.IPAddressClaim,
) (EffectiveMetadata, error) {
	metadata := EffectiveMetadata{
		TenantID: poolDefaults.TenantID,
		VRFID:    poolDefaults.VRFID,
		DNSName:  poolDefaults.DNSName,
		Tags:     append([]string{}, poolDefaults.Tags...),
	}
	if len(poolDefaults.CustomFields) > 0 {
		metadata.CustomFields = make(map[string]string, len(poolDefaults.CustomFields))
		maps.Copy(metadata.CustomFields, poolDefaults.CustomFields)
	} else {
		metadata.CustomFields = map[string]string{}
	}

	annotations := claim.GetAnnotations()
	if annotations == nil {
		return metadata, nil
	}

	if raw, ok := annotations[AnnotationTenantID]; ok && raw != "" {
		value, err := parseInt32(raw)
		if err != nil {
			return EffectiveMetadata{}, fmt.Errorf("parse %s: %w", AnnotationTenantID, err)
		}
		metadata.TenantID = &value
	}
	if raw, ok := annotations[AnnotationVRFID]; ok && raw != "" {
		value, err := parseInt32(raw)
		if err != nil {
			return EffectiveMetadata{}, fmt.Errorf("parse %s: %w", AnnotationVRFID, err)
		}
		metadata.VRFID = &value
	}
	if raw, ok := annotations[AnnotationDNSName]; ok {
		metadata.DNSName = raw
	}
	if raw, ok := annotations[AnnotationTags]; ok && raw != "" {
		metadata.Tags = splitCSV(raw)
	}
	if raw, ok := annotations[AnnotationCustomFields]; ok && raw != "" {
		values := map[string]string{}
		if err := json.Unmarshal([]byte(raw), &values); err != nil {
			return EffectiveMetadata{}, fmt.Errorf("parse %s: %w", AnnotationCustomFields, err)
		}
		maps.Copy(metadata.CustomFields, values)
	}

	return metadata, nil
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func parseInt32(raw string) (int32, error) {
	value, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		return 0, err
	}
	return int32(value), nil
}
