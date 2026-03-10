package v1alpha1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v4"
)

func TestGeneratedCRDsDocumentFieldsAndExposeCELValidation(t *testing.T) {
	t.Run("namespaced pool schema documents important fields", func(t *testing.T) {
		crd := loadCRD(t, "ipam.cluster.x-k8s.io_netboxippools.yaml")

		clusterNameDescription := lookupString(
			t,
			crd,
			"spec",
			"versions",
			"0",
			"schema",
			"openAPIV3Schema",
			"properties",
			"spec",
			"properties",
			"clusterName",
			"description",
		)
		if !strings.Contains(clusterNameDescription, "clusterctl move") {
			t.Fatalf("clusterName description = %q, want clusterctl move guidance", clusterNameDescription)
		}

		prefixesMinItems := lookupInt(
			t,
			crd,
			"spec",
			"versions",
			"0",
			"schema",
			"openAPIV3Schema",
			"properties",
			"spec",
			"properties",
			"prefixes",
			"minItems",
		)
		if prefixesMinItems != 1 {
			t.Fatalf("prefixes.minItems = %d, want 1", prefixesMinItems)
		}

		validations := lookupSlice(
			t,
			crd,
			"spec",
			"versions",
			"0",
			"schema",
			"openAPIV3Schema",
			"properties",
			"spec",
			"properties",
			"prefixes",
			"items",
			"x-kubernetes-validations",
		)
		assertValidationRule(t, validations, "has(self.id) != has(self.cidr)")
		assertValidationRule(t, validations, "!has(self.vrfID) || has(self.cidr)")
	})

	t.Run("global pool schema requires a secret namespace", func(t *testing.T) {
		crd := loadCRD(t, "ipam.cluster.x-k8s.io_globalnetboxippools.yaml")
		validations := lookupSlice(
			t,
			crd,
			"spec",
			"versions",
			"0",
			"schema",
			"openAPIV3Schema",
			"properties",
			"spec",
			"x-kubernetes-validations",
		)
		assertValidationRule(t, validations, "size(self.connectionSecretRef.namespace) > 0")
	})
}

func loadCRD(t *testing.T, filename string) map[string]any {
	t.Helper()

	path := filepath.Join("..", "..", "config", "crd", "bases", filename)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	var out map[string]any
	if unmarshalErr := yaml.Unmarshal(data, &out); unmarshalErr != nil {
		t.Fatalf("unmarshal %s: %v", path, unmarshalErr)
	}
	return out
}

func lookupString(t *testing.T, value any, path ...string) string {
	t.Helper()
	raw := lookupValue(t, value, path...)
	str, ok := raw.(string)
	if !ok {
		t.Fatalf("path %v = %#v, want string", path, raw)
	}
	return str
}

func lookupInt(t *testing.T, value any, path ...string) int {
	t.Helper()
	raw := lookupValue(t, value, path...)
	switch v := raw.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		t.Fatalf("path %v = %#v, want int", path, raw)
		return 0
	}
}

func lookupSlice(t *testing.T, value any, path ...string) []any {
	t.Helper()
	raw := lookupValue(t, value, path...)
	slice, ok := raw.([]any)
	if !ok {
		t.Fatalf("path %v = %#v, want slice", path, raw)
	}
	return slice
}

func lookupValue(t *testing.T, value any, path ...string) any {
	t.Helper()
	current := value
	for _, segment := range path {
		switch node := current.(type) {
		case map[string]any:
			var ok bool
			current, ok = node[segment]
			if !ok {
				t.Fatalf("path %v missing segment %q", path, segment)
			}
		case []any:
			index, ok := tryIndex(segment)
			if !ok {
				t.Fatalf("path %v segment %q is not a valid slice index", path, segment)
			}
			if index < 0 || index >= len(node) {
				t.Fatalf("path %v index %d out of range", path, index)
			}
			current = node[index]
		default:
			t.Fatalf("path %v hit unsupported node %#v at segment %q", path, current, segment)
		}
	}
	return current
}

func tryIndex(segment string) (int, bool) {
	switch segment {
	case "0", "1", "2", "3", "4", "5":
		return int(segment[0] - '0'), true
	default:
		return 0, false
	}
}

func assertValidationRule(t *testing.T, validations []any, wantRule string) {
	t.Helper()
	for _, item := range validations {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if rule, _ := entry["rule"].(string); rule == wantRule {
			return
		}
	}
	t.Fatalf("validation rule %q not found in %#v", wantRule, validations)
}
