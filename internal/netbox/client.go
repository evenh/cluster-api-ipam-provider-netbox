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

package netbox

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	netboxv4 "github.com/netbox-community/go-netbox/v4"

	ipamv1alpha1 "github.com/evenh/cluster-api-ipam-provider-netbox/api/v1alpha1"
)

type Client interface {
	ResolvePrefixIDs(ctx context.Context, refs []ipamv1alpha1.NetBoxPrefixReference) ([]int32, error)
	EnsureIPAddressCustomField(ctx context.Context, fieldName string) error
	AllocateIPAddress(ctx context.Context, prefixID int32, request AllocationRequest) (*AllocatedAddress, error)
	FindIPAddressByClaimUID(ctx context.Context, ownershipTag, fieldName, claimUID string) (*AllocatedAddress, error)
	DeleteIPAddress(ctx context.Context, id int32) error
}

type AllocationRequest struct {
	Metadata          EffectiveMetadata
	OwnershipTag      string
	ClaimUIDFieldName string
	ClaimUID          string
	Description       string
	Status            string
}

type AllocatedAddress struct {
	ID      int32
	Address string
	Prefix  int32
	DNSName string
}

type APIClient struct {
	client *netboxv4.APIClient
}

func NewClient(cfg ConnectionConfig) (Client, error) {
	httpClient, err := NewHTTPClient(cfg)
	if err != nil {
		return nil, err
	}
	scheme, host, err := SplitBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}

	apiCfg := netboxv4.NewConfiguration()
	apiCfg.Scheme = scheme
	apiCfg.Host = host
	apiCfg.DefaultHeader["Authorization"] = fmt.Sprintf("Token %s", cfg.Token)
	apiCfg.HTTPClient = httpClient

	return &APIClient{client: netboxv4.NewAPIClient(apiCfg)}, nil
}

func (c *APIClient) ResolvePrefixIDs(ctx context.Context, refs []ipamv1alpha1.NetBoxPrefixReference) ([]int32, error) {
	ids := make([]int32, 0, len(refs))
	for _, ref := range refs {
		switch {
		case ref.ID != nil:
			ids = append(ids, *ref.ID)
		case ref.CIDR != "":
			req := c.client.IpamAPI.IpamPrefixesList(ctx).Prefix([]string{ref.CIDR})
			if ref.VRFID != nil {
				req = req.VrfId([]*int32{ref.VRFID})
			}
			result, resp, err := req.Execute()
			if err != nil {
				return nil, fmt.Errorf("list prefixes for %q: %w", ref.CIDR, err)
			}
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
			if result == nil || result.Count == 0 {
				return nil, fmt.Errorf("no NetBox prefix matches %q", ref.CIDR)
			}
			if result.Count != 1 {
				return nil, fmt.Errorf("prefix reference %q is ambiguous", ref.CIDR)
			}
			ids = append(ids, result.Results[0].Id)
		default:
			return nil, fmt.Errorf("prefix reference must include id or cidr")
		}
	}
	return ids, nil
}

func (c *APIClient) EnsureIPAddressCustomField(ctx context.Context, fieldName string) error {
	result, resp, err := c.client.ExtrasAPI.ExtrasCustomFieldsList(ctx).
		Name([]string{fieldName}).
		ObjectType("ipam.ipaddress").
		Execute()
	if err != nil {
		return fmt.Errorf("list custom fields: %w", err)
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if result == nil || result.Count == 0 {
		return fmt.Errorf("NetBox custom field %q for ipam.ipaddress does not exist", fieldName)
	}
	return nil
}

func (c *APIClient) AllocateIPAddress(ctx context.Context, prefixID int32, req AllocationRequest) (*AllocatedAddress, error) {
	request := netboxv4.NewIPAddressRequestWithDefaults()
	request.SetStatus(netboxv4.IPAddressStatusValue(req.Status))
	request.SetDescription(req.Description)
	if req.Metadata.DNSName != "" {
		request.SetDnsName(req.Metadata.DNSName)
	}

	customFields := make(map[string]interface{}, len(req.Metadata.CustomFields)+1)
	for k, v := range req.Metadata.CustomFields {
		customFields[k] = v
	}
	customFields[req.ClaimUIDFieldName] = req.ClaimUID
	request.CustomFields = customFields
	request.Tags = toNestedTags(appendUnique(req.Metadata.Tags, req.OwnershipTag))

	if req.Metadata.TenantID != nil {
		request.SetTenant(netboxv4.Int32AsASNRangeRequestTenant(req.Metadata.TenantID))
	}
	if req.Metadata.VRFID != nil {
		request.SetVrf(netboxv4.Int32AsIPAddressRequestVrf(req.Metadata.VRFID))
	}

	created, resp, err := c.client.IpamAPI.IpamPrefixesAvailableIpsCreate(ctx, prefixID).
		IPAddressRequest([]netboxv4.IPAddressRequest{*request}).
		Execute()
	if err != nil {
		return nil, fmt.Errorf("allocate available ip from prefix %d: %w", prefixID, err)
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if len(created) == 0 {
		return nil, ErrNoAvailableIP
	}

	return mapIPAddress(&created[0]), nil
}

func (c *APIClient) FindIPAddressByClaimUID(ctx context.Context, ownershipTag, fieldName, claimUID string) (*AllocatedAddress, error) {
	req := c.client.IpamAPI.IpamIpAddressesList(ctx)
	req = req.Tag([]string{ownershipTag})
	result, resp, err := req.Execute()
	if err != nil {
		return nil, fmt.Errorf("list IP addresses: %w", err)
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if result == nil {
		return nil, nil
	}
	for _, item := range result.Results {
		if item.CustomFields == nil {
			continue
		}
		raw, ok := item.CustomFields[fieldName]
		if !ok {
			continue
		}
		if fmt.Sprint(raw) == claimUID {
			address := mapIPAddress(&item)
			return address, nil
		}
	}
	return nil, nil
}

func (c *APIClient) DeleteIPAddress(ctx context.Context, id int32) error {
	resp, err := c.client.IpamAPI.IpamIpAddressesDestroy(ctx, id).Execute()
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		return nil
	}
	if resp != nil && resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("delete ip address %d: %w", id, err)
}

var ErrNoAvailableIP = errors.New("no available IP")

func mapIPAddress(ip *netboxv4.IPAddress) *AllocatedAddress {
	if ip == nil {
		return nil
	}
	address := strings.TrimSpace(ip.Address)
	prefix := int32(0)
	if parts := strings.SplitN(address, "/", 2); len(parts) == 2 {
		address = parts[0]
		if parsedPrefix, err := parseInt32(parts[1]); err == nil {
			prefix = parsedPrefix
		}
	}
	allocated := &AllocatedAddress{
		ID:      ip.Id,
		Address: address,
		Prefix:  prefix,
	}
	if ip.DnsName != nil {
		allocated.DNSName = *ip.DnsName
	}
	return allocated
}

func appendUnique(values []string, extras ...string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values)+len(extras))
	for _, value := range append(values, extras...) {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func toNestedTags(tags []string) []netboxv4.NestedTagRequest {
	out := make([]netboxv4.NestedTagRequest, 0, len(tags))
	for _, tag := range tags {
		out = append(out, netboxv4.NestedTagRequest{Name: tag})
	}
	return out
}
