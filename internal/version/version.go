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

// Package version reports the version this binary was built from.
package version

import (
	"fmt"
	"runtime"
)

// gitVersion is stamped at build time via -ldflags "-X .../internal/version.gitVersion=...".
// It stays "dev" for local `go build`/`go run` invocations that don't set it.
//
//nolint:gochecknoglobals // only way to inject a build-time value into a compiled Go binary.
var gitVersion = "dev"

// Get returns the version this binary was built from.
func Get() string {
	return gitVersion
}

// UserAgent returns the HTTP User-Agent this process identifies itself with, following the
// "product/version (comment)" convention from RFC 9110 section 10.1.5.
func UserAgent() string {
	return fmt.Sprintf(
		"cluster-api-ipam-provider-netbox/%s (%s/%s) %s",
		gitVersion,
		runtime.GOOS,
		runtime.GOARCH,
		runtime.Version(),
	)
}
