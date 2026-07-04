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

import "testing"

func TestResolveGatewayPrecedence(t *testing.T) {
	testCases := []struct {
		name            string
		prefixCIDR      string
		customField     string
		perPrefixStatic string
		poolStatic      string
		wantGateway     string
		wantInRange     bool
		wantErr         bool
	}{
		{
			name:            "custom field wins over both statics",
			prefixCIDR:      "10.0.0.0/24",
			customField:     "10.0.0.1",
			perPrefixStatic: "10.0.0.2",
			poolStatic:      "10.0.0.3",
			wantGateway:     "10.0.0.1",
			wantInRange:     true,
		},
		{
			name:            "per-prefix static wins over pool static when custom field empty",
			prefixCIDR:      "10.0.0.0/24",
			perPrefixStatic: "10.0.0.2",
			poolStatic:      "10.0.0.3",
			wantGateway:     "10.0.0.2",
			wantInRange:     true,
		},
		{
			name:        "pool static used as last resort",
			prefixCIDR:  "10.0.0.0/24",
			poolStatic:  "10.0.0.3",
			wantGateway: "10.0.0.3",
			wantInRange: true,
		},
		{
			name:        "no candidate yields empty gateway, no error",
			prefixCIDR:  "10.0.0.0/24",
			wantGateway: "",
			wantInRange: false,
		},
		{
			name:        "off-subnet gateway is valid but not in range",
			prefixCIDR:  "10.0.0.0/24",
			poolStatic:  "10.9.9.1",
			wantGateway: "10.9.9.1",
			wantInRange: false,
		},
		{
			name:        "whitespace-only candidates are ignored",
			prefixCIDR:  "10.0.0.0/24",
			customField: "   ",
			poolStatic:  "  10.0.0.3 ",
			wantGateway: "10.0.0.3",
			wantInRange: true,
		},
		{
			name:       "invalid IP is rejected",
			prefixCIDR: "10.0.0.0/24",
			poolStatic: "not-an-ip",
			wantErr:    true,
		},
		{
			name:        "ipv6 gateway on ipv6 prefix",
			prefixCIDR:  "2001:db8::/64",
			customField: "2001:db8::1",
			wantGateway: "2001:db8::1",
			wantInRange: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gw, inRange, err := ResolveGateway(tc.prefixCIDR, tc.customField, tc.perPrefixStatic, tc.poolStatic)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ResolveGateway() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveGateway() error = %v", err)
			}
			if gw != tc.wantGateway {
				t.Fatalf("ResolveGateway() gateway = %q, want %q", gw, tc.wantGateway)
			}
			if inRange != tc.wantInRange {
				t.Fatalf("ResolveGateway() inRange = %v, want %v", inRange, tc.wantInRange)
			}
		})
	}
}

func TestResolveGatewayFamilyMismatch(t *testing.T) {
	if _, _, err := ResolveGateway("2001:db8::/64", "10.0.0.1", "", ""); err == nil {
		t.Fatal("expected error putting a v4 gateway on a v6 prefix")
	}
	if _, _, err := ResolveGateway("10.0.0.0/24", "2001:db8::1", "", ""); err == nil {
		t.Fatal("expected error putting a v6 gateway on a v4 prefix")
	}
}

func TestCustomFieldString(t *testing.T) {
	cf := map[string]any{
		"gateway": "10.0.0.1",
		"empty":   nil,
		"number":  float64(42),
		"spaces":  "  10.0.0.2  ",
	}
	if got := CustomFieldString(cf, "gateway"); got != "10.0.0.1" {
		t.Fatalf("CustomFieldString(gateway) = %q", got)
	}
	if got := CustomFieldString(cf, "empty"); got != "" {
		t.Fatalf("CustomFieldString(empty) = %q, want empty", got)
	}
	if got := CustomFieldString(cf, "missing"); got != "" {
		t.Fatalf("CustomFieldString(missing) = %q, want empty", got)
	}
	if got := CustomFieldString(cf, "spaces"); got != "10.0.0.2" {
		t.Fatalf("CustomFieldString(spaces) = %q, want trimmed", got)
	}
	if got := CustomFieldString(nil, "gateway"); got != "" {
		t.Fatalf("CustomFieldString(nil map) = %q, want empty", got)
	}
}
