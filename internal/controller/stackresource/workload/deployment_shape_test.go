package workload

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	"stackdome.io/cluster-agent/api/core/v1alpha1"
	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"
)

func specShapeResource() *v1alpha1.StackResource {
	return &v1alpha1.StackResource{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "shape-test",
			Namespace:  "test-ns",
			Generation: 1,
			Labels: map[string]string{
				v1alpha1.LabelManagedBy:    "stackdome",
				v1alpha1.LabelStackName:    "s1",
				v1alpha1.LabelResourceName: "shape-test",
			},
		},
		Spec: v1alpha1.StackResourceSpec{
			WorkloadType:                  v1alpha1.WorkloadTypeService,
			ImageSpec:                     &v1alpha1.ImageSpec{Image: "repo/app:v1.2.3"},
			Replicas:                      ptr.To(int32(2)),
			Command:                       []string{"/bin/app"},
			Args:                          []string{"--flag"},
			TerminationGracePeriodSeconds: ptr.To(int64(45)),
			HardenedSecurityDefaults:      ptr.To(true),
			Ports:                         []v1alpha1.Port{{Name: "http", Number: 8080, Protocol: "http"}},
			EnvironmentVariables: []v1alpha1.EnvironmentVariable{
				{Name: "PLAIN", Value: "v"},
				{Name: "SECRET", ValueFrom: &v1alpha1.EnvVarSource{SecretKeyRef: corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "sec"}, Key: "k"}}},
			},
			VolumeMounts: []v1alpha1.VolumeMount{{SourceVolume: "data", Destination: "/data"}},
			HealthChecks: &v1alpha1.HealthChecks{Readiness: &v1alpha1.Probe{HTTPGet: &v1alpha1.HTTPGetProbe{PortName: "http", Path: "/healthz"}}},
			Init:         &v1alpha1.InitSpec{Command: []string{"/bin/init"}},
		},
	}
}

