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

package controller

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	incidentsv1alpha1 "github.com/yih0nk/hivemind/api/v1alpha1"
)

// patchStatus applies mutate to triage's status, then patches only the
// status subresource with a JSON merge patch diffed against the
// pre-mutation copy. Custom resources do not support strategic merge
// patch, so MergeFrom (RFC 7386 merge patch) is the correct primitive here.
func (r *IncidentTriageReconciler) patchStatus(ctx context.Context, triage *incidentsv1alpha1.IncidentTriage, mutate func(*incidentsv1alpha1.IncidentTriageStatus)) error {
	patch := client.MergeFrom(triage.DeepCopy())
	mutate(&triage.Status)
	return r.Status().Patch(ctx, triage, patch)
}

// emitEvent records a Kubernetes Event attached to the triage object,
// visible in `kubectl describe incidenttriage`. eventType is
// corev1.EventTypeNormal or corev1.EventTypeWarning. The reason doubles
// as the event's action field, which the events API requires.
func (r *IncidentTriageReconciler) emitEvent(triage *incidentsv1alpha1.IncidentTriage, eventType, reason, message string) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Eventf(triage, nil, eventType, reason, reason, "%s", message)
}
