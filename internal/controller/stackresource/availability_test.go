package stackresource

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"stackdome.io/cluster-agent/api/core/v1alpha1"
)

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
