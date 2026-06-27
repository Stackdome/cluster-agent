package stackresource

import (
	"context"
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	buildsv1alpha1 "stackdome.io/cluster-agent/api/builds/v1alpha1"
	"stackdome.io/cluster-agent/api/core/v1alpha1"
	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller"
)

const (
	deploymentRevisionAnnotation = "deployment.kubernetes.io/revision"
	// After a new ReplicaSet becomes available, wait this long before declaring the
	// rollout settled. Gives pods time to crash so we can capture failure details.
	deploymentGracePeriodAfterNewReplicaSetAvailable = 3 * time.Minute
)

type DependencyChecker interface {
	DependenciesAvailable(ctx context.Context, resource *v1alpha1.StackResource) (bool, string, error)
	VolumeMountsReadyForUse(ctx context.Context, resource *v1alpha1.StackResource) (bool, string, error)
}

type workloadReconciler struct {
	client.Client
	Scheme            *runtime.Scheme
	DependencyChecker DependencyChecker
	uncachedClient    client.Client
}

// ---------------------------------------------------------------------------
// Labels and naming
// ---------------------------------------------------------------------------

func GetDeploymentNameForResource(resource *v1alpha1.StackResource) string {
	return resource.Name
}

func GetDeploymentLabelForResource(resource *v1alpha1.StackResource) map[string]string {
	return map[string]string{
		"resource": GetDeploymentNameForResource(resource),
	}
}

// identityLabels copies the Stackdome identity labels from the StackResource
// onto child objects (Deployment, Service) so the Stack controller can discover
// them and so they appear in label-based queries.
func identityLabels(resource *v1alpha1.StackResource) map[string]string {
	out := map[string]string{}
	for _, key := range []string{
		v1alpha1.LabelManagedBy,
		v1alpha1.LabelStackName,
		v1alpha1.LabelStackID,
		v1alpha1.LabelResourceName,
		v1alpha1.LabelResourceID,
	} {
		if val, ok := resource.Labels[key]; ok {
			out[key] = val
		}
	}
	return out
}

