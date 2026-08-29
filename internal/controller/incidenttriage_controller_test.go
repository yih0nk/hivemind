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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	incidentsv1alpha1 "github.com/yih0nk/hivemind/api/v1alpha1"
	"github.com/yih0nk/hivemind/internal/agents"
	"github.com/yih0nk/hivemind/internal/github"
	"github.com/yih0nk/hivemind/internal/reasoner"
)

// stubAgent stands in for real agents: the controller suite verifies the
// phase machine and dispatcher wiring, not agent internals, which have
// their own tests in internal/agents.
type stubAgent struct {
	name   string
	output string
	err    error
}

func (s stubAgent) Name() string { return s.name }

func (s stubAgent) Run(context.Context, *incidentsv1alpha1.IncidentTriage) (string, error) {
	return s.output, s.err
}

// stubReasoner drives the approval-gate paths: Synthesize pauses, Resume
// completes. It records the last resume decision for assertions.
type stubReasoner struct {
	resumed *bool
}

func (s stubReasoner) Name() string { return reasoner.SynthesizerKey }

func (s stubReasoner) Synthesize(context.Context, *incidentsv1alpha1.IncidentTriage) (reasoner.Result, error) {
	return reasoner.Result{
		Status:   reasoner.StatusAwaitingApproval,
		ThreadID: "thread-1",
		Proposal: "root cause: stub",
	}, nil
}

func (s stubReasoner) Resume(_ context.Context, _ string, approve bool, _ string) (reasoner.Result, error) {
	if s.resumed != nil {
		*s.resumed = approve
	}
	return reasoner.Result{Status: reasoner.StatusCompleted, Report: `{"rootCause":"resumed"}`}, nil
}

