package netbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"testing"

	ipamv1alpha1 "github.com/evenh/cluster-api-ipam-provider-netbox/api/v1alpha1"
)

func TestAllocateIPAddressEnsuresTags(t *testing.T) {
	tags := map[string]string{
		"pool-default": "pool-default",
	}

	var createdTags []netBoxTag
	var allocateRequests [][]map[string]any
	var userAgents []string

	client := newTestAPIClient(
		"https://netbox.example.com",
		roundTripFunc(func(r *http.Request) (*http.Response, error) {
			userAgents = append(userAgents, r.Header.Get("User-Agent"))

			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/api/extras/tags/":
				name := r.URL.Query().Get("name")
				if slug, ok := tags[name]; ok {
					return jsonResponse(r, http.StatusOK, map[string]any{
						"count": 1,
						"results": []map[string]any{{
							"name": name,
							"slug": slug,
						}},
					}), nil
				}
				return jsonResponse(r, http.StatusOK, map[string]any{
					"count":   0,
					"results": []any{},
				}), nil
			case r.Method == http.MethodPost && r.URL.Path == "/api/extras/tags/":
				var request netBoxTag
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatalf("decode tag request: %v", err)
				}
				createdTags = append(createdTags, request)
				tags[request.Name] = request.Slug
				return jsonResponse(r, http.StatusCreated, request), nil
			case r.Method == http.MethodPost && r.URL.Path == "/api/ipam/prefixes/7/available-ips/":
				var request []map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatalf("decode allocation request: %v", err)
				}
				allocateRequests = append(allocateRequests, request)
				return jsonResponse(r, http.StatusCreated, []map[string]any{{
					"id":       99,
					"address":  "10.0.0.10/24",
					"dns_name": "claimed.example.com",
				}}), nil
			default:
				return jsonResponse(r, http.StatusNotFound, map[string]any{"detail": "not found"}), nil
			}
		}),
	)

	address, err := client.AllocateIPAddress(context.Background(), 7, AllocationRequest{
		Metadata: EffectiveMetadata{
			DNSName: "claimed.example.com",
			Tags:    []string{"pool-default", "claim override"},
			CustomFields: map[string]string{
				"source": "test",
			},
		},
		OwnershipTag:      DefaultOwnershipTag,
		ClaimUIDFieldName: DefaultClaimUIDCustomField,
		ClaimUID:          "claim-uid",
		Description:       "test allocation",
		Status:            "active",
	})
	if err != nil {
		t.Fatalf("AllocateIPAddress() error = %v", err)
	}

	if address == nil || address.Address != "10.0.0.10" || address.DNSName != "claimed.example.com" {
		t.Fatalf("unexpected allocated address: %#v", address)
	}
	if len(createdTags) != 2 {
		t.Fatalf("created %d tags, want 2", len(createdTags))
	}
	if createdTags[0].Name != "claim override" || createdTags[0].Slug != "claim-override" {
		t.Fatalf("unexpected created tag: %#v", createdTags[0])
	}
	if createdTags[1].Name != DefaultOwnershipTag || createdTags[1].Slug != DefaultOwnershipTag {
		t.Fatalf("unexpected ownership tag: %#v", createdTags[1])
	}
	if len(allocateRequests) != 1 || len(allocateRequests[0]) != 1 {
		t.Fatalf("unexpected allocation requests: %#v", allocateRequests)
	}
	for i, agent := range userAgents {
		if agent != UserAgent {
			t.Fatalf("request %d user-agent = %q, want %q", i, agent, UserAgent)
		}
	}

	request := allocateRequests[0][0]
	rawTags, hasTags := request["tags"].([]any)
	if !hasTags || len(rawTags) != 3 {
		t.Fatalf("allocation request tags = %#v, want 3 tags", request["tags"])
	}
	if _, hasAddress := request["address"]; hasAddress {
		t.Fatalf("allocation request unexpectedly included address: %#v", request["address"])
	}

	gotTagRefs := make([]string, 0, len(rawTags))
	for _, rawTag := range rawTags {
		tag, isMap := rawTag.(map[string]any)
		if !isMap {
			t.Fatalf("tag payload = %#v, want object", rawTag)
		}
		name, _ := tag["name"].(string)
		slug, _ := tag["slug"].(string)
		if slug == "" {
			t.Fatalf("tag %#v missing slug", tag)
		}
		gotTagRefs = append(gotTagRefs, name+":"+slug)
	}
	slices.Sort(gotTagRefs)

	wantTagRefs := []string{
		DefaultOwnershipTag + ":" + DefaultOwnershipTag,
		"claim override:claim-override",
		"pool-default:pool-default",
	}
	slices.Sort(wantTagRefs)
	if !slices.Equal(gotTagRefs, wantTagRefs) {
		t.Fatalf("allocation request tags = %#v, want %#v", gotTagRefs, wantTagRefs)
	}
}

