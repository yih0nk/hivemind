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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	incidentsv1alpha1 "github.com/yihanhong/hivemind/api/v1alpha1"
	"github.com/yihanhong/hivemind/internal/agents"
)

// cleanupFinalizer blocks deletion until the operator has released any
// external resources a triage run created (PR branches, scratch state).
const cleanupFinalizer = "hivemind.io/cleanup"

// IncidentTriageReconciler reconciles a IncidentTriage object
type IncidentTriageReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder

	// Dispatcher fans out the triage agents during the Triaging phase.
	Dispatcher *agents.Dispatcher
}

// +kubebuilder:rbac:groups=incidents.yihanhong.dev,resources=incidenttriages,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=incidents.yihanhong.dev,resources=incidenttriages/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=incidents.yihanhong.dev,resources=incidenttriages/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods;configmaps,verbs=get;list
// +kubebuilder:rbac:groups="",resources=events,verbs=get;list;create;patch

// Reconcile drives an IncidentTriage through the phase state machine.
// It is level-based: each call re-derives everything from observed state,
// so every branch must be safe to run any number of times. Transitions
// happen one per reconcile; the status patch itself triggers the watch,
// which immediately re-enqueues the object for the next step.
func (r *IncidentTriageReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var triage incidentsv1alpha1.IncidentTriage
	if err := r.Get(ctx, req.NamespacedName, &triage); err != nil {
		// Not-found means the CR was deleted between the event and now; nothing to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Deletion requested: run cleanup, release the finalizer, let the delete finish.
	if !triage.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&triage, cleanupFinalizer) {
			// No external resources exist yet; real cleanup lands with the PR agent.
			log.Info("cleanup complete, releasing finalizer", "alert", triage.Spec.AlertName)
			controllerutil.RemoveFinalizer(&triage, cleanupFinalizer)
			if err := r.Update(ctx, &triage); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Ensure the finalizer exists before any work creates state worth cleaning up.
	// AddFinalizer reports whether it changed anything, keeping this idempotent.
	if controllerutil.AddFinalizer(&triage, cleanupFinalizer) {
		if err := r.Update(ctx, &triage); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	switch triage.Status.Phase {
	case "":
		return ctrl.Result{}, r.patchStatus(ctx, &triage, func(s *incidentsv1alpha1.IncidentTriageStatus) {
			s.Phase = NextPhase("")
			s.Message = "awaiting agent dispatch"
		})

	case incidentsv1alpha1.PhasePending:
		if err := r.patchStatus(ctx, &triage, func(s *incidentsv1alpha1.IncidentTriageStatus) {
			now := metav1.Now()
			s.StartTime = &now
			s.Phase = NextPhase(incidentsv1alpha1.PhasePending)
			s.Message = "dispatching triage agents"
		}); err != nil {
			return ctrl.Result{}, err
		}
		r.emitEvent(&triage, corev1.EventTypeNormal, "TriageStarted",
			fmt.Sprintf("dispatching agents for alert %q", triage.Spec.AlertName))
		log.Info("triage started", "alert", triage.Spec.AlertName, "severity", triage.Spec.Severity)
		return ctrl.Result{}, nil

	case incidentsv1alpha1.PhaseTriaging:
		outputs, dispatchErr := r.Dispatcher.Dispatch(ctx, &triage)
		if dispatchErr != nil {
			// Outputs from agents that succeeded are still persisted:
			// a failed sibling must not erase their reports.
			log.Error(dispatchErr, "agent dispatch failed", "alert", triage.Spec.AlertName)
			return ctrl.Result{}, r.patchStatus(ctx, &triage, func(s *incidentsv1alpha1.IncidentTriageStatus) {
				s.AgentOutputs = outputs
				s.Phase = incidentsv1alpha1.PhaseFailed
				s.Message = fmt.Sprintf("agent dispatch failed: %v", dispatchErr)
			})
		}
		return ctrl.Result{}, r.patchStatus(ctx, &triage, func(s *incidentsv1alpha1.IncidentTriageStatus) {
			s.AgentOutputs = outputs
			s.Phase = NextPhase(incidentsv1alpha1.PhaseTriaging)
			s.Message = "all agents reported"
		})

	case incidentsv1alpha1.PhaseRemediated:
		if triage.Status.CompletionTime == nil {
			if err := r.patchStatus(ctx, &triage, func(s *incidentsv1alpha1.IncidentTriageStatus) {
				now := metav1.Now()
				s.CompletionTime = &now
				s.Message = "triage complete"
			}); err != nil {
				return ctrl.Result{}, err
			}
			r.emitEvent(&triage, corev1.EventTypeNormal, "RemediationComplete",
				fmt.Sprintf("triage for alert %q finished", triage.Spec.AlertName))
			log.Info("triage complete", "alert", triage.Spec.AlertName)
		}
		return ctrl.Result{}, nil

	case incidentsv1alpha1.PhaseFailed:
		if triage.Status.CompletionTime == nil {
			if err := r.patchStatus(ctx, &triage, func(s *incidentsv1alpha1.IncidentTriageStatus) {
				now := metav1.Now()
				s.CompletionTime = &now
			}); err != nil {
				return ctrl.Result{}, err
			}
			r.emitEvent(&triage, corev1.EventTypeWarning, "TriageFailed", triage.Status.Message)
			log.Info("triage failed", "alert", triage.Spec.AlertName, "message", triage.Status.Message)
		}
		return ctrl.Result{}, nil

	default:
		log.Info("ignoring unknown phase", "phase", triage.Status.Phase)
		return ctrl.Result{}, nil
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *IncidentTriageReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&incidentsv1alpha1.IncidentTriage{}).
		Named("incidenttriage").
		Complete(r)
}
