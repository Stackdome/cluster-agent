package stackresource

import (
	"context"
	"time"

	cmv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	buildsv1alpha1 "stackdome.io/cluster-agent/api/builds/v1alpha1"
	"stackdome.io/cluster-agent/api/core/v1alpha1"
	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller"
	"stackdome.io/cluster-agent/internal/controller/stackresource/workload"
	"stackdome.io/cluster-agent/pkg/portcheck"
)

type subReconcilerResult = controller.SubReconcilerResult

var (
	resultNil             = controller.ResultNil
	resultStop            = controller.ResultStop
	resultRequeue         = controller.ResultRequeue
	resultContinue        = controller.ResultContinue
	resultRequeueAfter    = controller.ResultRequeueAfter
	resultDeferredRequeue = controller.ResultDeferredRequeue
)

type subReconciler interface {
	reconcile(context.Context, *v1alpha1.StackResource) (subReconcilerResult, error)
}

const DefaultRequeueTime = 5 * time.Second

// StackResourceReconciler reconciles a StackResource object
type StackResourceReconciler struct {
	client.Client
	uncachedClient       client.Client
	platformTLSNamespace string
	Scheme               *runtime.Scheme
	subReconcilers       []subReconciler
}

func (r *StackResourceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger = logger.WithValues("stackResource:", req.String())
	logger.Info("in stack resource reconciler")
	ctx = controller.ContextWithLogger(ctx, logger)
	stackResource := &v1alpha1.StackResource{}
	if err := r.Client.Get(ctx, req.NamespacedName, stackResource); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	originalStatus := stackResource.Status.DeepCopy()
	ctx, verdicts := controller.ContextWithVerdicts(ctx)

	res, err := r.reconcile(ctx, stackResource)
	if err != nil {
		return ctrl.Result{}, err
	}
	applicationBuildStatus, err := r.getImageBuildStatus(ctx, stackResource)
	if err != nil {
		return ctrl.Result{}, err
	}
	stackResource.Status.CurrentBuild = applicationBuildStatus

	deriveSummaryStatus(stackResource, verdicts)

	if !equality.Semantic.DeepEqual(originalStatus, &stackResource.Status) {
		logger.Info("updating stack resource status")
		if err := r.Client.Status().Update(ctx, stackResource); err != nil {
			return ctrl.Result{}, err
		}
	}

	return res, nil
}

func (r *StackResourceReconciler) reconcile(ctx context.Context, resource *v1alpha1.StackResource) (ctrl.Result, error) {
	var deferredRequeue *time.Duration
	for _, subReconciler := range r.subReconcilers {
		subReconcilerRes, err := subReconciler.reconcile(ctx, resource)
		switch {
		case err != nil:
			return ctrl.Result{}, err
		case subReconcilerRes == resultStop:
			return ctrl.Result{}, nil
		case subReconcilerRes == resultRequeue:
			return ctrl.Result{Requeue: true}, nil
		case subReconcilerRes.ResultRequeueAfter != nil:
			return ctrl.Result{RequeueAfter: *subReconcilerRes.ResultRequeueAfter}, nil
		}
		if subReconcilerRes.DeferredRequeueAfter != nil {
			deferredRequeue = subReconcilerRes.DeferredRequeueAfter
		}
	}
	if deferredRequeue != nil {
		return ctrl.Result{RequeueAfter: *deferredRequeue}, nil
	}
	return ctrl.Result{}, nil
}

// reportNotReady records a retriable not-ready verdict for this pass.
// deriveSummaryStatus turns verdicts into the summary conditions.
func reportNotReady(ctx context.Context, reason, msg string) {
	controller.VerdictsFromContext(ctx).ReportNotReady(reason, msg)
}

// reportFailed records a terminal failure verdict for this pass.
func reportFailed(ctx context.Context, reason, msg string) {
	controller.VerdictsFromContext(ctx).ReportFailed(reason, msg)
}

func setResourceCondition(resource *v1alpha1.StackResource, condType v1alpha1.StackResourceDomainCondition, ready bool, reason, msg string) {
	condStatus := metav1.ConditionFalse
	if ready {
		condStatus = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&resource.Status.Conditions, metav1.Condition{
		Type:               string(condType),
		Status:             condStatus,
		ObservedGeneration: resource.Generation,
		Reason:             reason,
		Message:            msg,
	})
}

func (r *StackResourceReconciler) getImageBuildStatus(ctx context.Context, resource *v1alpha1.StackResource) (*v1alpha1.BuildStatus, error) {
	if resource.Spec.BuildSpec == nil {
		return nil, nil
	}

	existingImageBuild := &buildsv1alpha1.ImageBuild{}
	if err := r.Client.Get(ctx,
		types.NamespacedName{
			Name:      buildsv1alpha1.ImageBuildName(resource.Name, resource.Spec.BuildSpec),
			Namespace: resource.Namespace,
		},
		existingImageBuild,
	); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}

	res := &v1alpha1.BuildStatus{
		Name:           existingImageBuild.Name,
		SourceRevision: existingImageBuild.Status.BuildSourceRevision,
		Phase:          string(existingImageBuild.Status.Phase),
	}

	availableCond := meta.FindStatusCondition(existingImageBuild.Status.Conditions, string(buildsv1alpha1.BuildAvailable))
	if availableCond != nil {
		res.Available = availableCond.Status == metav1.ConditionTrue
		res.Message = availableCond.Message
		res.Reason = availableCond.Reason
	} else {
		res.Available = false
	}
	return res, nil
}

func stackResourceAvailable(resource *v1alpha1.StackResource) bool {
	availableCond := meta.FindStatusCondition(resource.Status.Conditions, string(v1alpha1.StackResourceStatusAvailable))
	if availableCond != nil &&
		availableCond.Status == metav1.ConditionTrue &&
		availableCond.ObservedGeneration == resource.Generation {
		return true
	}
	return false
}

