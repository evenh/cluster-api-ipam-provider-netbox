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
	"fmt"
	"net/netip"
	"strings"
)

// ResolveGateway picks the gateway for a prefix using the precedence
// customFieldValue > perPrefixStatic > poolStatic, validates it against the prefix, and reports
// whether it falls inside the prefix's address range.
//
// It returns ("", false, nil) when none of the candidates is set — the default, gateway-less
// behaviour. It returns an error when the chosen gateway is not a valid IP address or belongs to a
// different address family than the prefix (so a v4 gateway is never written onto a v6 address);
// callers surface that as a not-ready pool. The returned gateway is normalised via netip.
//
// inRange reports whether the gateway lies within the prefix, i.e. NetBox could later hand it out
// as an ordinary host address unless it is reserved. That is the common case for a real gateway
// (e.g. 10.0.0.1 in 10.0.0.0/24), so it is advisory only: callers warn rather than reject.
func ResolveGateway(prefixCIDR, customFieldValue, perPrefixStatic, poolStatic string) (string, bool, error) {
	gateway := firstNonEmpty(customFieldValue, perPrefixStatic, poolStatic)
	if gateway == "" {
		return "", false, nil
	}

	addr, err := netip.ParseAddr(gateway)
	if err != nil {
		return "", false, fmt.Errorf("gateway %q is not a valid IP address: %w", gateway, err)
	}

	prefix, err := netip.ParsePrefix(strings.TrimSpace(prefixCIDR))
	if err != nil {
		return "", false, fmt.Errorf("prefix %q is not valid CIDR: %w", prefixCIDR, err)
	}

	if addr.Is4() != prefix.Addr().Is4() {
		return "", false, fmt.Errorf("gateway %q address family does not match prefix %q", gateway, prefixCIDR)
	}

	return addr.String(), prefix.Contains(addr), nil
}

// CustomFieldString reads a NetBox custom field value as a trimmed string, tolerating an absent key
// or a JSON null (both yield ""). NetBox returns custom field values as heterogeneous JSON, so a
// text field arrives as a string while an unset field arrives as nil.
func CustomFieldString(customFields map[string]any, name string) string {
	if name == "" || customFields == nil {
		return ""
	}
	value, ok := customFields[name]
	if !ok || value == nil {
		return ""
	}
	if s, isString := value.(string); isString {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
