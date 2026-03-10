package ipamutil

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	ipamv1 "sigs.k8s.io/cluster-api/api/ipam/v1beta2"
)

func TestRecordClaimEvent(t *testing.T) {
	claim := &ipamv1.IPAddressClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "claim",
			Namespace: "default",
		},
	}

	t.Run("records address allocated events", func(t *testing.T) {
		recorder := events.NewFakeRecorder(1)

		recordClaimEvent(
			recorder,
			claim,
			reasonAddressAllocated,
			"AllocateAddress",
			"Allocated IP address 10.0.0.5",
		)

		event := <-recorder.Events
		if !strings.Contains(event, "Normal") ||
			!strings.Contains(event, reasonAddressAllocated) ||
			!strings.Contains(event, "10.0.0.5") {
			t.Fatalf("unexpected event: %q", event)
		}
	})

	t.Run("records address released events", func(t *testing.T) {
		recorder := events.NewFakeRecorder(1)

		recordClaimEvent(
			recorder,
			claim,
			reasonAddressReleased,
			"ReleaseAddress",
			"Released IP address 10.0.0.5",
		)

		event := <-recorder.Events
		if !strings.Contains(event, "Normal") ||
			!strings.Contains(event, reasonAddressReleased) ||
			!strings.Contains(event, "10.0.0.5") {
			t.Fatalf("unexpected event: %q", event)
		}
	})
}