func mergeLabels(base map[string]string, extra map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// ---------------------------------------------------------------------------
// Image resolution
// ---------------------------------------------------------------------------

func (r *workloadReconciler) getImageBuild(ctx context.Context, resource *v1alpha1.StackResource) (*buildsv1alpha1.ImageBuild, error) {
	existingApplicationBuild := &buildsv1alpha1.ImageBuild{}
	if err := r.Client.Get(ctx,
		types.NamespacedName{
			Name:      buildsv1alpha1.ImageBuildName(resource.Name, resource.Spec.BuildSpec.SourceRevision.GetSourceRevisionString()),
			Namespace: resource.Namespace,
		},
		existingApplicationBuild,
	); err != nil {
		return nil, err
	}
	return existingApplicationBuild, nil
}

func (r *workloadReconciler) getImageForResource(ctx context.Context, resource *v1alpha1.StackResource) (*string, error) {
	if resource.Spec.BuildSpec != nil {
		requiredBuild, err := r.getImageBuild(ctx, resource)
		if err != nil {
			return nil, err
		}
		return ptr.To(requiredBuild.Status.ImageUrl), nil
	}
	return ptr.To(resource.Spec.ImageSpec.Image), nil
}

// ---------------------------------------------------------------------------
// Top-level reconcile: gates → workload type dispatch
// ---------------------------------------------------------------------------

// reconcile runs the pre-flight checks (dependencies, volume mounts, image
// build) and then dispatches to the workload-type-specific reconciler.
func (r *workloadReconciler) reconcile(ctx context.Context, resource *v1alpha1.StackResource) (subReconcilerResult, error) {
	logger := controller.LoggerFromContext(ctx)
	logger.Info("in workload reconciler", "resource", resource.Name)

	// --- Gate: sibling dependencies ---
	canRun, message, err := r.DependencyChecker.DependenciesAvailable(ctx, resource)
	if err != nil {
		return resultNil, err
	}
	if !canRun {
		setResourceCondition(resource, v1alpha1.StackResourceDependenciesReady, false, "DependenciesNotReady", message)
		reportStackResourceNotReady(resource, "DependenciesNotReady", message)
		return resultRequeueAfter(DefaultRequeueTime), nil
	}

	// --- Gate: volume mounts ---
	volumeMountsReady, message, err := r.DependencyChecker.VolumeMountsReadyForUse(ctx, resource)
	if err != nil {
		logger.Error(err, "failed to check if volume mounts are ready for use")
	}
	if !volumeMountsReady {
		setResourceCondition(resource, v1alpha1.StackResourceDependenciesReady, false, "VolumeMountsNotReady", message)
		reportStackResourceNotReady(resource, "VolumeMountsNotReady", message)
		return resultRequeueAfter(DefaultRequeueTime), nil
	}

	setResourceCondition(resource, v1alpha1.StackResourceDependenciesReady, true, "DependenciesReady", "dependencies and volume mounts ready")

	// --- Gate: image build (only for build-from-source resources) ---
	if resource.Spec.BuildSpec != nil {
		currentApplicationBuild, err := r.getImageBuild(ctx, resource)
		if err != nil {
			return resultNil, err
		}
		if !imageBuildComplete(currentApplicationBuild) {
			setResourceCondition(resource, v1alpha1.StackResourceBuildReady, false, "BuildNotReady", "application build is not yet ready")
			reportStackResourceNotReady(resource, "ApplicationBuildNotYetReady", "Application build is not yet ready")
			return resultStop, nil
		}
		setResourceCondition(resource, v1alpha1.StackResourceBuildReady, true, "BuildReady", "application build complete")
	}

	// --- Dispatch by workload type ---
	switch resource.Spec.WorkloadType {
	case v1alpha1.WorkloadTypeService, v1alpha1.WorkloadTypeWorker, "":
		return r.reconcileDeployment(ctx, resource)
	case v1alpha1.WorkloadTypeStatefulService, v1alpha1.WorkloadTypeJob, v1alpha1.WorkloadTypeCronJob:
		reportStackResourceFailed(resource, "WorkloadTypeNotSupported",
			fmt.Sprintf("workload type %q is not yet supported", resource.Spec.WorkloadType))
		return resultStop, nil
	default:
		return resultNil, fmt.Errorf("unknown workload type: %s", resource.Spec.WorkloadType)
	}
}

// ---------------------------------------------------------------------------
// Deployment reconciliation
// ---------------------------------------------------------------------------

func (r *workloadReconciler) reconcileDeployment(ctx context.Context, resource *v1alpha1.StackResource) (subReconcilerResult, error) {
	logger := controller.LoggerFromContext(ctx)

	deployment, restartAnnotationApplied, err := r.applyDeployment(ctx, resource)
	if err != nil {
		return resultNil, err
	}
	if deployment == nil {
		return resultStop, nil
	}

	if restartAnnotationApplied {
		resource.Status.LastRestartRequestProcessedAt = ptr.To(v1.NewTime(time.Now().UTC()))
		reportStackResourceNotReady(resource, "StackResourceDeploymentNotReady", "StackResource deployment restart requested")
		return resultStop, nil
	}

	logger.Info("deployment reconciled")
	return r.evaluateDeploymentStatus(ctx, resource, deployment), nil
}

// applyDeployment resolves inputs and creates or updates the Deployment for a
// StackResource. Returns (nil, nil) when the spec is invalid (terminal failure
// already reported on the resource).
func (r *workloadReconciler) applyDeployment(ctx context.Context, resource *v1alpha1.StackResource) (
	*appsv1.Deployment, bool, error) {
	volumeMountInfo, err := r.getVolumeMountInfoMap(ctx, resource)
	if err != nil {
		return nil, false, err
	}

	image, err := r.getImageForResource(ctx, resource)
	if err != nil {
		return nil, false, err
	}

	envVars := buildEnvVars(resource)
	needsRestart := r.requiresRestart(resource)

	replicas := ptr.To(int32(1))
	if resource.Spec.Replicas != nil {
		replicas = resource.Spec.Replicas
	}

	probes, err := buildProbes(resource)
	if err != nil {
		reportStackResourceFailed(resource, "InvalidSpec", err.Error())
		return nil, false, nil
	}

	deployment := &appsv1.Deployment{
		ObjectMeta: v1.ObjectMeta{
			Name:      GetDeploymentNameForResource(resource),
			Namespace: resource.Namespace,
		},
	}

	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, deployment, func() error {
		r.applyDeploymentSpec(deployment, resource, *image, replicas, envVars, probes, volumeMountInfo, needsRestart)
		if err := r.setImagePullSecret(ctx, resource, deployment); err != nil {
			return err
		}
		return controllerutil.SetControllerReference(resource, deployment, r.Scheme)
	})
	if err != nil {
		return nil, false, err
	}

	return deployment, needsRestart, nil
}

