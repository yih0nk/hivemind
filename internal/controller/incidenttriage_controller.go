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
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	incidentsv1alpha1 "github.com/yihanhong/hivemind/api/v1alpha1"
)

// IncidentTriageReconciler reconciles a IncidentTriage object
type IncidentTriageReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=incidents.yihanhong.dev,resources=incidenttriages,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=incidents.yihanhong.dev,resources=incidenttriages/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=pods;events;configmaps,verbs=get;list

// Reconcile drives an IncidentTriage toward completion. It is level-based:
// it receives only a name/namespace and must re-derive all state from the
// cluster, so every branch must be safe to run any number of times.
func (r *IncidentTriageReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var triage incidentsv1alpha1.IncidentTriage
	if err := r.Get(ctx, req.NamespacedName, &triage); err != nil {
		// Not-found means the CR was deleted between the event and now; nothing to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if triage.Status.Phase == "" {
		patch := client.MergeFrom(triage.DeepCopy())
		triage.Status.Phase = incidentsv1alpha1.PhasePending
		triage.Status.Message = "awaiting agent dispatch"
		if err := r.Status().Patch(ctx, &triage, patch); err != nil {
			return ctrl.Result{}, err
		}
		log.Info("incident registered", "alert", triage.Spec.AlertName, "severity", triage.Spec.Severity)
	}

	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *IncidentTriageReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&incidentsv1alpha1.IncidentTriage{}).
		Named("incidenttriage").
		Complete(r)
}