var _ = Describe("IncidentTriage Controller", func() {
	Context("When reconciling a resource", func() {
		const (
			resourceName      = "test-resource"
			resourceNamespace = "default"
		)

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: resourceNamespace,
		}
		incidenttriage := &incidentsv1alpha1.IncidentTriage{}

		controllerReconciler := &IncidentTriageReconciler{}

		BeforeEach(func() {
			*controllerReconciler = IncidentTriageReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: events.NewFakeRecorder(32),
				Dispatcher: &agents.Dispatcher{
					Agents: []agents.Agent{
						stubAgent{name: "logtriage", output: `{"likelyCause":"stub"}`},
					},
				},
				Reasoner: reasoner.NewInProcess(
					stubAgent{name: "synthesizer", output: `{"rootCause":"stub"}`},
				),
				PRClient: &github.FakePRClient{PRURL: "https://github.com/acme/runbooks/pull/1"},
			}
			By("creating the custom resource for the Kind IncidentTriage")
			err := k8sClient.Get(ctx, typeNamespacedName, incidenttriage)
			if err != nil && errors.IsNotFound(err) {
				resource := &incidentsv1alpha1.IncidentTriage{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: resourceNamespace,
					},
					Spec: incidentsv1alpha1.IncidentTriageSpec{
						AlertName:         "TestAlertFiring",
						Severity:          incidentsv1alpha1.SeverityCritical,
						AffectedNamespace: "default",
						GithubRepo:        "acme/runbooks",
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &incidentsv1alpha1.IncidentTriage{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance IncidentTriage")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			// The delete only completes once the reconciler releases the finalizer.
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			err = k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(errors.IsNotFound(err)).To(BeTrue())
		})
		It("should drive the resource through the phase state machine", func() {
			By("Reconciling until the terminal phase is reached")
			// finalizer -> Pending -> Triaging (evidence, synthesis, PR
			// passes) -> Remediated -> completion stamp
			for range 8 {
				_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: typeNamespacedName,
				})
				Expect(err).NotTo(HaveOccurred())
			}

			updated := &incidentsv1alpha1.IncidentTriage{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Finalizers).To(ContainElement(cleanupFinalizer))
			Expect(updated.Status.Phase).To(Equal(incidentsv1alpha1.PhaseRemediated))
			Expect(updated.Status.AgentOutputs).To(HaveKeyWithValue("logtriage", `{"likelyCause":"stub"}`))
			Expect(updated.Status.AgentOutputs).To(HaveKeyWithValue("synthesizer", `{"rootCause":"stub"}`))
			Expect(updated.Status.PRURL).To(Equal("https://github.com/acme/runbooks/pull/1"))
			Expect(updated.Status.StartTime).NotTo(BeNil())
			Expect(updated.Status.CompletionTime).NotTo(BeNil())
			Expect(updated.Status.Message).To(Equal("triage complete"))
		})

		It("should publish a partial report when synthesis fails", func() {
			controllerReconciler.Reasoner = reasoner.NewInProcess(stubAgent{
				name: "synthesizer",
				err:  fmt.Errorf("ollama unreachable"),
			})

			for range 8 {
				_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: typeNamespacedName,
				})
				Expect(err).NotTo(HaveOccurred())
			}

			updated := &incidentsv1alpha1.IncidentTriage{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(incidentsv1alpha1.PhaseRemediated))
			Expect(updated.Status.AgentOutputs).To(HaveKey("logtriage"))
			Expect(updated.Status.AgentOutputs).NotTo(HaveKey("synthesizer"))
			Expect(updated.Status.PRURL).To(Equal("https://github.com/acme/runbooks/pull/1"))
		})

		reconcileN := func(n int) {
			for range n {
				_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: typeNamespacedName,
				})
				Expect(err).NotTo(HaveOccurred())
			}
		}

		annotate := func(key, value string) {
			cur := &incidentsv1alpha1.IncidentTriage{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, cur)).To(Succeed())
			cur.Annotations = map[string]string{key: value}
			Expect(k8sClient.Update(ctx, cur)).To(Succeed())
		}

		It("should pause at AwaitingApproval, then open a PR once approved", func() {
			controllerReconciler.Reasoner = stubReasoner{}

			// finalizer -> Pending -> Triaging(evidence) -> Triaging(synth pauses)
			reconcileN(6)
			paused := &incidentsv1alpha1.IncidentTriage{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, paused)).To(Succeed())
			Expect(paused.Status.Phase).To(Equal(incidentsv1alpha1.PhaseAwaitingApproval))
			Expect(paused.Status.ReasonerThreadID).To(Equal("thread-1"))
			Expect(paused.Status.PendingProposal).To(ContainSubstring("stub"))
			Expect(paused.Status.PRURL).To(BeEmpty())

			annotate(approvalAnnotation, "approve")
			reconcileN(6)

			done := &incidentsv1alpha1.IncidentTriage{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, done)).To(Succeed())
			Expect(done.Status.Phase).To(Equal(incidentsv1alpha1.PhaseRemediated))
			Expect(done.Status.AgentOutputs).To(HaveKeyWithValue("synthesizer", `{"rootCause":"resumed"}`))
			Expect(done.Status.PRURL).To(Equal("https://github.com/acme/runbooks/pull/1"))
			Expect(done.Status.ReasonerThreadID).To(BeEmpty())
		})

		It("should end without a PR when the proposal is rejected", func() {
			rejected := true // will be overwritten by Resume's approve flag
			controllerReconciler.Reasoner = stubReasoner{resumed: &rejected}

			reconcileN(6)
			paused := &incidentsv1alpha1.IncidentTriage{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, paused)).To(Succeed())
			Expect(paused.Status.Phase).To(Equal(incidentsv1alpha1.PhaseAwaitingApproval))

			annotate(approvalAnnotation, "reject")
			reconcileN(4)

			done := &incidentsv1alpha1.IncidentTriage{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, done)).To(Succeed())
			Expect(done.Status.Phase).To(Equal(incidentsv1alpha1.PhaseRemediated))
			Expect(rejected).To(BeFalse()) // Resume was called with approve=false
			Expect(done.Status.PRURL).To(BeEmpty())
		})
	})
})
