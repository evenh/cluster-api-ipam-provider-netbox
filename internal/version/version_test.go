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

package version

import (
	"fmt"
	"runtime"
	"testing"
)

func TestGetReturnsGitVersion(t *testing.T) {
	original := gitVersion
	t.Cleanup(func() { gitVersion = original })

	gitVersion = "v1.2.3"
	if got := Get(); got != "v1.2.3" {
		t.Fatalf("Get() = %q, want %q", got, "v1.2.3")
	}
}

func TestUserAgentFollowsProductVersionCommentConvention(t *testing.T) {
	original := gitVersion
	t.Cleanup(func() { gitVersion = original })

	gitVersion = "v1.2.3"
	want := fmt.Sprintf(
		"cluster-api-ipam-provider-netbox/v1.2.3 (%s/%s) %s",
		runtime.GOOS,
		runtime.GOARCH,
		runtime.Version(),
	)
	if got := UserAgent(); got != want {
		t.Fatalf("UserAgent() = %q, want %q", got, want)
	}
}
