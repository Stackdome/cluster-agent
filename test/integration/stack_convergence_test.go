package integration

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"
	"stackdome.io/cluster-agent/test/integration/fixtures"
	"stackdome.io/cluster-agent/test/integration/helpers"
)

// Stack convergence tests exercise the full convergence hierarchy end-to-end:
//
//   StackResource.Converged=True → Stack.ResourcesReady=True → Stack.Available=True → Phase=Ready
//
// Convergence is strictly stronger than availability: a StackResource can be Available
// (old pods serving traffic) but not Converged (new pods failing). The Stack controller
// aggregates child states into phase by priority: Failed > Progressing > Degraded > Ready > Pending.
//
// Key invariants tested here:
//   - Orphans (children not in spec.resourceNames) block Available without affecting ResourcesReady
//   - Per-child revision tokens allow independent rollouts; Stack converges only when all match
//   - lastConverged is write-once per revision+releaseID — a sticky checkpoint of the last known-good state
//   - Degraded = all children serving but not all converged (requires prior convergence; first deploy is Progressing)
//   - MinReadySeconds prevents the deployment controller from scaling down the old RS when a new pod
//     briefly starts then crashes (pod must stay Ready for 10s before counting as available)
//   - Addresses (external/internal) persist when a workload goes unhealthy because the Service/Ingress
//     still exist in the cluster; only the pods are down
var _ = Describe("Stack convergence", func() {

	// Orphan detection: a StackResource that exists in the cluster but is NOT listed in
	// spec.resourceNames should block Stack.Available (orphan = pending deletion) without
	// affecting ResourcesReady (the named children are still healthy).
	Context("Orphan detection", Ordered, func() {
		var stack *corev1alpha1.Stack

		It("should report orphaned resources and block Available", func() {
			swr := fixtures.SimpleStack("orphan-stack")
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack

			By("Waiting for initial Ready")
			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), readyTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Creating an orphan StackResource")
			orphan := &corev1alpha1.StackResource{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "orphan-extra",
					Namespace: stack.Namespace,
					Labels: map[string]string{
						corev1alpha1.LabelManagedBy:    corev1alpha1.ManagedByStackdome,
						corev1alpha1.LabelStackName:    stack.Name,
						corev1alpha1.LabelResourceName: "orphan-extra",
					},
					Annotations: map[string]string{
						corev1alpha1.RevisionAnnotation: fixtures.TestRevision,
					},
				},
				Spec: corev1alpha1.StackResourceSpec{
					ImageSpec: &corev1alpha1.ImageSpec{Image: "nginx:1.25-alpine"},
					Ports:     []corev1alpha1.Port{{Name: "http", Number: 80, Protocol: "http", FQDN: "orphan-extra.local"}},
				},
			}
			orphan.OwnerReferences = []metav1.OwnerReference{{
				APIVersion:         "core.stackdome.io/v1alpha1",
				Kind:               "Stack",
				Name:               stack.Name,
				UID:                stack.UID,
				Controller:         ptr.To(true),
				BlockOwnerDeletion: ptr.To(true),
			}}
			Expect(c.Create(ctx, orphan)).To(Succeed())

			By("Waiting for Stack to report orphan")
			Eventually(func() []string {
				s := &corev1alpha1.Stack{}
				if err := c.Get(ctx, client.ObjectKeyFromObject(stack), s); err != nil {
					return nil
				}
				return s.Status.OrphanedResources
			}, readyTimeout, "5s").Should(ContainElement("orphan-extra"))

			By("Verifying Stack is Pending with ResourcesReady=True but Available=False")
			s := &corev1alpha1.Stack{}
			Expect(c.Get(ctx, client.ObjectKeyFromObject(stack), s)).To(Succeed())
			Expect(s.Status.Phase).To(Equal(corev1alpha1.StackPending))

			resourcesReady := meta.FindStatusCondition(s.Status.Conditions, string(corev1alpha1.StackConditionResourcesReady))
			Expect(resourcesReady).NotTo(BeNil())
			Expect(resourcesReady.Status).To(Equal(metav1.ConditionTrue), "named children are healthy")

			available := meta.FindStatusCondition(s.Status.Conditions, string(corev1alpha1.StackConditionAvailable))
			Expect(available).NotTo(BeNil())
			Expect(available.Status).To(Equal(metav1.ConditionFalse))
			Expect(available.Reason).To(Equal("OrphanedResources"))

			By("Deleting the orphan")
			Expect(c.Delete(ctx, orphan)).To(Succeed())

			By("Waiting for Stack to return to Ready")
			_, err = helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), readyTimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		AfterAll(func() {
			helpers.CleanupStack(ctx, c, stack)
		})
	})

	// Per-child revision tokens: in managed mode, each child independently echoes its own
	// revision annotation. The Stack only converges when ALL children have observed their
	// respective tokens. A broken child C does not invalidate the convergence of child B.
	Context("Per-child revision token convergence", Ordered, func() {
		const (
			stackName    = "rev-conv-stack"
			resA         = "rev-conv-a"
			resB         = "rev-conv-b"
			revA1        = "rev-a1"
			revB1        = "rev-b1"
			root1        = "root-1"
			revA2        = "rev-a2"
			root2        = "root-2"
			revA3        = "rev-a3"
			root3        = "root-3"
			brokenImage  = "nonexistent-registry.example.com/fake-image:v999"
			workingImage = "nginx:1.25-alpine"
		)
		var stack *corev1alpha1.Stack

		stampAnnotation := func(obj client.Object, rev string) {
			a := obj.GetAnnotations()
			if a == nil {
				a = make(map[string]string)
			}
			a[corev1alpha1.RevisionAnnotation] = rev
			obj.SetAnnotations(a)
		}

		It("should converge when each child echoes its own distinct token", func() {
			By("Creating Stack with distinct per-child tokens")
			swr := fixtures.NewStack(stackName,
				fixtures.NewResource(stackName, resA),
				fixtures.NewResource(stackName, resB))

			stampAnnotation(swr.Stack, root1)
			Expect(c.Create(ctx, swr.Stack)).To(Succeed())
			Expect(c.Get(ctx, client.ObjectKeyFromObject(swr.Stack), swr.Stack)).To(Succeed())
			stack = swr.Stack

			for _, sr := range swr.Resources {
				switch sr.Name {
				case resA:
					stampAnnotation(sr, revA1)
				case resB:
					stampAnnotation(sr, revB1)
				}
				sr.OwnerReferences = []metav1.OwnerReference{fixtures.OwnerRefTo(stack)}
				Expect(c.Create(ctx, sr)).To(Succeed())
			}

			By("Waiting for Stack to reach Ready")
			readyStack, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), readyTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying each child echoes its own token")
			srA := &corev1alpha1.StackResource{}
			Expect(c.Get(ctx, client.ObjectKey{Name: resA, Namespace: stack.Namespace}, srA)).To(Succeed())
			Expect(srA.Status.ObservedRevision).To(Equal(revA1))

			srB := &corev1alpha1.StackResource{}
			Expect(c.Get(ctx, client.ObjectKey{Name: resB, Namespace: stack.Namespace}, srB)).To(Succeed())
			Expect(srB.Status.ObservedRevision).To(Equal(revB1))

			By("Verifying Stack echoes the root token")
			Expect(readyStack.Status.LastConverged).NotTo(BeNil())
			Expect(readyStack.Status.LastConverged.Revision).To(Equal(root1))
		})

		It("should stay Pending while a new child cannot converge", func() {
			By("Simulating a release that adds a new broken child C")
			resC := "rev-conv-c"
			newSR := fixtures.NewResource(stackName, resC,
				fixtures.WithImage(brokenImage))
			stampAnnotation(newSR, "rev-c1")
			newSR.OwnerReferences = []metav1.OwnerReference{fixtures.OwnerRefTo(stack)}
			Expect(c.Create(ctx, newSR)).To(Succeed())

			By("Updating Stack to include C in resourceNames and bump root to root-2")
			Eventually(func() error {
				if err := c.Get(ctx, client.ObjectKeyFromObject(stack), stack); err != nil {
					return err
				}
				stack.Spec.ResourceNames = []string{resA, resB, resC}
				stampAnnotation(stack, root2)
				return c.Update(ctx, stack)
			}, "30s", "1s").Should(Succeed())

			By("Waiting for Stack to drop to Pending (child C can never converge)")
			Eventually(func() corev1alpha1.StackPhase {
				s := &corev1alpha1.Stack{}
				if err := c.Get(ctx, client.ObjectKeyFromObject(stack), s); err != nil {
					return ""
				}
				return s.Status.Phase
			}, readyTimeout, "5s").Should(Equal(corev1alpha1.StackProgressing),
				"Stack must be Progressing while child C cannot converge (new revision rolling out)")

			By("Verifying Stack lastConverged still shows root-1 (sticky)")
			s := &corev1alpha1.Stack{}
			Expect(c.Get(ctx, client.ObjectKeyFromObject(stack), s)).To(Succeed())
			Expect(s.Status.LastConverged).NotTo(BeNil())
			Expect(s.Status.LastConverged.Revision).To(Equal(root1),
				"lastConverged must remain at root-1")

			By("Verifying child B stays converged on rev-b1 (no cross-comparison)")
			srB := &corev1alpha1.StackResource{}
			Expect(c.Get(ctx, client.ObjectKey{Name: resB, Namespace: stack.Namespace}, srB)).To(Succeed())
			Expect(srB.Status.ObservedRevision).To(Equal(revB1),
				"untouched child must stay converged on its original token")
		})

		It("should converge after the broken child is fixed", func() {
			resC := "rev-conv-c"

			By("Fixing child C with a working image and bumping Stack to root-3")
			Eventually(func() error {
				srC := &corev1alpha1.StackResource{}
				if err := c.Get(ctx, client.ObjectKey{Name: resC, Namespace: stack.Namespace}, srC); err != nil {
					return err
				}
				stampAnnotation(srC, revA3)
				srC.Spec.ImageSpec = &corev1alpha1.ImageSpec{Image: workingImage}
				return c.Update(ctx, srC)
			}, "30s", "1s").Should(Succeed())

			Eventually(func() error {
				if err := c.Get(ctx, client.ObjectKeyFromObject(stack), stack); err != nil {
					return err
				}
				stampAnnotation(stack, root3)
				return c.Update(ctx, stack)
			}, "30s", "1s").Should(Succeed())

			By("Waiting for Stack to return to Ready and echo root-3")
			readyStack, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), readyTimeout)
			Expect(err).NotTo(HaveOccurred())
			Expect(readyStack.Status.LastConverged).NotTo(BeNil())
			Expect(readyStack.Status.LastConverged.Revision).To(Equal(root3))

			By("Verifying child B is still converged on rev-b1 (no churn)")
			srB := &corev1alpha1.StackResource{}
			Expect(c.Get(ctx, client.ObjectKey{Name: resB, Namespace: stack.Namespace}, srB)).To(Succeed())
			Expect(srB.Status.ObservedRevision).To(Equal(revB1))
		})

		AfterAll(func() {
			helpers.CleanupStack(ctx, c, stack)
		})
	})

	// Missing child: a resourceName in spec with no corresponding StackResource CR is Pending,
	// not Progressing — we can't progress what doesn't exist.
	Context("Missing child detection", func() {
		It("should report Pending with missing resource", func() {
			stack := &corev1alpha1.Stack{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "missing-child-stack",
					Namespace: env.TestNamespace,
					Labels: map[string]string{
						corev1alpha1.LabelManagedBy: corev1alpha1.ManagedByStackdome,
						corev1alpha1.LabelStackName: "missing-child-stack",
					},
					Annotations: map[string]string{
						corev1alpha1.RevisionAnnotation: fixtures.TestRevision,
					},
				},
				Spec: corev1alpha1.StackSpec{
					ResourceNames: []string{"nonexistent-resource"},
				},
			}
			Expect(c.Create(ctx, stack)).To(Succeed())

			Eventually(func() bool {
				s := &corev1alpha1.Stack{}
				if err := c.Get(ctx, client.ObjectKeyFromObject(stack), s); err != nil {
					return false
				}
				if s.Status.Phase != corev1alpha1.StackPending {
					return false
				}
				for _, r := range s.Status.Resources {
					if r.Name == "nonexistent-resource" && r.Missing {
						return true
					}
				}
				return false
			}, readyTimeout, "5s").Should(BeTrue())

			_ = c.Delete(ctx, stack)
			_ = helpers.WaitForStackDeleted(ctx, c, client.ObjectKeyFromObject(stack), deleteTimeout)
		})
	})

	// Unannotated child in managed mode: a child without the revision annotation cannot
	// satisfy isConverged (rev == ""), so the Stack blocks with a specific message.
	Context("Managed stack with unannotated child stays Pending", func() {
		It("should block convergence with a specific message", func() {
			stackName := "managed-unannotated"
			resourceName := stackName + "-app"

			swr := fixtures.NewStack(stackName,
				fixtures.NewResource(stackName, resourceName))

			stampAnnotation := func(obj client.Object, rev string) {
				a := obj.GetAnnotations()
				if a == nil {
					a = make(map[string]string)
				}
				a[corev1alpha1.RevisionAnnotation] = rev
				obj.SetAnnotations(a)
			}

			By("Creating Stack with root annotation but child WITHOUT annotation")
			stampAnnotation(swr.Stack, "root-managed")
			Expect(c.Create(ctx, swr.Stack)).To(Succeed())
			Expect(c.Get(ctx, client.ObjectKeyFromObject(swr.Stack), swr.Stack)).To(Succeed())

			sr := swr.Resources[0]
			sr.OwnerReferences = []metav1.OwnerReference{fixtures.OwnerRefTo(swr.Stack)}
			Expect(c.Create(ctx, sr)).To(Succeed())

			By("Waiting for Stack to report the unannotated child message")
			Eventually(func() string {
				s := &corev1alpha1.Stack{}
				if err := c.Get(ctx, client.ObjectKeyFromObject(swr.Stack), s); err != nil {
					return ""
				}
				for _, r := range s.Status.Resources {
					if r.Name == resourceName {
						return r.Message
					}
				}
				return ""
			}, readyTimeout, "5s").Should(Equal("no revision annotation (required for release-managed stacks)"))

			By("Verifying Stack is Progressing (first deploy, child not converged)")
			s := &corev1alpha1.Stack{}
			Expect(c.Get(ctx, client.ObjectKeyFromObject(swr.Stack), s)).To(Succeed())
			Expect(s.Status.Phase).To(Equal(corev1alpha1.StackProgressing))

			By("Cleanup")
			helpers.CleanupStack(ctx, c, swr.Stack)
		})
	})

	// Rollback: lastConverged is keyed on revision+releaseID. Changing the releaseID (same
	// revision) is a distinct convergence event — the write-once guard fires again.
	Context("Rollback produces new lastConverged record", Ordered, func() {
		const (
			stackName = "rollback-stack"
			resName   = "rollback-app"
			rev1      = "rb-rev-1"
			root1     = "rb-root-1"
			release1  = "rb-uuid-1"
			release2  = "rb-uuid-2"
		)
		var stack *corev1alpha1.Stack

		stampAnnotation := func(obj client.Object, key, val string) {
			a := obj.GetAnnotations()
			if a == nil {
				a = make(map[string]string)
			}
			a[key] = val
			obj.SetAnnotations(a)
		}

		It("should converge on release 1", func() {
			swr := fixtures.NewStack(stackName,
				fixtures.NewResource(stackName, resName))

			stampAnnotation(swr.Stack, corev1alpha1.RevisionAnnotation, root1)
			stampAnnotation(swr.Stack, corev1alpha1.ReleaseIDAnnotation, release1)
			Expect(c.Create(ctx, swr.Stack)).To(Succeed())
			Expect(c.Get(ctx, client.ObjectKeyFromObject(swr.Stack), swr.Stack)).To(Succeed())
			stack = swr.Stack

			sr := swr.Resources[0]
			stampAnnotation(sr, corev1alpha1.RevisionAnnotation, rev1)
			sr.OwnerReferences = []metav1.OwnerReference{fixtures.OwnerRefTo(stack)}
			Expect(c.Create(ctx, sr)).To(Succeed())

			By("Waiting for Stack to converge")
			readyStack, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), readyTimeout)
			Expect(err).NotTo(HaveOccurred())
			Expect(readyStack.Status.LastConverged).NotTo(BeNil())
			Expect(readyStack.Status.LastConverged.Revision).To(Equal(root1))
			Expect(readyStack.Status.LastConverged.ReleaseID).To(Equal(release1))
		})

		It("should produce a new lastConverged record on rollback with same revision but new releaseID", func() {
			By("Simulating rollback: same revision root1, new releaseID uuid-2")
			Eventually(func() error {
				if err := c.Get(ctx, client.ObjectKeyFromObject(stack), stack); err != nil {
					return err
				}
				stampAnnotation(stack, corev1alpha1.ReleaseIDAnnotation, release2)
				return c.Update(ctx, stack)
			}, "30s", "1s").Should(Succeed())

			By("Waiting for lastConverged.releaseId to update to uuid-2")
			Eventually(func() string {
				s := &corev1alpha1.Stack{}
				if err := c.Get(ctx, client.ObjectKeyFromObject(stack), s); err != nil {
					return ""
				}
				if s.Status.LastConverged == nil {
					return ""
				}
				return s.Status.LastConverged.ReleaseID
			}, readyTimeout, "5s").Should(Equal(release2))

			By("Verifying revision is still root1")
			s := &corev1alpha1.Stack{}
			Expect(c.Get(ctx, client.ObjectKeyFromObject(stack), s)).To(Succeed())
			Expect(s.Status.LastConverged.Revision).To(Equal(root1))
		})

		AfterAll(func() {
			helpers.CleanupStack(ctx, c, stack)
		})
	})

	// Partial failure: when one child gets a broken image, lastConverged stays sticky at the
	// previous revision. The healthy child converges independently. The Stack is Progressing
	// (new revision active) + Degraded (all children serving via old pods). The broken child
	// reports Phase=Degraded with enriched convergence condition and captured failure details.
	Context("Partial failure preserves last-converged revision", Ordered, func() {
		const (
			stackName    = "partial-fail-stack"
			resA         = "partial-fail-a"
			resB         = "partial-fail-b"
			revA1        = "pf-rev-a1"
			revB1        = "pf-rev-b1"
			root1        = "pf-root-1"
			revA2        = "pf-rev-a2"
			revB2        = "pf-rev-b2"
			root2        = "pf-root-2"
			brokenImage  = "nonexistent-registry.example.com/crash-image:v999"
			workingImage = "nginxinc/nginx-unprivileged:1.25-alpine"
		)
		var stack *corev1alpha1.Stack

		stampAnnotation := func(obj client.Object, rev string) {
			a := obj.GetAnnotations()
			if a == nil {
				a = make(map[string]string)
			}
			a[corev1alpha1.RevisionAnnotation] = rev
			obj.SetAnnotations(a)
		}

		It("should converge at revision 1 with both resources healthy", func() {
			By("Creating Stack with two resources")
			swr := fixtures.NewStack(stackName,
				fixtures.NewResource(stackName, resA, fixtures.WithImage(workingImage), fixtures.WithHardenedSecurity()),
				fixtures.NewResource(stackName, resB, fixtures.WithImage(workingImage), fixtures.WithHardenedSecurity()))

			stampAnnotation(swr.Stack, root1)
			Expect(c.Create(ctx, swr.Stack)).To(Succeed())
			Expect(c.Get(ctx, client.ObjectKeyFromObject(swr.Stack), swr.Stack)).To(Succeed())
			stack = swr.Stack

			for _, sr := range swr.Resources {
				switch sr.Name {
				case resA:
					stampAnnotation(sr, revA1)
				case resB:
					stampAnnotation(sr, revB1)
				}
				sr.OwnerReferences = []metav1.OwnerReference{fixtures.OwnerRefTo(stack)}
				Expect(c.Create(ctx, sr)).To(Succeed())
			}

			By("Waiting for Stack to converge at root-1")
			readyStack, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), readyTimeout)
			Expect(err).NotTo(HaveOccurred())
			Expect(readyStack.Status.LastConverged).NotTo(BeNil())
			Expect(readyStack.Status.LastConverged.Revision).To(Equal(root1))

			By("Verifying both SRs converged at their rev1 tokens")
			for _, name := range []string{resA, resB} {
				sr := &corev1alpha1.StackResource{}
				Expect(c.Get(ctx, client.ObjectKey{Name: name, Namespace: stack.Namespace}, sr)).To(Succeed())
				Expect(sr.Status.LastConverged).NotTo(BeNil(), "%s should have LastConverged", name)
			}
		})

		It("should keep Stack lastConverged at rev1 when one child gets a broken image at rev2", func() {
			By("Updating SR-A to a broken image at revision 2")
			Eventually(func() error {
				srA := &corev1alpha1.StackResource{}
				if err := c.Get(ctx, client.ObjectKey{Name: resA, Namespace: stack.Namespace}, srA); err != nil {
					return err
				}
				stampAnnotation(srA, revA2)
				srA.Spec.ImageSpec = &corev1alpha1.ImageSpec{Image: brokenImage}
				return c.Update(ctx, srA)
			}, "30s", "1s").Should(Succeed())

			By("Updating SR-B to a working image at revision 2")
			Eventually(func() error {
				srB := &corev1alpha1.StackResource{}
				if err := c.Get(ctx, client.ObjectKey{Name: resB, Namespace: stack.Namespace}, srB); err != nil {
					return err
				}
				stampAnnotation(srB, revB2)
				srB.Spec.ImageSpec = &corev1alpha1.ImageSpec{Image: workingImage}
				return c.Update(ctx, srB)
			}, "30s", "1s").Should(Succeed())

			By("Bumping Stack root to revision 2")
			Eventually(func() error {
				if err := c.Get(ctx, client.ObjectKeyFromObject(stack), stack); err != nil {
					return err
				}
				stampAnnotation(stack, root2)
				return c.Update(ctx, stack)
			}, "30s", "1s").Should(Succeed())

			By("Waiting for SR-B to converge at rev2")
			Eventually(func() string {
				sr := &corev1alpha1.StackResource{}
				if err := c.Get(ctx, client.ObjectKey{Name: resB, Namespace: stack.Namespace}, sr); err != nil {
					return ""
				}
				if sr.Status.LastConverged == nil {
					return ""
				}
				return sr.Status.LastConverged.Revision
			}, readyTimeout, "5s").Should(Equal(revB2))

			By("Verifying SR-B is Available with ObservedRevision=rev2")
			srB := &corev1alpha1.StackResource{}
			Expect(c.Get(ctx, client.ObjectKey{Name: resB, Namespace: stack.Namespace}, srB)).To(Succeed())
			Expect(helpers.StackResourceIsAvailable(srB)).To(BeTrue(), "SR-B should be Available")
			Expect(srB.Status.ObservedRevision).To(Equal(revB2))
			Expect(srB.Status.AvailableReplicas).To(Equal(int32(1)), "SR-B should have 1 available replica")
			Expect(srB.Status.UpdatedReplicas).To(Equal(int32(1)), "SR-B should have 1 updated replica (all pods on target revision)")
			Expect(srB.Status.LastConverged).NotTo(BeNil())
			Expect(srB.Status.LastConverged.Revision).To(Equal(revB2))

			By("Verifying SR-A observed rev2, stays Available on old pods, but is NOT Converged")
			Eventually(func() string {
				sr := &corev1alpha1.StackResource{}
				if err := c.Get(ctx, client.ObjectKey{Name: resA, Namespace: stack.Namespace}, sr); err != nil {
					return ""
				}
				return sr.Status.ObservedRevision
			}, readyTimeout, "5s").Should(Equal(revA2))

			srA := &corev1alpha1.StackResource{}
			Expect(c.Get(ctx, client.ObjectKey{Name: resA, Namespace: stack.Namespace}, srA)).To(Succeed())
			Expect(helpers.StackResourceIsAvailable(srA)).To(BeTrue(),
				"SR-A stays Available — the old rev1 ReplicaSet keeps serving while the broken rev2 image can't pull (tolerant availability)")
			convergedA := meta.FindStatusCondition(srA.Status.Conditions, string(corev1alpha1.StackResourceConverged))
			Expect(convergedA).NotTo(BeNil())
			Expect(convergedA.Status).To(Equal(metav1.ConditionFalse),
				"SR-A must NOT be Converged — it never rolled out at rev2")
			Expect(convergedA.Message).To(ContainSubstring("rollout not converged"),
				"Converged condition should include descriptive replica details")
			Expect(convergedA.Message).To(ContainSubstring("unavailable"),
				"Converged condition should mention unavailable replicas")
			Expect(srA.Status.LastConverged).NotTo(BeNil())
			Expect(srA.Status.LastConverged.Revision).To(Equal(revA1),
				"SR-A lastConverged must be sticky at rev1 — it never became Ready at rev2")

			By("Verifying SR-A has Phase=Degraded (serving old revision but new rollout stuck)")
			Expect(srA.Status.Phase).To(Equal(corev1alpha1.StackResourcePhaseDegraded),
				"SR-A should be Degraded — serving traffic on old revision, new rollout not converged")

			By("Verifying SR-A Available condition indicates previous revision")
			availableA := meta.FindStatusCondition(srA.Status.Conditions, string(corev1alpha1.StackResourceStatusAvailable))
			Expect(availableA).NotTo(BeNil())
			Expect(availableA.Status).To(Equal(metav1.ConditionTrue))
			Expect(availableA.Message).To(ContainSubstring("previous revision"),
				"Available message should indicate serving on previous revision")

			By("Verifying SR-A has LastFailureDetails populated from crashing new pods")
			Eventually(func() []corev1alpha1.LastFailureDetail {
				sr := &corev1alpha1.StackResource{}
				if err := c.Get(ctx, client.ObjectKey{Name: resA, Namespace: stack.Namespace}, sr); err != nil {
					return nil
				}
				return sr.Status.LastFailureDetails
			}, readyTimeout, "5s").ShouldNot(BeEmpty(),
				"LastFailureDetails should capture failures from the new ReplicaSet's pods (e.g. ImagePullBackOff)")

			Expect(c.Get(ctx, client.ObjectKey{Name: resA, Namespace: stack.Namespace}, srA)).To(Succeed())
			Expect(srA.Status.LastFailureDetails[0].LastTerminationReason).To(
				BeElementOf("ImagePullBackOff", "ErrImagePull"),
				"Failure reason should be ImagePullBackOff or ErrImagePull")

			By("Verifying SR-A still has available replicas from rev1 (old pods kept alive during failed rollout)")
			Expect(srA.Status.AvailableReplicas).To(BeNumerically(">=", 1),
				"AvailableReplicas should be >= 1: Kubernetes keeps old ReplicaSet pods running when the new image can't start")
			Expect(srA.Status.Replicas).To(BeNumerically(">", srA.Status.AvailableReplicas),
				"Total replicas should exceed available — the broken new pod exists but isn't serving")

			By("Verifying Stack is Progressing with lastConverged still at root-1")
			s := &corev1alpha1.Stack{}
			Expect(c.Get(ctx, client.ObjectKeyFromObject(stack), s)).To(Succeed())
			Expect(s.Status.Phase).To(Equal(corev1alpha1.StackProgressing))
			Expect(s.Status.LastConverged).NotTo(BeNil())
			Expect(s.Status.LastConverged.Revision).To(Equal(root1),
				"Stack lastConverged must remain at root-1 since SR-A never converged at rev2")

			By("Verifying Stack summary reflects the split state")
			for _, summary := range s.Status.Resources {
				switch summary.Name {
				case resA:
					Expect(summary.ObservedRevision).To(Equal(revA2))
					Expect(summary.ConvergedRevision).To(BeEmpty(),
						"SR-A is not converged at rev2, so ConvergedRevision must be empty")
					Expect(summary.LastConverged).NotTo(BeNil())
					Expect(summary.LastConverged.Revision).To(Equal(revA1),
						"SR-A summary.lastConverged sticky at rev1")
					Expect(summary.AvailableReplicas).To(BeNumerically(">=", 1),
						"SR-A summary should show available replicas from the old ReplicaSet")
					Expect(summary.Replicas).To(BeNumerically(">", summary.AvailableReplicas),
						"SR-A total replicas should exceed available (broken new pod exists)")
				case resB:
					Expect(summary.ObservedRevision).To(Equal(revB2))
					Expect(summary.ConvergedRevision).To(Equal(revB2),
						"SR-B is converged at rev2")
					Expect(summary.LastConverged).NotTo(BeNil())
					Expect(summary.LastConverged.Revision).To(Equal(revB2))
					Expect(summary.AvailableReplicas).To(Equal(int32(1)),
						"SR-B summary should show 1 available replica")
					Expect(summary.UpdatedReplicas).To(Equal(int32(1)),
						"SR-B summary should show 1 updated replica")
				}
			}

			By("Verifying conditions: Available=False, ResourcesReady=False, Progressing=True, Degraded=True")
			available := meta.FindStatusCondition(s.Status.Conditions, string(corev1alpha1.StackConditionAvailable))
			Expect(available).NotTo(BeNil())
			Expect(available.Status).To(Equal(metav1.ConditionFalse))

			resourcesReady := meta.FindStatusCondition(s.Status.Conditions, string(corev1alpha1.StackConditionResourcesReady))
			Expect(resourcesReady).NotTo(BeNil())
			Expect(resourcesReady.Status).To(Equal(metav1.ConditionFalse))

			progressing := meta.FindStatusCondition(s.Status.Conditions, string(corev1alpha1.StackConditionProgressing))
			Expect(progressing).NotTo(BeNil())
			Expect(progressing.Status).To(Equal(metav1.ConditionTrue),
				"Progressing must be True — new revision is being rolled out")

			degraded := meta.FindStatusCondition(s.Status.Conditions, string(corev1alpha1.StackConditionDegraded))
			Expect(degraded).NotTo(BeNil())
			Expect(degraded.Status).To(Equal(metav1.ConditionTrue),
				"Degraded must be True — all children are serving traffic (old pods) but not all are converged")
		})

		AfterAll(func() {
			helpers.CleanupStack(ctx, c, stack)
		})
	})

	// Standalone mode: a Stack without revision annotations reaches Ready based purely on
	// child readiness (deploymentConverged). No lastConverged tracking, no targetRevision.
	Context("Standalone stack converges on readiness alone", func() {
		It("should reach Ready without any revision annotations", func() {
			stackName := "standalone-stack"
			resourceName := stackName + "-app"

			swr := fixtures.NewStack(stackName,
				fixtures.NewResource(stackName, resourceName))

			// Create WITHOUT stamping revision annotations (standalone mode)
			Expect(c.Create(ctx, swr.Stack)).To(Succeed())
			Expect(c.Get(ctx, client.ObjectKeyFromObject(swr.Stack), swr.Stack)).To(Succeed())

			sr := swr.Resources[0]
			sr.OwnerReferences = []metav1.OwnerReference{fixtures.OwnerRefTo(swr.Stack)}
			Expect(c.Create(ctx, sr)).To(Succeed())

			By("Waiting for Stack to reach Ready")
			readyStack, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(swr.Stack), readyTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying standalone mode: no targetRevision, no lastConverged")
			Expect(readyStack.Status.TargetRevision).To(BeEmpty())
			Expect(readyStack.Status.LastConverged).To(BeNil())

			By("Cleanup")
			helpers.CleanupStack(ctx, c, swr.Stack)
		})
	})

	Context("MinReadySeconds prevents premature old RS scale-down", Ordered, func() {
		// This test verifies that MinReadySeconds protects the old ReplicaSet during
		// a failed rolling update. Without MinReadySeconds, a pod that starts but
		// crashes within seconds would briefly be marked Ready, causing Kubernetes to
		// consider the rollout complete and scale down the old (healthy) RS. With
		// MinReadySeconds=10, the pod must stay Ready for 10 continuous seconds before
		// the deployment controller counts it as "available." A pod that crashes at 5s
		// never reaches that threshold, so the old RS is preserved.
		const (
			stackName    = "min-ready-stack"
			resourceName = "min-ready-app"
		)
		var stack *corev1alpha1.Stack

		It("should keep old RS alive when new pod crashes within MinReadySeconds window", func() {
			By("Creating a Stack with a working image and an exposed public port")
			// Start with a healthy nginx deployment so we have a known-good RS
			// with addresses populated (external + internal).
			swr := fixtures.NewStack(stackName,
				fixtures.NewResource(stackName, resourceName,
					fixtures.WithImage("nginxinc/nginx-unprivileged:1.25-alpine"),
					fixtures.WithPorts(corev1alpha1.Port{
						Name: "http", Number: 8080, Protocol: "http",
						ExposeToPublic: true, FQDN: resourceName + ".local",
					})))
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack

			By("Waiting for Stack to reach Ready")
			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), readyTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the deployment has MinReadySeconds=10")
			// This is the setting that protects us — without it, the rollout
			// would complete the instant the new pod's container starts.
			dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, resourceName)
			Expect(err).NotTo(HaveOccurred())
			Expect(dep.Spec.MinReadySeconds).To(Equal(int32(10)))

			By("Recording the current external and internal addresses")
			sr := &corev1alpha1.StackResource{}
			Expect(c.Get(ctx, client.ObjectKey{Name: resourceName, Namespace: stack.Namespace}, sr)).To(Succeed())
			Expect(sr.Status.ExternalAddress).NotTo(BeEmpty(), "should have external address before update")
			Expect(sr.Status.InternalAddress).NotTo(BeNil(), "should have internal address before update")

			By("Updating the StackResource to a busybox image that crashes after 5 seconds")
			// sleep 5 + exit 1 means the container runs for 5 seconds, which is
			// within the 10-second MinReadySeconds window. The pod becomes Ready
			// instantly (no readiness probe), but since it crashes before 10s,
			// the deployment controller never counts it as "available." Therefore
			// the old RS (nginx) must NOT be scaled down.
			Eventually(func() error {
				if err := c.Get(ctx, client.ObjectKey{Name: resourceName, Namespace: stack.Namespace}, sr); err != nil {
					return err
				}
				sr.Spec.ImageSpec = &corev1alpha1.ImageSpec{Image: "busybox:1.36"}
				sr.Spec.Command = []string{"sh"}
				sr.Spec.Args = []string{"-c", "sleep 5; exit 1"}
				return c.Update(ctx, sr)
			}, "30s", "1s").Should(Succeed())

			By("Waiting for the new RS to appear with unavailable replicas")
			// Once the deployment controller creates the new RS and the pod starts
			// crashing, we'll see UnavailableReplicas > 0. This confirms the rollout
			// is in progress but stuck.
			Eventually(func() int32 {
				dep, err := helpers.GetDeploymentForResource(ctx, c, stack.Namespace, resourceName)
				if err != nil {
					return 0
				}
				return dep.Status.UnavailableReplicas
			}, readyTimeout, "5s").Should(BeNumerically(">=", 1))

			By("Verifying the old RS still has replicas — MinReadySeconds prevented scale-down")
			// The critical assertion: the old RS must still be serving. If
			// MinReadySeconds were 0, the old RS would already be at 0 replicas
			// because the new pod was briefly Ready before crashing.
			dep, err = helpers.GetDeploymentForResource(ctx, c, stack.Namespace, resourceName)
			Expect(err).NotTo(HaveOccurred())
			Expect(dep.Status.AvailableReplicas).To(BeNumerically(">=", 1),
				"old RS pods must still be available — MinReadySeconds prevented the old RS from being scaled down")

			By("Verifying the deployment is still Available (old pods serving)")
			available := false
			for _, cond := range dep.Status.Conditions {
				if cond.Type == appsv1.DeploymentAvailable && cond.Status == corev1.ConditionTrue {
					available = true
				}
			}
			Expect(available).To(BeTrue(), "deployment should still be Available via old RS")

			By("Verifying StackResource still has addresses (not cleared during failed rollout)")
			Expect(c.Get(ctx, client.ObjectKey{Name: resourceName, Namespace: stack.Namespace}, sr)).To(Succeed())
			Expect(sr.Status.ExternalAddress).NotTo(BeEmpty(),
				"external address must persist — the Service and Ingress still exist")
			Expect(sr.Status.ExternalAddress[0].Address).To(HavePrefix("http://"),
				"external address should have http:// scheme prefix")
			Expect(sr.Status.InternalAddress).NotTo(BeNil(),
				"internal address must persist — the Service still exists")
		})

		AfterAll(func() {
			helpers.CleanupStack(ctx, c, stack)
		})
	})

	Context("Address persistence after runtime crash", Ordered, func() {
		// This test verifies that external and internal addresses persist in the
		// StackResource status even after the workload enters CrashLoopBackOff with
		// no healthy pods. Previously, initializeStatusAndPhase cleared these fields
		// on every reconcile, so a crashing workload would lose its addresses — bad
		// UX for the hub since the URLs disappear even though the Service/Ingress
		// still exist in the cluster.
		//
		// The test uses a marker file in a PVC-backed volume to control crash behavior:
		//   - First run: no marker file exists → create it → sleep 60s → exit 1
		//     This gives MinReadySeconds (10s) time to pass, so the rollout completes,
		//     the svc reconciler runs, and addresses are populated.
		//   - Subsequent runs: marker file exists → sleep 3s → exit 1
		//     This crashes within MinReadySeconds (10s), but since the rollout already
		//     completed (no old RS to protect), the pod just CrashLoopBackOff's. The
		//     deployment goes Available=False. We assert addresses persist.
		const (
			stackName    = "addr-persist-stack"
			resourceName = "addr-persist-app"
			volumeName   = "addr-persist-marker"
		)
		var stack *corev1alpha1.Stack

		It("should preserve addresses when pod enters CrashLoopBackOff after initial convergence", func() {
			By("Creating an empty Volume CR for the marker file")
			// The Volume CRD with no Source and NeedsSyncBeforeUse=false creates a
			// blank PVC. We mount it into the container at /marker so the marker file
			// persists across container restarts (emptyDir equivalent via PVC).
			vol := &storagev1alpha1.Volume{
				ObjectMeta: metav1.ObjectMeta{
					Name:      volumeName,
					Namespace: env.TestNamespace,
				},
				Spec: storagev1alpha1.VolumeSpec{
					Size:               "1Mi",
					AccessMode:         corev1.ReadWriteOnce,
					NeedsSyncBeforeUse: false,
				},
			}
			Expect(c.Create(ctx, vol)).To(Succeed())

			By("Waiting for Volume to be Ready (PVC created)")
			Eventually(func() storagev1alpha1.VolumePhase {
				v := &storagev1alpha1.Volume{}
				if err := c.Get(ctx, client.ObjectKey{Name: volumeName, Namespace: env.TestNamespace}, v); err != nil {
					return ""
				}
				return v.Status.Phase
			}, readyTimeout, "5s").Should(Equal(storagev1alpha1.VolumePhaseReady))

			By("Creating Stack with a resource that uses the marker-file crash pattern")
			// The command implements a two-phase crash pattern:
			//   Phase 1 (first run): Creates /marker/ran, sleeps 60s, then exits.
			//     The 60s sleep exceeds MinReadySeconds (10s), so the deployment
			//     controller considers the rollout complete. The svc reconciler runs,
			//     populates ExternalAddress and InternalAddress.
			//   Phase 2 (restarts): /marker/ran exists, so it sleeps only 3s then exits.
			//     3s < MinReadySeconds (10s), but since the rollout already completed
			//     (there's no old RS), this just causes CrashLoopBackOff. During the
			//     backoff windows, the deployment has 0 Ready pods → Available=False.
			sr := fixtures.NewResource(stackName, resourceName,
				fixtures.WithImage("busybox:1.36"),
				fixtures.WithCommand(
					[]string{"sh"},
					[]string{"-c", "if [ -f /marker/ran ]; then sleep 3; exit 1; fi; touch /marker/ran; sleep 60; exit 1"},
				),
				fixtures.WithPorts(corev1alpha1.Port{
					Name: "http", Number: 8080, Protocol: "http",
					ExposeToPublic: true, FQDN: resourceName + ".local",
				}),
			)
			sr.Spec.VolumeMounts = []corev1alpha1.VolumeMount{
				{SourceVolume: volumeName, Destination: "/marker"},
			}

			swr := fixtures.NewStack(stackName, sr)
			Expect(fixtures.CreateStackWithResources(ctx, c, swr)).To(Succeed())
			stack = swr.Stack

			By("Waiting for Stack to reach Ready (first run: 60s healthy)")
			// During the first 60 seconds the pod is Running and Ready. The deployment
			// passes MinReadySeconds after 10s, the rollout completes, and the svc
			// reconciler creates the Service + Ingress and populates addresses.
			_, err := helpers.WaitForStackReady(ctx, c, client.ObjectKeyFromObject(stack), readyTimeout)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying addresses are populated while healthy")
			sr = &corev1alpha1.StackResource{}
			Expect(c.Get(ctx, client.ObjectKey{Name: resourceName, Namespace: stack.Namespace}, sr)).To(Succeed())
			Expect(sr.Status.ExternalAddress).NotTo(BeEmpty(), "external address should be set after convergence")
			Expect(sr.Status.ExternalAddress[0].Address).To(HavePrefix("http://"),
				"external address should have http:// scheme prefix")
			Expect(sr.Status.InternalAddress).NotTo(BeNil(), "internal address should be set after convergence")

			By("Waiting for the pod to crash and SR to go not-Available")
			// After the first 60s, the container exits. Kubernetes restarts it. The
			// marker file persists in the PVC, so the new container sees /marker/ran
			// and exits after 3s. This is within MinReadySeconds, so the pod is never
			// counted as available. After 2-3 restarts with increasing backoff, there
			// are periods where the deployment has 0 Ready pods.
			Eventually(func() bool {
				sr := &corev1alpha1.StackResource{}
				if err := c.Get(ctx, client.ObjectKey{Name: resourceName, Namespace: stack.Namespace}, sr); err != nil {
					return false
				}
				return !helpers.StackResourceIsAvailable(sr)
			}, readyTimeout, "5s").Should(BeTrue(),
				"SR should become not-Available after the pod enters CrashLoopBackOff")

			By("Verifying addresses persist despite no healthy pods")
			// This is the key assertion. Previously, initializeStatusAndPhase cleared
			// ExternalAddress and InternalAddress on every reconcile, so they would be
			// nil here. After our fix, addresses persist because the Service and Ingress
			// still exist in the cluster — only the pods are down.
			Expect(c.Get(ctx, client.ObjectKey{Name: resourceName, Namespace: stack.Namespace}, sr)).To(Succeed())
			Expect(sr.Status.ExternalAddress).NotTo(BeEmpty(),
				"external address must persist even with no healthy pods — the Service and Ingress still exist")
			Expect(sr.Status.ExternalAddress[0].Address).To(HavePrefix("http://"),
				"scheme prefix must persist")
			Expect(sr.Status.InternalAddress).NotTo(BeNil(),
				"internal address must persist — the Service still exists")
		})

		AfterAll(func() {
			helpers.CleanupStack(ctx, c, stack)
			_ = c.Delete(ctx, &storagev1alpha1.Volume{
				ObjectMeta: metav1.ObjectMeta{Name: volumeName, Namespace: env.TestNamespace},
			})
		})
	})
})
