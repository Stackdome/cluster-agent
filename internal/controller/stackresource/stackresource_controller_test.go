package stackresource

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"stackdome.io/cluster-agent/api/core/v1alpha1"
)

var _ = Describe("reportStackResourceNotReady", func() {
	It("replaces the stale success reason on the Stalled condition", func() {
		resource := &v1alpha1.StackResource{
			ObjectMeta: metav1.ObjectMeta{Name: "svc-01", Namespace: "ns"},
		}
		reportStackResourceReady(resource)

		reportStackResourceNotReady(resource, "StackResourceDeploymentNotReady", "deployment is not yet available")

		stalled := meta.FindStatusCondition(resource.Status.Conditions, string(v1alpha1.StackResourceStalled))
		Expect(stalled).NotTo(BeNil())
		Expect(stalled.Status).To(Equal(metav1.ConditionFalse), "not-ready is retriable, so Stalled stays False")
		Expect(stalled.Reason).To(Equal("StackResourceDeploymentNotReady"))
		Expect(stalled.Message).To(Equal("deployment is not yet available"))
	})
})
