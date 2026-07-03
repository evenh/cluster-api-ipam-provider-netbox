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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	ipamv1alpha1 "github.com/evenh/cluster-api-ipam-provider-netbox/api/v1alpha1"
	"github.com/evenh/cluster-api-ipam-provider-netbox/internal/version"
)

const addressPartsCount = 2
const customFieldsKey = "custom_fields"
const v2TokenPrefix = "nbt_"

type Client interface {
	ResolvePrefixIDs(ctx context.Context, refs []ipamv1alpha1.NetBoxPrefixReference) ([]int32, error)
	EnsureIPAddressCustomField(ctx context.Context, fieldName string) error
	AllocateIPAddress(ctx context.Context, prefixID int32, request AllocationRequest) (*AllocatedAddress, error)
	FindIPAddressByClaimUID(ctx context.Context, ownershipTag, fieldName, claimUID string) (*AllocatedAddress, error)
	FindIPAddressByAddress(ctx context.Context, ownershipTag, address string) (*AllocatedAddress, error)
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
	baseURL    string
	token      string
	httpClient *http.Client
}

func AuthorizationHeaderValue(token string) string {
	if strings.HasPrefix(token, v2TokenPrefix) && strings.Contains(token, ".") {
		return "Bearer " + token
	}
	return "Token " + token
}

func ComposeV2Token(key, secret string) string {
	return v2TokenPrefix + key + "." + secret
}

type netBoxListResponse[T any] struct {
	Count   int    `json:"count"`
	Next    string `json:"next"`
	Results []T    `json:"results"`
}

type netBoxTag struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type netBoxPrefix struct {
	ID     int32  `json:"id"`
	Prefix string `json:"prefix"`
}

type netBoxIPAddress struct {
	ID           int32          `json:"id"`
	Address      string         `json:"address"`
	DNSName      string         `json:"dns_name"`
	Status       *netBoxStatus  `json:"status,omitempty"`
	Tags         []netBoxTag    `json:"tags,omitempty"`
	CustomFields map[string]any `json:"custom_fields,omitempty"`
}

type netBoxStatus struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type netBoxCustomField struct {
	Name string `json:"name"`
}

type apiError struct {
	statusCode int
	status     string
	body       string
}

func (e *apiError) Error() string {
	body := strings.TrimSpace(e.body)
	if body == "" {
		return e.status
	}
	return fmt.Sprintf("%s: %s", e.status, body)
}

// SanitizedError strips NetBox's raw HTTP response body from err. Use it before surfacing
// an error to a lower-privileged consumer than the NetBox connection Secret, such as an
// IPAddressClaim status condition: apiError.Error() otherwise includes the full response
// body, which may contain internal NetBox details the claim creator has no other access to.
// The caller should log the original, unsanitized error before calling this.
func SanitizedError(err error) error {
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		return fmt.Errorf("NetBox request failed: %s", apiErr.status)
	}
	return err
}

func NewClient(cfg ConnectionConfig) (Client, error) {
	httpClient, err := NewHTTPClient(cfg)
	if err != nil {
		return nil, err
	}
	if _, _, splitErr := SplitBaseURL(cfg.BaseURL); splitErr != nil {
		return nil, splitErr
	}

	return &APIClient{
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		token:      cfg.Token,
		httpClient: httpClient,
	}, nil
}

func (c *APIClient) ResolvePrefixIDs(ctx context.Context, refs []ipamv1alpha1.NetBoxPrefixReference) ([]int32, error) {
	ids := make([]int32, 0, len(refs))
	for _, ref := range refs {
		switch {
		case ref.ID != nil:
			ids = append(ids, *ref.ID)
		case ref.CIDR != "":
			query := url.Values{}
			query.Set("prefix", ref.CIDR)
			if ref.VRFID != nil {
				query.Set("vrf_id", strconv.FormatInt(int64(*ref.VRFID), 10))
			}

			results, err := listAll[netBoxPrefix](ctx, c, "/api/ipam/prefixes/", query)
			if err != nil {
				return nil, fmt.Errorf("list prefixes for %q: %w", ref.CIDR, err)
			}
			switch len(results) {
			case 0:
				return nil, fmt.Errorf("no NetBox prefix matches %q", ref.CIDR)
			case 1:
				ids = append(ids, results[0].ID)
			default:
				return nil, fmt.Errorf("prefix reference %q is ambiguous", ref.CIDR)
			}
		default:
			return nil, errors.New("prefix reference must include id or cidr")
		}
	}
	return ids, nil
}

func (c *APIClient) EnsureIPAddressCustomField(ctx context.Context, fieldName string) error {
	query := url.Values{}
	query.Set("name", fieldName)
	query.Set("object_type", "ipam.ipaddress")

	results, err := listAll[netBoxCustomField](ctx, c, "/api/extras/custom-fields/", query)
	if err != nil {
		return fmt.Errorf("list custom fields: %w", err)
	}
	if len(results) == 0 {
		return fmt.Errorf("NetBox custom field %q for ipam.ipaddress does not exist", fieldName)
	}
	return nil
}