func TestResolvePrefixIDs(t *testing.T) {
	client := newTestAPIClient(
		"https://netbox.example.com",
		roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method != http.MethodGet || r.URL.Path != "/api/ipam/prefixes/" {
				return jsonResponse(r, http.StatusNotFound, map[string]any{"detail": "not found"}), nil
			}
			if got := r.Header.Get("User-Agent"); got != UserAgent {
				t.Fatalf("user-agent = %q, want %q", got, UserAgent)
			}
			if r.URL.Query().Get("prefix") != "10.20.0.0/24" {
				t.Fatalf("unexpected prefix query: %q", r.URL.RawQuery)
			}
			if r.URL.Query().Get("vrf_id") != "23" {
				t.Fatalf("unexpected vrf query: %q", r.URL.RawQuery)
			}
			return jsonResponse(r, http.StatusOK, map[string]any{
				"count": 1,
				"results": []map[string]any{{
					"id":     88,
					"prefix": "10.20.0.0/24",
				}},
			}), nil
		}),
	)

	vrfID := int32(23)
	ids, err := client.ResolvePrefixIDs(context.Background(), []ipamv1alpha1.NetBoxPrefixReference{{
		CIDR:  "10.20.0.0/24",
		VRFID: &vrfID,
	}})
	if err != nil {
		t.Fatalf("ResolvePrefixIDs() error = %v", err)
	}
	if !slices.Equal(ids, []int32{88}) {
		t.Fatalf("ResolvePrefixIDs() = %#v, want [88]", ids)
	}
}

func TestEnsureIPAddressCustomField(t *testing.T) {
	client := newTestAPIClient(
		"https://netbox.example.com",
		roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method != http.MethodGet || r.URL.Path != "/api/extras/custom-fields/" {
				return jsonResponse(r, http.StatusNotFound, map[string]any{"detail": "not found"}), nil
			}
			if got := r.Header.Get("User-Agent"); got != UserAgent {
				t.Fatalf("user-agent = %q, want %q", got, UserAgent)
			}
			if r.URL.Query().Get("name") != DefaultClaimUIDCustomField ||
				r.URL.Query().Get("object_type") != "ipam.ipaddress" {
				t.Fatalf("unexpected custom field query: %q", r.URL.RawQuery)
			}
			return jsonResponse(r, http.StatusOK, map[string]any{
				"count": 1,
				"results": []map[string]any{{
					"name": DefaultClaimUIDCustomField,
				}},
			}), nil
		}),
	)

	if err := client.EnsureIPAddressCustomField(context.Background(), DefaultClaimUIDCustomField); err != nil {
		t.Fatalf("EnsureIPAddressCustomField() error = %v", err)
	}
}

func TestAuthorizationHeaderValue(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		token string
		want  string
	}{
		{
			name:  "v1 token",
			token: "0123456789abcdef0123456789abcdef01234567",
			want:  "Token 0123456789abcdef0123456789abcdef01234567",
		},
		{
			name:  "v2 token",
			token: ComposeV2Token("ABC12345", "secret-token-value"),
			want:  "Bearer nbt_ABC12345.secret-token-value",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := AuthorizationHeaderValue(tc.token); got != tc.want {
				t.Fatalf("AuthorizationHeaderValue() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestToNestedTagsIncludesSlug(t *testing.T) {
	got := toNestedTags([]string{"claim override", "pool-default"})
	gotRefs := make([]string, 0, len(got))
	for _, tag := range got {
		gotRefs = append(gotRefs, tag.Name+":"+tag.Slug)
	}
	wantRefs := []string{
		"claim override:claim-override",
		"pool-default:pool-default",
	}
	if !slices.Equal(gotRefs, wantRefs) {
		t.Fatalf("toNestedTags() = %#v, want %#v", gotRefs, wantRefs)
	}
}

func newTestAPIClient(baseURL string, transport http.RoundTripper) *APIClient {
	return &APIClient{
		baseURL:    baseURL,
		token:      "token",
		httpClient: &http.Client{Transport: transport},
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func jsonResponse(req *http.Request, statusCode int, payload any) *http.Response {
	body, err := json.Marshal(payload)
	if err != nil {
		panic(fmt.Sprintf("marshal payload: %v", err))
	}

	return &http.Response{
		StatusCode: statusCode,
		Status:     fmt.Sprintf("%d %s", statusCode, http.StatusText(statusCode)),
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body:    io.NopCloser(bytes.NewReader(body)),
		Request: req,
	}
}