// evaluateDeploymentStatus syncs replica counts, evaluates convergence and
// availability conditions, captures failure details, and returns the
// appropriate sub-reconciler result.
func (r *workloadReconciler) evaluateDeploymentStatus(ctx context.Context, resource *v1alpha1.StackResource, deployment *appsv1.Deployment) subReconcilerResult {
	// --- Sync replica counts ---
	resource.Status.Replicas = deployment.Status.Replicas
	resource.Status.AvailableReplicas = deployment.Status.AvailableReplicas
	resource.Status.UpdatedReplicas = deployment.Status.UpdatedReplicas

	converged := deploymentConverged(deployment)
	serving := deploymentServing(deployment)

	// --- Convergence condition ---
	if converged {
		setResourceCondition(resource, v1alpha1.StackResourceConverged, true, "FullyConverged", "all replicas updated and available on the target revision")
		r.stampLastConverged(resource)
	} else {
		setResourceCondition(resource, v1alpha1.StackResourceConverged, false, "NotConverged", convergenceMessage(deployment))
	}

	// --- Serving: deployment has minimum availability ---
	if serving {
		setResourceCondition(resource, v1alpha1.StackResourceWorkloadAvailable, true, "DeploymentServing", "deployment serving at minimum availability")
		if converged {
			resource.Status.LastFailureDetails = nil
			resource.Status.LastFailureDeploymentRevision = ""
		} else {
			r.captureFailureDetailsOnce(ctx, resource, deployment.Annotations[deploymentRevisionAnnotation])
			if len(resource.Status.LastFailureDetails) == 0 && !controller.DeploymentRolloutSettled(deployment, deploymentGracePeriodAfterNewReplicaSetAvailable) {
				return resultDeferredRequeue(10 * time.Second)
			}
		}
		return resultContinue
	}

	// --- Not serving: capture failures, requeue until settled ---
	controller.LoggerFromContext(ctx).Info("deployment not serving")
	setResourceCondition(resource, v1alpha1.StackResourceWorkloadAvailable, false, "DeploymentNotAvailable", "deployment is not yet available")
	r.captureFailureDetailsOnce(ctx, resource, deployment.Annotations[deploymentRevisionAnnotation])
	reportStackResourceNotReady(resource, "StackResourceDeploymentNotReady", "StackResourceDeploymentNotReady")

	if !controller.DeploymentRolloutSettled(deployment, deploymentGracePeriodAfterNewReplicaSetAvailable) {
		return resultRequeueAfter(10 * time.Second)
	}
	return resultStop
}

// stampLastConverged records the convergence timestamp for the current revision,
// write-once per revision.
func (r *workloadReconciler) stampLastConverged(resource *v1alpha1.StackResource) {
	rev, ok := resource.Annotations[v1alpha1.RevisionAnnotation]
	if !ok {
		return
	}
	if lc := resource.Status.LastConverged; lc == nil || lc.Revision != rev {
		resource.Status.LastConverged = &v1alpha1.StackResourceConvergenceRecord{
			Revision: rev,
			At:       v1.Now(),
		}
	}
}