func (c *APIClient) AllocateIPAddress(
	ctx context.Context,
	prefixID int32,
	req AllocationRequest,
) (*AllocatedAddress, error) {
	tags, err := c.ensureTags(ctx, appendUnique(req.Metadata.Tags, req.OwnershipTag))
	if err != nil {
		return nil, fmt.Errorf("ensure tags: %w", err)
	}

	customFields := make(map[string]any, len(req.Metadata.CustomFields)+1)
	for k, v := range req.Metadata.CustomFields {
		customFields[k] = v
	}
	customFields[req.ClaimUIDFieldName] = req.ClaimUID

	payload := map[string]any{
		"status":        req.Status,
		"description":   req.Description,
		customFieldsKey: customFields,
		"tags":          tags,
	}
	if req.Metadata.DNSName != "" {
		payload["dns_name"] = req.Metadata.DNSName
	}
	if req.Metadata.TenantID != nil {
		payload["tenant"] = *req.Metadata.TenantID
	}
	if req.Metadata.VRFID != nil {
		payload["vrf"] = *req.Metadata.VRFID
	}

	var created []netBoxIPAddress
	if postErr := c.post(
		ctx,
		fmt.Sprintf("/api/ipam/prefixes/%d/available-ips/", prefixID),
		[]map[string]any{payload},
		&created,
		http.StatusOK,
		http.StatusCreated,
	); postErr != nil {
		if isNoAvailableIPError(postErr) {
			return nil, ErrNoAvailableIP
		}
		return nil, fmt.Errorf("allocate available ip from prefix %d: %w", prefixID, postErr)
	}
	if len(created) == 0 {
		return nil, ErrNoAvailableIP
	}

	return mapIPAddress(created[0]), nil
}

func (c *APIClient) FindIPAddressByClaimUID(
	ctx context.Context,
	ownershipTag, fieldName, claimUID string,
) (*AllocatedAddress, error) {
	// NetBox's ?tag= filter validates its value against existing Tag objects and 400s
	// otherwise, so the tag must exist before it can be used to filter, even though this
	// call never intends to create one.
	if _, err := c.ensureTag(ctx, ownershipTag); err != nil {
		return nil, fmt.Errorf("ensure ownership tag: %w", err)
	}

	query := url.Values{}
	query.Set("tag", ownershipTag)

	results, err := listAll[netBoxIPAddress](ctx, c, "/api/ipam/ip-addresses/", query)
	if err != nil {
		return nil, fmt.Errorf("list IP addresses: %w", err)
	}
	for _, item := range results {
		if fmt.Sprint(item.CustomFields[fieldName]) == claimUID {
			return mapIPAddress(item), nil
		}
	}
	return nil, nil
}

func (c *APIClient) FindIPAddressByAddress(
	ctx context.Context,
	ownershipTag, address string,
) (*AllocatedAddress, error) {
	if _, err := c.ensureTag(ctx, ownershipTag); err != nil {
		return nil, fmt.Errorf("ensure ownership tag: %w", err)
	}

	query := url.Values{}
	query.Set("tag", ownershipTag)
	query.Set("address", address)

	results, err := listAll[netBoxIPAddress](ctx, c, "/api/ipam/ip-addresses/", query)
	if err != nil {
		return nil, fmt.Errorf("list IP addresses by address: %w", err)
	}
	for _, item := range results {
		mapped := mapIPAddress(item)
		if strings.TrimSpace(item.Address) == address || strings.TrimSpace(mapped.Address) == address {
			return mapped, nil
		}
	}
	return nil, nil
}

func (c *APIClient) DeleteIPAddress(ctx context.Context, id int32) error {
	err := c.do(
		ctx,
		http.MethodDelete,
		fmt.Sprintf("/api/ipam/ip-addresses/%d/", id),
		nil,
		nil,
		nil,
		http.StatusNoContent,
		http.StatusNotFound,
	)
	if err == nil {
		return nil
	}
	var apiErr *apiError
	if errors.As(err, &apiErr) && apiErr.statusCode == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("delete ip address %d: %w", id, err)
}

var ErrNoAvailableIP = errors.New("no available IP")
var nonSlugCharacters = regexp.MustCompile(`[^a-z0-9_-]+`)