var _ = Describe("Deployment spec shape (regression guard)", Ordered, func() {
	var dep *appsv1.Deployment
	res := specShapeResource()
	name := res.Name

	BeforeAll(func() {
		volumeMountInfo := map[string]*storagev1alpha1.Volume{
			"data": {Status: storagev1alpha1.VolumeStatus{PvcName: "data-pvc"}},
		}
		probes, err := buildProbes(res)
		Expect(err).NotTo(HaveOccurred())

		r := &Reconciler{}
		dep = &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: res.Namespace}}
		r.applyDeploymentSpec(dep, res, "repo/app:v1.2.3", res.Spec.Replicas,
			buildEnvVars(res), probes, volumeMountInfo, false)
	})

	It("has RollingUpdate strategy maxUnavailable=1 maxSurge=25%", func() {
		Expect(dep.Spec.Strategy.Type).To(Equal(appsv1.RollingUpdateDeploymentStrategyType))
		Expect(dep.Spec.Strategy.RollingUpdate).NotTo(BeNil())
		Expect(*dep.Spec.Strategy.RollingUpdate.MaxUnavailable).To(Equal(intstr.FromInt32(1)))
		Expect(*dep.Spec.Strategy.RollingUpdate.MaxSurge).To(Equal(intstr.FromString("25%")))
	})

	It("has ProgressDeadlineSeconds=300 MinReadySeconds=10", func() {
		Expect(*dep.Spec.ProgressDeadlineSeconds).To(Equal(int32(300)))
		Expect(dep.Spec.MinReadySeconds).To(Equal(int32(workloadMinReadySeconds)))
	})

	It("has replicas from spec", func() {
		Expect(*dep.Spec.Replicas).To(Equal(int32(2)))
	})

	It("has selector matching only {resource: name}", func() {
		Expect(dep.Spec.Selector).NotTo(BeNil())
		Expect(dep.Spec.Selector.MatchLabels).To(Equal(map[string]string{"resource": name}))
	})

	It("has pod-template labels: resource + identity", func() {
		labels := dep.Spec.Template.ObjectMeta.Labels
		// resource selector label
		Expect(labels).To(HaveKeyWithValue("resource", name))
		// identity labels propagated from the StackResource
		Expect(labels).To(HaveKeyWithValue(v1alpha1.LabelManagedBy, "stackdome"))
		Expect(labels).To(HaveKeyWithValue(v1alpha1.LabelStackName, "s1"))
		Expect(labels).To(HaveKeyWithValue(v1alpha1.LabelResourceName, "shape-test"))
	})

	It("has correct main container", func() {
		Expect(dep.Spec.Template.Spec.Containers).To(HaveLen(1))
		c := dep.Spec.Template.Spec.Containers[0]
		Expect(c.Name).To(Equal(name))
		Expect(c.Image).To(Equal("repo/app:v1.2.3"))
		Expect(c.ImagePullPolicy).To(Equal(corev1.PullIfNotPresent))
		Expect(c.TerminationMessagePolicy).To(Equal(corev1.TerminationMessageFallbackToLogsOnError))
		Expect(c.Command).To(Equal([]string{"/bin/app"}))
		Expect(c.Args).To(Equal([]string{"--flag"}))
		Expect(c.Ports).To(HaveLen(1))
		Expect(c.Ports[0].Name).To(Equal("http"))
		Expect(c.Ports[0].ContainerPort).To(Equal(int32(8080)))
	})

	It("maps env vars literal + secretKeyRef", func() {
		c := dep.Spec.Template.Spec.Containers[0]
		Expect(c.Env).To(HaveLen(2))

		Expect(c.Env[0].Name).To(Equal("PLAIN"))
		Expect(c.Env[0].Value).To(Equal("v"))
		Expect(c.Env[0].ValueFrom).To(BeNil())

		Expect(c.Env[1].Name).To(Equal("SECRET"))
		Expect(c.Env[1].Value).To(BeEmpty())
		Expect(c.Env[1].ValueFrom).NotTo(BeNil())
		Expect(c.Env[1].ValueFrom.SecretKeyRef).NotTo(BeNil())
		Expect(c.Env[1].ValueFrom.SecretKeyRef.Name).To(Equal("sec"))
		Expect(c.Env[1].ValueFrom.SecretKeyRef.Key).To(Equal("k"))
	})

	It("sets readiness probe on the mapped port", func() {
		c := dep.Spec.Template.Spec.Containers[0]
		Expect(c.ReadinessProbe).NotTo(BeNil())
		Expect(c.ReadinessProbe.HTTPGet).NotTo(BeNil())
		Expect(c.ReadinessProbe.HTTPGet.Path).To(Equal("/healthz"))
		Expect(c.ReadinessProbe.HTTPGet.Port).To(Equal(intstr.FromInt32(8080)))
	})

	It("mounts the PVC-backed volume", func() {
		c := dep.Spec.Template.Spec.Containers[0]
		Expect(c.VolumeMounts).To(HaveLen(1))
		Expect(c.VolumeMounts[0].Name).To(Equal("data"))
		Expect(c.VolumeMounts[0].MountPath).To(Equal("/data"))

		Expect(dep.Spec.Template.Spec.Volumes).To(HaveLen(1))
		Expect(dep.Spec.Template.Spec.Volumes[0].Name).To(Equal("data"))
		Expect(dep.Spec.Template.Spec.Volumes[0].PersistentVolumeClaim).NotTo(BeNil())
		Expect(dep.Spec.Template.Spec.Volumes[0].PersistentVolumeClaim.ClaimName).To(Equal("data-pvc"))
	})

	It("sets terminationGracePeriodSeconds", func() {
		Expect(dep.Spec.Template.Spec.TerminationGracePeriodSeconds).NotTo(BeNil())
		Expect(*dep.Spec.Template.Spec.TerminationGracePeriodSeconds).To(Equal(int64(45)))
	})

	It("has init container {resource}-init with shared env+volumes", func() {
		Expect(dep.Spec.Template.Spec.InitContainers).To(HaveLen(1))
		ic := dep.Spec.Template.Spec.InitContainers[0]
		Expect(ic.Name).To(Equal(name + "-init"))
		Expect(ic.Image).To(Equal("repo/app:v1.2.3"))
		Expect(ic.Command).To(Equal([]string{"/bin/init"}))
		// init container shares env vars and volume mounts with main container
		Expect(ic.Env).To(Equal(dep.Spec.Template.Spec.Containers[0].Env))
		Expect(ic.VolumeMounts).To(Equal(dep.Spec.Template.Spec.Containers[0].VolumeMounts))
	})

	It("applies hardened security defaults", func() {
		podSec := dep.Spec.Template.Spec.SecurityContext
		Expect(podSec).NotTo(BeNil())
		Expect(*podSec.RunAsNonRoot).To(BeTrue())
		Expect(podSec.SeccompProfile).NotTo(BeNil())
		Expect(podSec.SeccompProfile.Type).To(Equal(corev1.SeccompProfileTypeRuntimeDefault))

		c := dep.Spec.Template.Spec.Containers[0]
		Expect(c.SecurityContext).NotTo(BeNil())
		Expect(*c.SecurityContext.AllowPrivilegeEscalation).To(BeFalse())
		Expect(c.SecurityContext.Capabilities).NotTo(BeNil())
		Expect(c.SecurityContext.Capabilities.Drop).To(ConsistOf(corev1.Capability("ALL")))

		ic := dep.Spec.Template.Spec.InitContainers[0]
		Expect(ic.SecurityContext).NotTo(BeNil())
		Expect(*ic.SecurityContext.AllowPrivilegeEscalation).To(BeFalse())
		Expect(ic.SecurityContext.Capabilities).NotTo(BeNil())
		Expect(ic.SecurityContext.Capabilities.Drop).To(ConsistOf(corev1.Capability("ALL")))
	})

	It("stamps a restart annotation only when needsRestart", func() {
		// The Deployment built with needsRestart=false should have no restart annotation.
		Expect(dep.Spec.Template.Annotations).NotTo(HaveKey(v1alpha1.RestartResourceAnnotation))

		// Build a second Deployment with needsRestart=true and verify the annotation is set.
		volumeMountInfo := map[string]*storagev1alpha1.Volume{
			"data": {Status: storagev1alpha1.VolumeStatus{PvcName: "data-pvc"}},
		}
		probes, err := buildProbes(res)
		Expect(err).NotTo(HaveOccurred())
		restartDep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: res.Namespace}}
		r := &Reconciler{}
		r.applyDeploymentSpec(restartDep, res, "repo/app:v1.2.3", res.Spec.Replicas,
			buildEnvVars(res), probes, volumeMountInfo, true)
		Expect(restartDep.Spec.Template.Annotations).To(HaveKey(v1alpha1.RestartResourceAnnotation))
	})
})