// ---------------------------------------------------------------------------
// Deployment spec construction
// ---------------------------------------------------------------------------

func (r *workloadReconciler) applyDeploymentSpec(
	deployment *appsv1.Deployment,
	resource *v1alpha1.StackResource,
	image string,
	replicas *int32,
	envVars []corev1.EnvVar,
	probes probeSet,
	volumeMountInfo map[string]*storagev1alpha1.Volume,
	needsRestart bool,
) {
	deployment.ObjectMeta.Labels = mergeLabels(deployment.ObjectMeta.Labels, identityLabels(resource))
	deployment.Spec.Selector = &v1.LabelSelector{
		MatchLabels: GetDeploymentLabelForResource(resource),
	}
	deployment.Spec.Replicas = replicas
	deployment.Spec.Strategy.Type = appsv1.RollingUpdateDeploymentStrategyType
	deployment.Spec.Strategy.RollingUpdate = &appsv1.RollingUpdateDeployment{
		MaxUnavailable: ptr.To(intstr.FromInt32(maxUnavailableForReplicas(*replicas))),
		MaxSurge:       ptr.To(intstr.FromString("25%")),
	}
	deployment.Spec.ProgressDeadlineSeconds = ptr.To(int32(300))

	deployment.Spec.Template.ObjectMeta.Labels = mergeLabels(GetDeploymentLabelForResource(resource), identityLabels(resource))

	// --- Main container ---
	if len(deployment.Spec.Template.Spec.Containers) == 0 {
		deployment.Spec.Template.Spec.Containers = []corev1.Container{{}}
	}
	c := &deployment.Spec.Template.Spec.Containers[0]
	c.Name = resource.Name
	c.Image = image
	c.ImagePullPolicy = resolveImagePullPolicy(resource, image)
	c.TerminationMessagePolicy = corev1.TerminationMessageFallbackToLogsOnError
	c.Command = nilIfEmpty(resource.Spec.Command)
	c.Args = nilIfEmpty(resource.Spec.Args)
	c.Ports = nilIfEmpty(containerPorts(resource))
	c.Env = nilIfEmpty(envVars)
	c.VolumeMounts = nilIfEmpty(volumeMountList(resource))
	c.Resources = corev1.ResourceRequirements{}
	if resource.Spec.Resources != nil {
		c.Resources = *resource.Spec.Resources
	}
	c.ReadinessProbe = probes.readiness
	c.LivenessProbe = probes.liveness
	c.StartupProbe = probes.startup

	// --- Volumes ---
	deployment.Spec.Template.Spec.Volumes = nilIfEmpty(volumesList(resource, volumeMountInfo))

	if resource.Spec.TerminationGracePeriodSeconds != nil {
		deployment.Spec.Template.Spec.TerminationGracePeriodSeconds = resource.Spec.TerminationGracePeriodSeconds
	}

	// --- Init container ---
	if resource.Spec.Init != nil {
		initImage := image
		if resource.Spec.Init.ImageSpec != nil {
			initImage = resource.Spec.Init.ImageSpec.Image
		}
		if len(deployment.Spec.Template.Spec.InitContainers) == 0 {
			deployment.Spec.Template.Spec.InitContainers = []corev1.Container{{}}
		}
		ic := &deployment.Spec.Template.Spec.InitContainers[0]
		ic.Name = resource.InitContainerName()
		ic.Image = initImage
		ic.ImagePullPolicy = resolveImagePullPolicy(resource, initImage)
		ic.TerminationMessagePolicy = corev1.TerminationMessageFallbackToLogsOnError
		ic.Command = nilIfEmpty(resource.Spec.Init.Command)
		ic.Args = nilIfEmpty(resource.Spec.Init.Args)
		ic.Env = nilIfEmpty(envVars)
		ic.VolumeMounts = nilIfEmpty(volumeMountList(resource))
	} else {
		deployment.Spec.Template.Spec.InitContainers = nil
	}

	// --- Security defaults ---
	applySecurityDefaults(&deployment.Spec.Template.Spec, resource.Spec.HardenedSecurityDefaults)

	// --- Restart annotation ---
	if needsRestart {
		if deployment.Spec.Template.Annotations == nil {
			deployment.Spec.Template.Annotations = make(map[string]string)
		}
		deployment.Spec.Template.Annotations[v1alpha1.RestartResourceAnnotation] = v1.Now().UTC().String()
	}
}

