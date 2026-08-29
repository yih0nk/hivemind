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
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	incidentsv1alpha1 "github.com/yih0nk/hivemind/api/v1alpha1"
	"github.com/yih0nk/hivemind/internal/agents"
	"github.com/yih0nk/hivemind/internal/github"
	"github.com/yih0nk/hivemind/internal/reasoner"
)

// cleanupFinalizer blocks deletion until the operator has released any
// external resources a triage run created (PR branches, scratch state).
const cleanupFinalizer = "hivemind.io/cleanup"

// IncidentTriageReconciler reconciles a IncidentTriage object
type IncidentTriageReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder

	// Dispatcher fans out the evidence agents in the first Triaging pass.
	Dispatcher *agents.Dispatcher
	// Reasoner runs alone in a second pass, turning the evidence agents'
	// outputs (persisted by the pass before it, never the one it runs in)
	// into the root-cause report. In-process by default; optionally the
	// external LangGraph brain over HTTP.
	Reasoner reasoner.Reasoner
	// PRClient publishes the finished report as a pull request.
	PRClient github.PRClient

	// AgentTimeout bounds the synthesizer's run, mirroring the bound
	// the dispatcher applies to the evidence agents. <= 0 means
	// agents.DefaultAgentTimeout.
	AgentTimeout time.Duration
}

// +kubebuilder:rbac:groups=incidents.yih0nk.github.io,resources=incidenttriages,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=incidents.yih0nk.github.io,resources=incidenttriages/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=incidents.yih0nk.github.io,resources=incidenttriages/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods;configmaps,verbs=get;list
// +kubebuilder:rbac:groups="",resources=pods/log,verbs=get
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
		return r.reconcileTriaging(ctx, &triage)

	case incidentsv1alpha1.PhaseAwaitingApproval:
		return r.reconcileApproval(ctx, &triage)

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

// reconcileTriaging advances the Triaging phase one pass per reconcile:
// evidence agents first, then the synthesizer (which reads their outputs
// from status, persisted by the previous pass), then the PR. Each pass
// ends in a status patch, so a crash between passes loses at most one
// pass's work, and the patch itself re-enqueues the object for the next.
func (r *IncidentTriageReconciler) reconcileTriaging(ctx context.Context, triage *incidentsv1alpha1.IncidentTriage) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Pass 1: nothing gathered yet -- fan out the evidence agents.
	if len(triage.Status.AgentOutputs) == 0 {
		outputs, dispatchErr := r.Dispatcher.Dispatch(ctx, triage)
		if dispatchErr != nil {
			// Outputs from agents that succeeded are still persisted:
			// a failed sibling must not erase their reports.
			log.Error(dispatchErr, "agent dispatch failed", "alert", triage.Spec.AlertName)
			return ctrl.Result{}, r.patchStatus(ctx, triage, func(s *incidentsv1alpha1.IncidentTriageStatus) {
				s.AgentOutputs = outputs
				s.Phase = incidentsv1alpha1.PhaseFailed
				s.Message = fmt.Sprintf("agent dispatch failed: %v", dispatchErr)
			})
		}
		return ctrl.Result{}, r.patchStatus(ctx, triage, func(s *incidentsv1alpha1.IncidentTriageStatus) {
			s.AgentOutputs = outputs
			s.Message = "evidence agents reported; synthesizing"
		})
	}

	// Pass 2: evidence persisted but not yet synthesized.
	if _, ok := triage.Status.AgentOutputs[r.Reasoner.Name()]; !ok {
		timeout := r.AgentTimeout
		if timeout <= 0 {
			timeout = agents.DefaultAgentTimeout
		}
		synthCtx, cancel := context.WithTimeout(ctx, timeout)
		result, err := r.Reasoner.Synthesize(synthCtx, triage)
		cancel()
		if err == nil {
			// The reasoner paused for a human: park the run in
			// AwaitingApproval and surface the proposal + thread id.
			if result.Status == reasoner.StatusAwaitingApproval {
				r.emitEvent(triage, corev1.EventTypeNormal, "ApprovalRequired",
					fmt.Sprintf("triage for alert %q awaiting human approval", triage.Spec.AlertName))
				log.Info("triage awaiting approval", "alert", triage.Spec.AlertName,
					"thread", result.ThreadID)
				return ctrl.Result{}, r.patchStatus(ctx, triage, func(s *incidentsv1alpha1.IncidentTriageStatus) {
					s.Phase = incidentsv1alpha1.PhaseAwaitingApproval
					s.ReasonerThreadID = result.ThreadID
					s.PendingProposal = result.Proposal
					s.Message = "awaiting approval: annotate " + approvalAnnotation + "=approve|reject"
				})
			}
			return ctrl.Result{}, r.patchStatus(ctx, triage, func(s *incidentsv1alpha1.IncidentTriageStatus) {
				s.AgentOutputs[r.Reasoner.Name()] = result.Report
				s.Message = "synthesis complete; publishing report"
			})
		}
		// A failed synthesis does not fail the run: the evidence is
		// already gathered and persisted, and a partial report in a PR
		// beats a complete one that never lands. Fall through and
		// publish; the missing section renders as "_no output_".
		log.Error(err, "synthesis failed; publishing partial report", "alert", triage.Spec.AlertName)
		r.emitEvent(triage, corev1.EventTypeWarning, "SynthesisFailed",
			fmt.Sprintf("publishing partial report: %v", err))
	}

	// Pass 3: full report persisted -- publish it (when a repo is
	// configured) and leave Triaging.
	url := triage.Status.PRURL
	if url == "" && triage.Spec.GithubRepo != "" {
		var err error
		url, err = github.OpenIncidentPR(ctx, r.PRClient, triage.Spec.GithubRepo, triage)
		if err != nil {
			log.Error(err, "opening incident PR failed", "alert", triage.Spec.AlertName)
			return ctrl.Result{}, r.patchStatus(ctx, triage, func(s *incidentsv1alpha1.IncidentTriageStatus) {
				s.Phase = incidentsv1alpha1.PhaseFailed
				s.Message = fmt.Sprintf("opening incident PR failed: %v", err)
			})
		}
		r.emitEvent(triage, corev1.EventTypeNormal, "PROpened",
			fmt.Sprintf("incident report for alert %q: %s", triage.Spec.AlertName, url))
		log.Info("incident PR opened", "alert", triage.Spec.AlertName, "url", url)
	}
	message := "incident report published"
	if url == "" {
		message = "no GitHub repo configured; skipping PR"
	}
	return ctrl.Result{}, r.patchStatus(ctx, triage, func(s *incidentsv1alpha1.IncidentTriageStatus) {
		s.PRURL = url
		s.Phase = NextPhase(incidentsv1alpha1.PhaseTriaging)
		s.Message = message
	})
}

