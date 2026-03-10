package reconcileutil

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type EventRecorder interface {
	RecordNormal(obj runtime.Object, reason, action, message string)
	RecordWarning(obj runtime.Object, reason, action, message string)
}

type ControllerBase struct {
	client.Client

	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
}

func (b ControllerBase) RecordNormal(obj runtime.Object, reason, action, message string) {
	recordEvent(b.Recorder, obj, corev1.EventTypeNormal, reason, action, message)
}

func (b ControllerBase) RecordWarning(obj runtime.Object, reason, action, message string) {
	recordEvent(b.Recorder, obj, corev1.EventTypeWarning, reason, action, message)
}

func recordEvent(
	recorder events.EventRecorder,
	obj runtime.Object,
	eventType, reason, action, message string,
) {
	if recorder == nil || obj == nil {
		return
	}

	recorder.Eventf(obj, nil, eventType, reason, action, message)
}