// ---------------------------------------------------------------------------
// Status evaluation helpers
// ---------------------------------------------------------------------------

func convergenceMessage(deployment *appsv1.Deployment) string {
	desired := int32(1)
	if deployment.Spec.Replicas != nil {
		desired = *deployment.Spec.Replicas
	}
	msg := fmt.Sprintf("rollout not converged: %d/%d updated, %d/%d ready, %d unavailable",
		deployment.Status.UpdatedReplicas, desired,
		deployment.Status.ReadyReplicas, desired,
		deployment.Status.UnavailableReplicas)
	for _, c := range deployment.Status.Conditions {
		if c.Type == appsv1.DeploymentProgressing && c.Status == corev1.ConditionFalse {
			msg += "; " + c.Reason
			break
		}
	}
	return msg
}

// captureFailureDetailsOnce records crash/error details from pods for the
// current deployment revision. The revision tag prevents redundant pod
// listings on subsequent reconciles.
func (r *workloadReconciler) captureFailureDetailsOnce(ctx context.Context, resource *v1alpha1.StackResource, deploymentRevision string) {
	if resource.Status.LastFailureDeploymentRevision == deploymentRevision {
		return
	}
	failureDetails, err := captureLastFailureDetails(ctx, r.uncachedClient, resource, deploymentRevision)
	if err != nil {
		controller.LoggerFromContext(ctx).Error(err, "failed to capture failure details")
	}
	resource.Status.LastFailureDetails = failureDetails
	if len(resource.Status.LastFailureDetails) > 0 {
		resource.Status.LastFailureDeploymentRevision = deploymentRevision
	}
}

// ---------------------------------------------------------------------------
// Image pull secret
// ---------------------------------------------------------------------------

func (r *workloadReconciler) setImagePullSecret(ctx context.Context, resource *v1alpha1.StackResource, deployment *appsv1.Deployment) error {
	if !resource.NeedsPullSecret() {
		return nil
	}
	secretName, err := r.resolveImagePullSecretName(ctx, resource)
	if err != nil {
		return err
	}
	deployment.Spec.Template.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: secretName}}
	return nil
}

func (r *workloadReconciler) resolveImagePullSecretName(ctx context.Context, resource *v1alpha1.StackResource) (string, error) {
	authType := resource.RegistryAuthType()
	switch authType {
	case v1alpha1.RegistryAuthTypeDockerHub, v1alpha1.RegistryAuthTypeInClusterZotRegistry:
		var secretRef string
		if resource.HasBuildSpec() {
			secretRef = resource.Spec.BuildSpec.Registry.Auth.DockerConfigAuth.SecretRef.Name
		} else {
			secretRef = resource.Spec.ImageSpec.PullAuth.DockerConfigAuth.SecretRef.Name
		}
		secret := &corev1.Secret{}
		if err := r.Client.Get(ctx, types.NamespacedName{Name: secretRef, Namespace: resource.Namespace}, secret); err != nil {
			if apierrors.IsNotFound(err) {
				return "", fmt.Errorf("docker config secret not found: %w", err)
			}
			return "", fmt.Errorf("failed to get docker config secret: %w", err)
		}
		return secret.Name, nil
	default:
		return "", fmt.Errorf("unsupported registry auth type: %s", authType)
	}
}

// ---------------------------------------------------------------------------
// Restart detection
// ---------------------------------------------------------------------------