// Approval annotations a human sets to decide a paused triage. Applying either
// value updates the CR, which re-enqueues it into reconcileApproval.
const (
	approvalAnnotation     = "hivemind.io/approval"      // "approve" | "reject"
	approvalNoteAnnotation = "hivemind.io/approval-note" // optional reviewer note
)

// reconcileApproval handles a run paused at the approval gate. It waits (does
// nothing) until a human sets the approval annotation, then resumes the reasoner
// with that decision: approve re-enters Triaging to publish the PR, reject ends
// the run Remediated with no PR. The resumed report is persisted either way, so
// a rejected proposal is still recorded.
func (r *IncidentTriageReconciler) reconcileApproval(ctx context.Context, triage *incidentsv1alpha1.IncidentTriage) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	decision := triage.Annotations[approvalAnnotation]
	if decision != "approve" && decision != "reject" {
		// No decision yet: wait. Annotating the CR re-enqueues it here.
		return ctrl.Result{}, nil
	}
	approve := decision == "approve"

	timeout := r.AgentTimeout
	if timeout <= 0 {
		timeout = agents.DefaultAgentTimeout
	}
	resumeCtx, cancel := context.WithTimeout(ctx, timeout)
	result, err := r.Reasoner.Resume(
		resumeCtx, triage.Status.ReasonerThreadID, approve, triage.Annotations[approvalNoteAnnotation])
	cancel()
	if err != nil {
		log.Error(err, "resuming reasoner after approval failed", "alert", triage.Spec.AlertName)
		return ctrl.Result{}, r.patchStatus(ctx, triage, func(s *incidentsv1alpha1.IncidentTriageStatus) {
			s.Phase = incidentsv1alpha1.PhaseFailed
			s.Message = fmt.Sprintf("resuming after approval failed: %v", err)
		})
	}

	if !approve {
		r.emitEvent(triage, corev1.EventTypeNormal, "TriageRejected",
			fmt.Sprintf("human rejected the proposed fix for alert %q; no PR opened", triage.Spec.AlertName))
		log.Info("triage rejected by human", "alert", triage.Spec.AlertName)
		return ctrl.Result{}, r.patchStatus(ctx, triage, func(s *incidentsv1alpha1.IncidentTriageStatus) {
			s.AgentOutputs[r.Reasoner.Name()] = result.Report
			s.ReasonerThreadID = ""
			s.PendingProposal = ""
			s.Phase = incidentsv1alpha1.PhaseRemediated
			s.Message = "rejected by human; no PR opened"
		})
	}

	r.emitEvent(triage, corev1.EventTypeNormal, "TriageApproved",
		fmt.Sprintf("human approved the proposed fix for alert %q", triage.Spec.AlertName))
	log.Info("triage approved by human", "alert", triage.Spec.AlertName)
	// Re-enter Triaging with the synthesizer output now persisted, so the next
	// pass opens the PR.
	return ctrl.Result{}, r.patchStatus(ctx, triage, func(s *incidentsv1alpha1.IncidentTriageStatus) {
		s.AgentOutputs[r.Reasoner.Name()] = result.Report
		s.ReasonerThreadID = ""
		s.PendingProposal = ""
		s.Phase = incidentsv1alpha1.PhaseTriaging
		s.Message = "approved; publishing report"
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *IncidentTriageReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&incidentsv1alpha1.IncidentTriage{}).
		Named("incidenttriage").
		Complete(r)
}
