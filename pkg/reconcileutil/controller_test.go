package reconcileutil

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	ipamv1 "sigs.k8s.io/cluster-api/api/ipam/v1beta2"
)

func TestControllerBaseRecordsEvents(t *testing.T) {
	claim := &ipamv1.IPAddressClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "claim",
			Namespace: "default",
		},
	}

	t.Run("records normal events", func(t *testing.T) {
		recorder := events.NewFakeRecorder(1)
		base := ControllerBase{Recorder: recorder}

		base.RecordNormal(claim, "AddressAllocated", "AllocateAddress", "Allocated IP address 10.0.0.5")

		event := <-recorder.Events
		if !strings.Contains(event, "Normal") ||
			!strings.Contains(event, "AddressAllocated") ||
			!strings.Contains(event, "10.0.0.5") {
			t.Fatalf("unexpected event: %q", event)
		}
	})

	t.Run("records warning events", func(t *testing.T) {
		recorder := events.NewFakeRecorder(1)
		base := ControllerBase{Recorder: recorder}

		base.RecordWarning(claim, "PoolInUse", "BlockPoolDeletion", "Pool deletion is blocked")

		event := <-recorder.Events
		if !strings.Contains(event, "Warning") ||
			!strings.Contains(event, "PoolInUse") ||
			!strings.Contains(event, "Pool deletion is blocked") {
			t.Fatalf("unexpected event: %q", event)
		}
	})
}