type workloadSubReconciler struct{ inner *workload.Reconciler }

func (w workloadSubReconciler) reconcile(ctx context.Context, r *v1alpha1.StackResource) (subReconcilerResult, error) {
	return w.inner.Reconcile(ctx, r)
}

type defaultStatusReporter struct{}

func (defaultStatusReporter) ReportNotReady(ctx context.Context, r *v1alpha1.StackResource, reason, msg string) {
	reportNotReady(ctx, reason, msg)
}

func (defaultStatusReporter) ReportFailed(ctx context.Context, r *v1alpha1.StackResource, reason, msg string) {
	reportFailed(ctx, reason, msg)
}

func (defaultStatusReporter) SetCondition(r *v1alpha1.StackResource, t v1alpha1.StackResourceDomainCondition, ready bool, reason, msg string) {
	setResourceCondition(r, t, ready, reason, msg)
}

// StackResourceReconcilerOpts carries the tunables the controller needs beyond
// its clients. Grouping them keeps the constructor readable as options accrue.
type StackResourceReconcilerOpts struct {
	ImageBuildHistoryLimit int
	PlatformTLSNamespace   string
	// PortVerifier proves declared ports are actually listening. Optional: a nil
	// verifier disables the check.
	PortVerifier *portcheck.Verifier
	// PortCheckGrace bounds requeueing while a verification is outstanding.
	PortCheckGrace time.Duration
}

func NewStackResourceReconciler(client client.Client, scheme *runtime.Scheme, uncachedClient client.Client, opts StackResourceReconcilerOpts) *StackResourceReconciler {
	platformTLSNamespace := opts.PlatformTLSNamespace
	if platformTLSNamespace == "" {
		platformTLSNamespace = DefaultPlatformTLSNamespace
	}
	w := &StackResourceReconciler{
		Client:               client,
		uncachedClient:       uncachedClient,
		platformTLSNamespace: platformTLSNamespace,
		Scheme:               scheme,
	}

	depChecker := &workloadDependencyChecker{
		Client: client,
	}
	subReconcilers := []subReconciler{
		&registryAuthReconciler{
			client: client,
			scheme: scheme,
		},
		&imageBuildReconciler{
			Client:       client,
			scheme:       scheme,
			historyLimit: opts.ImageBuildHistoryLimit,
		},
		workloadSubReconciler{inner: workload.NewReconciler(
			client, scheme, depChecker, uncachedClient, defaultStatusReporter{},
			opts.PortVerifier, opts.PortCheckGrace,
		)},
		&svcReconciler{
			Client:               client,
			Scheme:               scheme,
			platformTLSNamespace: platformTLSNamespace,
		},
	}
	w.subReconcilers = subReconcilers
	return w
}

func imageBuildFailed(imageBuild *buildsv1alpha1.ImageBuild) bool {
	return imageBuild.Status.Phase == buildsv1alpha1.BuildPhaseFailed
}

func imageBuildComplete(imageBuild *buildsv1alpha1.ImageBuild) bool {
	availableCond := meta.FindStatusCondition(imageBuild.Status.Conditions, string(buildsv1alpha1.BuildAvailable))
	if availableCond != nil && availableCond.Status == metav1.ConditionTrue {
		return true
	}
	return false
}

// SetupWithManager sets up the controller with the Manager.
func (r *StackResourceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.StackResource{}).
		Watches(&buildsv1alpha1.ImageBuild{}, handler.EnqueueRequestForOwner(r.Scheme, mgr.GetRESTMapper(), &v1alpha1.StackResource{})).
		Watches(&corev1.Service{}, handler.EnqueueRequestForOwner(r.Scheme, mgr.GetRESTMapper(), &v1alpha1.StackResource{})).
		Watches(&networkingv1.Ingress{}, handler.EnqueueRequestForOwner(r.Scheme, mgr.GetRESTMapper(), &v1alpha1.StackResource{})).
		Watches(&cmv1.Certificate{}, handler.EnqueueRequestsFromMapFunc(certificateToStackResource)).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.platformWildcardTLSSecretToStackResources),
			builder.WithPredicates(platformWildcardTLSSecretPredicate(r.platformTLSNamespace)),
		).
		Watches(&appsv1.Deployment{}, handler.EnqueueRequestForOwner(r.Scheme, mgr.GetRESTMapper(), &v1alpha1.StackResource{})).
		Watches(&appsv1.StatefulSet{}, handler.EnqueueRequestForOwner(r.Scheme, mgr.GetRESTMapper(), &v1alpha1.StackResource{})).
		Watches(&batchv1.Job{}, handler.EnqueueRequestForOwner(r.Scheme, mgr.GetRESTMapper(), &v1alpha1.StackResource{})).
		Watches(&batchv1.CronJob{}, handler.EnqueueRequestForOwner(r.Scheme, mgr.GetRESTMapper(), &v1alpha1.StackResource{})).
		Watches(&storagev1alpha1.Volume{}, handler.EnqueueRequestsFromMapFunc(
			func(ctx context.Context, o client.Object) []reconcile.Request {
				volume := o.(*storagev1alpha1.Volume)
				res := []reconcile.Request{}
				if volume.Spec.Source != nil && len(volume.Spec.Source.BuildArtifacts) != 0 {
					for _, artifact := range volume.Spec.Source.BuildArtifacts {
						res = append(res, reconcile.Request{
							NamespacedName: types.NamespacedName{
								Namespace: volume.Namespace,
								Name:      artifact.BuildSource.Name,
							},
						})
					}
				}
				return res
			},
		)).
		Complete(r)
}
