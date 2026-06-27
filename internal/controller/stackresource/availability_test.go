package stackresource

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"stackdome.io/cluster-agent/api/core/v1alpha1"
)

var _ = Describe("deploymentRolledOut", func() {
	DescribeTable("returns the correct result",
		func(generation, observed, replicas, updated int32, want bool) {
			d := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Generation: int64(generation)},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: int64(observed),
					Replicas:           replicas,
					UpdatedReplicas:    updated,
				},
			}
			Expect(deploymentRolledOut(d)).To(Equal(want))
		},
		Entry("fully rolled out", int32(2), int32(2), int32(3), int32(3), true),
		Entry("old pods still present (replicas>updated)", int32(2), int32(2), int32(4), int32(3), false),
		Entry("generation not observed", int32(3), int32(2), int32(3), int32(3), false),
		Entry("same-revision restart, count dipped together", int32(2), int32(2), int32(2), int32(2), true),
		Entry("no pods yet", int32(1), int32(1), int32(0), int32(0), true),
	)
})

var _ = Describe("deploymentServing", func() {
	DescribeTable("returns the correct result",
		func(generation, observed int32, availCondition corev1.ConditionStatus, want bool) {
			d := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Generation: int64(generation)},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: int64(observed),
					Conditions: []appsv1.DeploymentCondition{
						{Type: appsv1.DeploymentAvailable, Status: availCondition},
					},
				},
			}
			Expect(deploymentServing(d)).To(Equal(want))
		},
		Entry("available condition true", int32(2), int32(2), corev1.ConditionTrue, true),
		Entry("available condition false", int32(2), int32(2), corev1.ConditionFalse, false),
		Entry("generation not observed", int32(3), int32(2), corev1.ConditionTrue, false),
	)
})

var _ = Describe("deploymentConverged", func() {
	base := func() *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Generation: 2},
			Spec:       appsv1.DeploymentSpec{Replicas: ptr.To(int32(3))},
			Status: appsv1.DeploymentStatus{
				ObservedGeneration:  2,
				UpdatedReplicas:     3,
				ReadyReplicas:       3,
				AvailableReplicas:   3,
				UnavailableReplicas: 0,
				Conditions: []appsv1.DeploymentCondition{
					{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
				},
			},
		}
	}

	It("should be converged for a fully rolled-out, fully available deployment", func() {
		Expect(deploymentConverged(base())).To(BeTrue())
	})

	It("must not be converged when generation is unobserved", func() {
		notObserved := base()
		notObserved.Status.ObservedGeneration = 1
		Expect(deploymentConverged(notObserved)).To(BeFalse())
	})

	It("must not be converged when a replica is unavailable (strict)", func() {
		oneUnavailable := base()
		oneUnavailable.Status.AvailableReplicas = 2
		oneUnavailable.Status.UnavailableReplicas = 1
		Expect(deploymentConverged(oneUnavailable)).To(BeFalse())
	})

	It("must not be converged when not all replicas are updated", func() {
		notFullyUpdated := base()
		notFullyUpdated.Status.UpdatedReplicas = 2
		Expect(deploymentConverged(notFullyUpdated)).To(BeFalse())
	})
})

var _ = Describe("maxUnavailableForReplicas", func() {
	DescribeTable("returns the correct value",
		func(replicas, want int32) {
			Expect(maxUnavailableForReplicas(replicas)).To(Equal(want))
		},
		Entry("0 replicas", int32(0), int32(0)),
		Entry("1 replica", int32(1), int32(0)),
		Entry("2 replicas", int32(2), int32(1)),
		Entry("3 replicas", int32(3), int32(1)),
		Entry("4 replicas", int32(4), int32(1)),
		Entry("5 replicas", int32(5), int32(1)),
		Entry("8 replicas", int32(8), int32(2)),
		Entry("10 replicas", int32(10), int32(2)),
		Entry("12 replicas", int32(12), int32(3)),
	)
})

var _ = Describe("reportStackResourceNotReady", func() {
	It("should clear Converged condition to False", func() {
		resource := &v1alpha1.StackResource{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "web",
				Generation: 1,
				Annotations: map[string]string{
					v1alpha1.RevisionAnnotation: "rev-1",
				},
			},
		}

		setResourceCondition(resource, v1alpha1.StackResourceConverged, true, "FullyConverged", "all good")
		cond := meta.FindStatusCondition(resource.Status.Conditions, string(v1alpha1.StackResourceConverged))
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionTrue))

		reportStackResourceNotReady(resource, "DependenciesNotReady", "waiting for db")

		cond = meta.FindStatusCondition(resource.Status.Conditions, string(v1alpha1.StackResourceConverged))
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
	})
})

var _ = Describe("reportStackResourceFailed", func() {
	It("should clear Converged condition to False", func() {
		resource := &v1alpha1.StackResource{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "web",
				Generation: 1,
				Annotations: map[string]string{
					v1alpha1.RevisionAnnotation: "rev-1",
				},
			},
		}

		setResourceCondition(resource, v1alpha1.StackResourceConverged, true, "FullyConverged", "all good")

		reportStackResourceFailed(resource, "WorkloadTypeNotSupported", "unsupported")

		cond := meta.FindStatusCondition(resource.Status.Conditions, string(v1alpha1.StackResourceConverged))
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
	})
})

var _ = Describe("serving tolerant of one pod down", func() {
	It("should be serving but not converged when one pod of three is down", func() {
		d := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Generation: 2},
			Spec:       appsv1.DeploymentSpec{Replicas: ptr.To(int32(3))},
			Status: appsv1.DeploymentStatus{
				ObservedGeneration:  2,
				Replicas:            3,
				UpdatedReplicas:     3,
				ReadyReplicas:       2,
				AvailableReplicas:   2,
				UnavailableReplicas: 1,
				Conditions: []appsv1.DeploymentCondition{
					{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
				},
			},
		}
		Expect(deploymentServing(d)).To(BeTrue(), "minimum available with one pod down")
		Expect(deploymentConverged(d)).To(BeFalse(), "only 2 of 3 available")
	})
})
