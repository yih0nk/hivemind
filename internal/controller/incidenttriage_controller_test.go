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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	incidentsv1alpha1 "github.com/yihanhong/hivemind/api/v1alpha1"
	"github.com/yihanhong/hivemind/internal/agents"
	"github.com/yihanhong/hivemind/internal/github"
)

// stubAgent stands in for real agents: the controller suite verifies the
// phase machine and dispatcher wiring, not agent internals, which have
// their own tests in internal/agents.
type stubAgent struct {
	name   string
	output string
}

func (s stubAgent) Name() string { return s.name }

func (s stubAgent) Run(context.Context, *incidentsv1alpha1.IncidentTriage) (string, error) {
	return s.output, nil
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
				Synthesizer: stubAgent{name: "synthesizer", output: `{"rootCause":"stub"}`},
				PRClient:    &github.FakePRClient{PRURL: "https://github.com/acme/runbooks/pull/1"},
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
	})
})