func mapIPAddress(ip netBoxIPAddress) *AllocatedAddress {
	address := strings.TrimSpace(ip.Address)
	prefix := int32(0)
	if parts := strings.SplitN(address, "/", addressPartsCount); len(parts) == addressPartsCount {
		address = parts[0]
		if parsedPrefix, err := parseInt32(parts[1]); err == nil {
			prefix = parsedPrefix
		}
	}
	return &AllocatedAddress{
		ID:      ip.ID,
		Address: address,
		Prefix:  prefix,
		DNSName: ip.DNSName,
	}
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

func (c *APIClient) ensureTags(ctx context.Context, names []string) ([]netBoxTag, error) {
	out := make([]netBoxTag, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}

		tag, err := c.ensureTag(ctx, name)
		if err != nil {
			return nil, err
		}
		out = append(out, tag)
	}
	return out, nil
}

func (c *APIClient) ensureTag(ctx context.Context, name string) (netBoxTag, error) {
	query := url.Values{}
	query.Set("name", name)

	results, err := listAll[netBoxTag](ctx, c, "/api/extras/tags/", query)
	if err != nil {
		return netBoxTag{}, fmt.Errorf("list tag %q: %w", name, err)
	}
	for _, item := range results {
		if item.Name == name {
			return item, nil
		}
	}

	request := netBoxTag{
		Name: name,
		Slug: slugifyTag(name),
	}
	var created netBoxTag
	if err = c.post(ctx, "/api/extras/tags/", request, &created, http.StatusCreated); err != nil {
		return netBoxTag{}, fmt.Errorf("create tag %q: %w", name, err)
	}
	if created.Name == "" {
		return netBoxTag{}, fmt.Errorf("create tag %q: empty response", name)
	}
	return created, nil
}

func slugifyTag(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return "tag"
	}

	var b strings.Builder
	b.Grow(len(value))
	lastHyphen := false
	for _, r := range value {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastHyphen = false
		case r == '_' || r == '-':
			if !lastHyphen && b.Len() > 0 {
				b.WriteRune(r)
				lastHyphen = true
			}
		default:
			if !lastHyphen && b.Len() > 0 {
				b.WriteByte('-')
				lastHyphen = true
			}
		}
	}

	slug := strings.Trim(b.String(), "-_")
	slug = nonSlugCharacters.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-_")
	if slug == "" {
		return "tag"
	}
	return slug
}

func isNoAvailableIPError(err error) bool {
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.statusCode != http.StatusBadRequest && apiErr.statusCode != http.StatusConflict {
		return false
	}

	body := strings.ToLower(apiErr.body)
	return strings.Contains(body, "available ip") || strings.Contains(body, "available addresses")
}

func (c *APIClient) get(ctx context.Context, path string, query url.Values, response any) error {
	return c.do(ctx, http.MethodGet, path, query, nil, response, http.StatusOK)
}

// listAll follows NetBox's paginated "next" links until exhausted, returning every result
// across all pages. NetBox's default page size (50) means any list endpoint can be
// paginated; a plain get() into a netBoxListResponse only ever sees the first page.
func listAll[T any](ctx context.Context, c *APIClient, path string, query url.Values) ([]T, error) {
	var all []T
	nextPath, nextQuery := path, query
	for {
		var page netBoxListResponse[T]
		if err := c.get(ctx, nextPath, nextQuery, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Results...)
		if page.Next == "" {
			return all, nil
		}
		next, err := url.Parse(page.Next)
		if err != nil {
			return nil, fmt.Errorf("parse next page url %q: %w", page.Next, err)
		}
		nextPath, nextQuery = next.Path, next.Query()
	}
}

func (c *APIClient) post(ctx context.Context, path string, request any, response any, expectedStatus ...int) error {
	return c.do(ctx, http.MethodPost, path, nil, request, response, expectedStatus...)
}

func (c *APIClient) do(
	ctx context.Context,
	method, path string,
	query url.Values,
	request any,
	response any,
	expectedStatus ...int,
) error {
	var body io.Reader
	if request != nil {
		payload, err := json.Marshal(request)
		if err != nil {
			return fmt.Errorf("marshal %s %s request: %w", method, path, err)
		}
		body = bytes.NewReader(payload)
	}

	endpoint := c.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}

	httpReq, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("build %s %s request: %w", method, path, err)
	}
	httpReq.Header.Set("Authorization", AuthorizationHeaderValue(c.token))
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", version.UserAgent())
	if request != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return fmt.Errorf("read %s %s response: %w", method, path, err)
	}
	if !containsStatus(expectedStatus, httpResp.StatusCode) {
		return &apiError{
			statusCode: httpResp.StatusCode,
			status:     httpResp.Status,
			body:       string(respBody),
		}
	}
	if response == nil || len(respBody) == 0 {
		return nil
	}
	if unmarshalErr := json.Unmarshal(respBody, response); unmarshalErr != nil {
		return fmt.Errorf("decode %s %s response: %w", method, path, unmarshalErr)
	}
	return nil
}

func containsStatus(statuses []int, status int) bool {
	return slices.Contains(statuses, status)
}
