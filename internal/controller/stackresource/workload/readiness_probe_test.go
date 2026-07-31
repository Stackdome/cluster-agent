package workload

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"stackdome.io/cluster-agent/api/core/v1alpha1"
)

var _ = Describe("buildProbes default readiness synthesis", func() {
	newResource := func(wt v1alpha1.WorkloadType, ports []v1alpha1.Port, hc *v1alpha1.HealthChecks) *v1alpha1.StackResource {
		return &v1alpha1.StackResource{Spec: v1alpha1.StackResourceSpec{
			WorkloadType: wt,
			Ports:        ports,
			HealthChecks: hc,
		}}
	}

	Context("when a serving workload declares ports and no readiness check", func() {
		It("synthesizes a TCP probe on the public port", func() {
			resource := newResource(v1alpha1.WorkloadTypeService, []v1alpha1.Port{
				{Name: "metrics", Number: 9090},
				{Name: "http", Number: 8080, ExposeToPublic: true},
			}, nil)

			probes, err := buildProbes(resource)

			Expect(err).NotTo(HaveOccurred())
			Expect(probes.readiness).NotTo(BeNil())
			Expect(probes.readiness.TCPSocket).NotTo(BeNil())
			Expect(probes.readiness.TCPSocket.Port.IntValue()).To(Equal(8080))
		})

		It("never synthesizes liveness or startup probes", func() {
			resource := newResource(v1alpha1.WorkloadTypeService,
				[]v1alpha1.Port{{Name: "http", Number: 8080, ExposeToPublic: true}}, nil)

			probes, err := buildProbes(resource)

			Expect(err).NotTo(HaveOccurred())
			Expect(probes.liveness).To(BeNil(), "a slow starter must be held out of endpoints, not restart-looped")
			Expect(probes.startup).To(BeNil())
		})

		It("falls back to the first port when none is public", func() {
			resource := newResource(v1alpha1.WorkloadTypeStatefulService, []v1alpha1.Port{
				{Name: "grpc", Number: 50051},
				{Name: "metrics", Number: 9090},
			}, nil)

			probes, err := buildProbes(resource)

			Expect(err).NotTo(HaveOccurred())
			Expect(probes.readiness.TCPSocket.Port.IntValue()).To(Equal(50051))
		})

		It("uses a grace window long enough for a slow boot", func() {
			resource := newResource(v1alpha1.WorkloadTypeService,
				[]v1alpha1.Port{{Name: "http", Number: 8080}}, nil)

			probes, err := buildProbes(resource)

			Expect(err).NotTo(HaveOccurred())
			grace := probes.readiness.PeriodSeconds * probes.readiness.FailureThreshold
			Expect(grace).To(BeNumerically(">=", 300), "must tolerate a five-minute JVM or Rails boot")
		})
	})

	Context("when the user declares their own readiness check", func() {
		It("preserves it untouched", func() {
			resource := newResource(v1alpha1.WorkloadTypeService,
				[]v1alpha1.Port{{Name: "http", Number: 8080, ExposeToPublic: true}},
				&v1alpha1.HealthChecks{Readiness: &v1alpha1.Probe{
					HTTPGet: &v1alpha1.HTTPGetProbe{Path: "/healthz", PortName: "http"},
				}})

			probes, err := buildProbes(resource)

			Expect(err).NotTo(HaveOccurred())
			Expect(probes.readiness.HTTPGet).NotTo(BeNil())
			Expect(probes.readiness.TCPSocket).To(BeNil())
		})
	})

	Context("when the workload has nothing to serve", func() {
		It("synthesizes nothing for non-serving workload types", func() {
			for _, wt := range []v1alpha1.WorkloadType{
				v1alpha1.WorkloadTypeWorker,
				v1alpha1.WorkloadTypeJob,
				v1alpha1.WorkloadTypeCronJob,
			} {
				probes, err := buildProbes(newResource(wt, []v1alpha1.Port{{Name: "http", Number: 8080}}, nil))

				Expect(err).NotTo(HaveOccurred())
				Expect(probes.readiness).To(BeNil(), "workload type %s must not get a probe", wt)
			}
		})

		It("synthesizes nothing when no ports are declared", func() {
			probes, err := buildProbes(newResource(v1alpha1.WorkloadTypeService, nil, nil))

			Expect(err).NotTo(HaveOccurred())
			Expect(probes.readiness).To(BeNil())
		})
	})
})