func (r *workloadReconciler) requiresRestart(resource *v1alpha1.StackResource) bool {
	lastRestartProcessedAt := resource.Status.LastRestartRequestProcessedAt
	currentRestartRequest := resource.Spec.RestartRequest
	switch {
	case currentRestartRequest != nil && lastRestartProcessedAt == nil:
		return true
	case currentRestartRequest != nil && currentRestartRequest.UTC().After(lastRestartProcessedAt.Time.UTC()):
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// Spec mapping helpers
// ---------------------------------------------------------------------------

func buildEnvVars(resource *v1alpha1.StackResource) []corev1.EnvVar {
	res := make([]corev1.EnvVar, 0, len(resource.Spec.EnvironmentVariables))
	for _, env := range resource.Spec.EnvironmentVariables {
		if env.ValueFrom != nil {
			res = append(res, corev1.EnvVar{
				Name: env.Name,
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &env.ValueFrom.SecretKeyRef,
				},
			})
		} else {
			res = append(res, corev1.EnvVar{
				Name:  env.Name,
				Value: env.Value,
			})
		}
	}
	return res
}

type probeSet struct {
	readiness *corev1.Probe
	liveness  *corev1.Probe
	startup   *corev1.Probe
}

func buildProbes(resource *v1alpha1.StackResource) (probeSet, error) {
	hc := resource.Spec.HealthChecks
	if hc == nil {
		return probeSet{}, nil
	}
	readiness, err := buildProbe(hc.Readiness, resource.Spec.Ports)
	if err != nil {
		return probeSet{}, fmt.Errorf("readiness probe: %w", err)
	}
	liveness, err := buildProbe(hc.Liveness, resource.Spec.Ports)
	if err != nil {
		return probeSet{}, fmt.Errorf("liveness probe: %w", err)
	}
	startup, err := buildProbe(hc.Startup, resource.Spec.Ports)
	if err != nil {
		return probeSet{}, fmt.Errorf("startup probe: %w", err)
	}
	return probeSet{readiness: readiness, liveness: liveness, startup: startup}, nil
}

func buildProbe(p *v1alpha1.Probe, ports []v1alpha1.Port) (*corev1.Probe, error) {
	if p == nil {
		return nil, nil
	}
	probe := &corev1.Probe{
		InitialDelaySeconds: p.InitialDelaySeconds,
		PeriodSeconds:       p.PeriodSeconds,
		FailureThreshold:    p.FailureThreshold,
		TimeoutSeconds:      p.TimeoutSeconds,
	}
	switch {
	case p.HTTPGet != nil:
		number, ok := portNumberByName(p.HTTPGet.PortName, ports)
		if !ok {
			return nil, fmt.Errorf("probe references unknown port %q", p.HTTPGet.PortName)
		}
		probe.ProbeHandler = corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{Path: p.HTTPGet.Path, Port: intstr.FromInt32(number)},
		}
	case p.TCPSocket != nil:
		number, ok := portNumberByName(p.TCPSocket.PortName, ports)
		if !ok {
			return nil, fmt.Errorf("probe references unknown port %q", p.TCPSocket.PortName)
		}
		probe.ProbeHandler = corev1.ProbeHandler{
			TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(number)},
		}
	case len(p.Command) > 0:
		probe.ProbeHandler = corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: p.Command}}
	}
	return probe, nil
}

func portNumberByName(name string, ports []v1alpha1.Port) (int32, bool) {
	for _, p := range ports {
		if p.Name == name {
			return p.Number, true
		}
	}
	return 0, false
}

func containerPorts(resource *v1alpha1.StackResource) []corev1.ContainerPort {
	res := make([]corev1.ContainerPort, 0, len(resource.Spec.Ports))
	for _, port := range resource.Spec.Ports {
		res = append(res, corev1.ContainerPort{
			Name:          port.Name,
			ContainerPort: port.Number,
		})
	}
	return res
}

// ---------------------------------------------------------------------------
// Security
// ---------------------------------------------------------------------------

func applySecurityDefaults(podSpec *corev1.PodSpec, hardened *bool) {
	if hardened == nil || !*hardened {
		return
	}
	podSpec.SecurityContext = &corev1.PodSecurityContext{
		RunAsNonRoot: ptr.To(true),
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}
	for i := range podSpec.Containers {
		podSpec.Containers[i].SecurityContext = &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(false),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		}
	}
	for i := range podSpec.InitContainers {
		podSpec.InitContainers[i].SecurityContext = &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(false),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		}
	}
}

// ---------------------------------------------------------------------------
// Volume helpers
// ---------------------------------------------------------------------------

func volumeMountList(resource *v1alpha1.StackResource) []corev1.VolumeMount {
	if len(resource.Spec.VolumeMounts) == 0 {
		return []corev1.VolumeMount{}
	}
	res := make([]corev1.VolumeMount, 0, len(resource.Spec.VolumeMounts))
	for _, mount := range resource.Spec.VolumeMounts {
		vm := corev1.VolumeMount{
			Name:      mount.SourceVolume,
			MountPath: mount.Destination,
			ReadOnly:  mount.ReadOnly,
		}
		if len(mount.SourceSubPath) > 0 {
			vm.SubPath = strings.TrimPrefix(mount.SourceSubPath, "/")
		}
		res = append(res, vm)
	}
	return res
}

func volumesList(resource *v1alpha1.StackResource, volumeInfo map[string]*storagev1alpha1.Volume) []corev1.Volume {
	if len(resource.Spec.VolumeMounts) == 0 {
		return []corev1.Volume{}
	}
	res := make([]corev1.Volume, 0)
	addedVolumes := make(map[string]struct{})
	for _, mount := range resource.Spec.VolumeMounts {
		sourceVolumeName := mount.SourceVolume
		_, added := addedVolumes[sourceVolumeName]
		if !added {
			res = append(res, corev1.Volume{
				Name: sourceVolumeName,
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: volumeInfo[sourceVolumeName].Status.PvcName,
					},
				},
			})
			addedVolumes[sourceVolumeName] = struct{}{}
		}
	}
	return res
}

func (r *workloadReconciler) getVolumeMountInfoMap(ctx context.Context, resource *v1alpha1.StackResource) (map[string]*storagev1alpha1.Volume, error) {
	res := make(map[string]*storagev1alpha1.Volume)
	for _, mount := range resource.Spec.VolumeMounts {
		sourceVolumeName := mount.SourceVolume
		referencedVolume := &storagev1alpha1.Volume{}
		if err := r.Client.Get(ctx, types.NamespacedName{Name: sourceVolumeName, Namespace: resource.Namespace}, referencedVolume); err != nil {
			return nil, fmt.Errorf("failed to get the referenced volume '%s' in resource '%s': %w", sourceVolumeName, resource.Name, err)
		}
		res[sourceVolumeName] = referencedVolume
	}
	return res, nil
}

// ---------------------------------------------------------------------------
// Image pull policy
// ---------------------------------------------------------------------------

// resolveImagePullPolicy returns the pull policy for a container image.
// Precedence: explicit ImageSpec policy > tag-based inference.
// Untagged refs and "latest" use PullAlways; all other tags use PullIfNotPresent.
func resolveImagePullPolicy(resource *v1alpha1.StackResource, image string) corev1.PullPolicy {
	if resource.Spec.ImageSpec != nil && resource.Spec.ImageSpec.ImagePullPolicy != "" {
		return resource.Spec.ImageSpec.ImagePullPolicy
	}

	if i := strings.LastIndex(image, ":"); i < 0 || image[i+1:] == "latest" {
		return corev1.PullAlways
	}
	return corev1.PullIfNotPresent
}

// ---------------------------------------------------------------------------
// Utilities
// ---------------------------------------------------------------------------

func nilIfEmpty[T any](s []T) []T {
	if len(s) == 0 {
		return nil
	}
	return s
}
