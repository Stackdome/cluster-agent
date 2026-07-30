package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"os"
	"strings"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	clientgocorev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/utils/ptr"

	cmv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	barmancloudv1 "github.com/cloudnative-pg/plugin-barman-cloud/api/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	addonsv1alpha1 "stackdome.io/cluster-agent/api/addons/v1alpha1"
	buildsv1alpha1 "stackdome.io/cluster-agent/api/builds/v1alpha1"
	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
	registryv1alpha1 "stackdome.io/cluster-agent/api/registry/v1alpha1"
	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"
	usersv1alpha1 "stackdome.io/cluster-agent/api/users/v1alpha1"
	postgrescontroller "stackdome.io/cluster-agent/internal/controller/addons/postgrescluster"
	clusterinfocontroller "stackdome.io/cluster-agent/internal/controller/clusterinfo"
	"stackdome.io/cluster-agent/internal/controller/errorpages"
	"stackdome.io/cluster-agent/internal/controller/imagebuild"
	nfsservercontroller "stackdome.io/cluster-agent/internal/controller/nfsserver"
	objectstoragecontroller "stackdome.io/cluster-agent/internal/controller/objectstorage"
	"stackdome.io/cluster-agent/internal/controller/stack"
	"stackdome.io/cluster-agent/internal/controller/stackresource"
	"stackdome.io/cluster-agent/internal/controller/stackresource/workload"
	"stackdome.io/cluster-agent/internal/controller/volume"
	"stackdome.io/cluster-agent/internal/controller/workspaceuser"
	"stackdome.io/cluster-agent/pkg/config"
	"stackdome.io/cluster-agent/pkg/execute"
	"stackdome.io/cluster-agent/pkg/portcheck"
	"stackdome.io/cluster-agent/pkg/registry/zotregistry"
	"stackdome.io/cluster-agent/pkg/rwmany_provisioner"

	registry "stackdome.io/cluster-agent/internal/controller/registry"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

const (
	// fallbackErrorPagesNamespace is where the agent runs unless the pod tells it
	// otherwise through POD_NAMESPACE.
	fallbackErrorPagesNamespace = "stackdome-control-plane"
	// defaultErrorPagesSelector matches the agent Deployment shipped in
	// config/deploy. Helm installs label their pods differently and pass
	// --error-pages-selector explicitly.
	defaultErrorPagesSelector = "app.kubernetes.io/name=stackdome-operator"
)

// defaultErrorPagesNamespace prefers the namespace the agent actually runs in,
// so the Traefik objects land next to the pods the Service selects.
var defaultErrorPagesNamespace = func() string {
	if ns := os.Getenv("POD_NAMESPACE"); ns != "" {
		return ns
	}
	return fallbackErrorPagesNamespace
}()

// errEmptyErrorPagesSelector marks the one parseSelector outcome that silently
// breaks the error pages, so it is logged with the raw flag value rather than
// dropped.
var errEmptyErrorPagesSelector = errors.New("error page pod selector parsed to an empty label set")

// parseSelector turns "k1=v1,k2=v2" into a label map. Malformed entries are
// dropped rather than fatal: a wrong selector must not stop the agent. An
// all-malformed value yields an empty map, which errorpages.Ensure rejects —
// an empty Service selector matches every pod in the namespace.
func parseSelector(raw string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(raw, ",") {
		key, value, found := strings.Cut(strings.TrimSpace(pair), "=")
		if !found || key == "" {
			continue
		}
		out[key] = value
	}
	return out
}

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(corev1alpha1.AddToScheme(scheme))
	utilruntime.Must(registryv1alpha1.AddToScheme(scheme))
	utilruntime.Must(buildsv1alpha1.AddToScheme(scheme))
	utilruntime.Must(usersv1alpha1.AddToScheme(scheme))
	utilruntime.Must(storagev1alpha1.AddToScheme(scheme))
	utilruntime.Must(cnpgv1.AddToScheme(scheme))
	utilruntime.Must(barmancloudv1.AddToScheme(scheme))
	utilruntime.Must(addonsv1alpha1.AddToScheme(scheme))
	utilruntime.Must(cmv1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var imageBuildHistoryLimit int
	var portCheckGrace time.Duration
	var errorPagesAddr string
	var errorPagesNamespace string
	var errorPagesSelector string
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.StringVar(&errorPagesAddr, "error-pages-bind-address", errorpages.DefaultAddr,
		"The address the error page server binds to.")
	flag.StringVar(&errorPagesNamespace, "error-pages-namespace", defaultErrorPagesNamespace,
		"Namespace the error page Service, Middleware, and IngressRoute are created in. Defaults to POD_NAMESPACE when set.")
	flag.StringVar(&errorPagesSelector, "error-pages-selector", defaultErrorPagesSelector,
		"Comma-separated key=value pod selector of the agent's own Deployment, used by the error page Service.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", false,
		"If set the metrics endpoint is served securely")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	flag.IntVar(&imageBuildHistoryLimit, "image-build-history-limit", 5,
		"Number of completed/cancelled ImageBuilds to retain per StackResource.")
	flag.DurationVar(&portCheckGrace, "port-check-grace", workload.DefaultPortCheckGrace,
		"How long a StackResource keeps being requeued while its declared-port verification is still outstanding.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}

	tlsOpts := []func(*tls.Config){}
	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	webhookServer := webhook.NewServer(webhook.Options{
		TLSOpts: tlsOpts,
	})

	registryBuilder := zotregistry.NewZotRegistry(zotregistry.ZotRegistryOpts{
		RegistryImage:                 config.ZotImage,
		RegistryConfigReconcilerImage: config.RegistryConfigReconcilerImage,
		GCDelay:                       "1h",
		// Run GC every 6h rather than 24h so the Kaniko build cache cannot
		// accumulate a full day of large per-commit layers between sweeps.
		GCInterval:       "6h",
		EnableGC:         true,
		RegistryLogLevel: "info",
		LayerCachingOpts: zotregistry.LayerCachingOpts{
			Enabled: true,
			// Kaniko cache repos are nested (e.g. <ns>/<app>/<app>/cache), so the
			// glob must use the globstar "**" (matches across "/"). Plain "*/cache"
			// matches only a single segment and never matches the real path, causing
			// the cache repo to fall through to the catch-all "**" policy.
			RepoGlobPatterns: []string{"**/cache", "**/cache/**"},
			TagsToKeep:       10,
			// Keep cache layers reused within a week hot so repeated builds stay
			// fast; idle caches age out and free their storage.
			PulledWithin: "168h",
		},
	})

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress:   metricsAddr,
			SecureServing: secureMetrics,
			TLSOpts:       tlsOpts,
		},
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "66da0ac8.stackdome.io",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	uncachedClient, err := client.New(
		mgr.GetConfig(), client.Options{Scheme: mgr.GetScheme(), Mapper: mgr.GetRESTMapper()})
	if err != nil {
		setupLog.Error(err, "unable to set up uncached client")
		os.Exit(1)
	}

	// One verifier for the whole process: its worker pool bounds concurrent
	// dialing globally, and its result cache is shared across reconciles.
	portVerifier := portcheck.NewVerifier(portcheck.DefaultWorkers, portcheck.DefaultDialTimeout)
	if err = mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		portVerifier.Start(ctx)
		<-ctx.Done()
		return nil
	})); err != nil {
		setupLog.Error(err, "unable to start port verifier")
		os.Exit(1)
	}

	stackResourceController := stackresource.NewStackResourceReconciler(
		mgr.GetClient(), mgr.GetScheme(), uncachedClient,
		stackresource.StackResourceReconcilerOpts{
			ImageBuildHistoryLimit: imageBuildHistoryLimit,
			PortVerifier:           portVerifier,
			PortCheckGrace:         portCheckGrace,
		})
	if err = stackResourceController.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "WorkspaceResource")
		os.Exit(1)
	}

	if err = (&imagebuild.ImageBuildReconciler{
		Client:         mgr.GetClient(),
		Scheme:         mgr.GetScheme(),
		UncachedClient: uncachedClient,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "WorkspaceApplicationBuild")
		os.Exit(1)
	}

	if err = stack.NewStackReconciler(mgr.GetClient(), mgr.GetScheme()).
		SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "stack")
		os.Exit(1)
	}

	if err = (&volume.VolumeReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "volume")
		os.Exit(1)
	}
	if err = (&workspaceuser.WorkspaceUserReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "WorkspaceUser")
		os.Exit(1)
	}

	if err = registry.NewRegistryReconciler(
		mgr.GetClient(),
		mgr.GetScheme(),
		registryBuilder).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Registry")
		os.Exit(1)
	}

	storageClassOpts := nfsservercontroller.StorageClassOpts{
		Name:            config.StackdomeRWManyStorageClassName,
		ProvisionerName: config.StackdomeRWManyProvisionerName,
		ReclaimPolicy:   ptr.To(clientgocorev1.PersistentVolumeReclaimDelete),
		MountOptions:    []string{},
	}

	storageReconciler, err := nfsservercontroller.NewNFSServerReconciler(
		uncachedClient,
		mgr.GetClient(),
		mgr.GetScheme(),
		storageClassOpts,
		config.NfsServerImage,
	)
	if err != nil {
		setupLog.Error(err, "unable to create storageReconciler", "reconciler", "NFSServer")
		os.Exit(1)
	}

	if err := storageReconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "NFSServer")
		os.Exit(1)
	}

	objectStorageReconciler := objectstoragecontroller.NewObjectStorageReconciler(
		mgr.GetClient(),
		mgr.GetScheme(),
		config.RustFSImage,
	)
	if err := objectStorageReconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "ObjectStorage")
		os.Exit(1)
	}

	podExecuter, err := execute.NewPodExecuter(ctrl.GetConfigOrDie())
	if err != nil {
		setupLog.Error(err, "unable to create podexecuter", "controller", "NFSServer")
		os.Exit(1)
	}

	clientset, err := kubernetes.NewForConfig(ctrl.GetConfigOrDie())
	if err != nil {
		setupLog.Error(err, "unable to create k8s clientset", "controller", "NFSServer")
		os.Exit(1)
	}

	stackdomeNfsManyProvisioner, err := rwmany_provisioner.NewStackdomeRWManyProvisioner(
		context.Background(),
		rwmany_provisioner.StackdomeRWManyProvisionerSpec{
			ClientSet:       clientset,
			ProvisionerName: config.StackdomeRWManyProvisionerName,
			PodExecuter:     podExecuter,
			Logger:          setupLog,
			Client:          mgr.GetClient(),
		})
	if err != nil {
		setupLog.Error(err, "unable to create stackdomeNfsManyProvisioner", "controller", "NFSServer")
		os.Exit(1)
	}

	mgr.Add(stackdomeNfsManyProvisioner)

	postgresAddonController := postgrescontroller.NewPostgresAddonReconciler(postgrescontroller.PostgresClusterReconcilerSpec{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	})

	if err = postgresAddonController.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "PostgresCluster")
		os.Exit(1)
	}

	if err = (&clusterinfocontroller.ClusterInfoReconciler{}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "ClusterInfo")
		os.Exit(1)
	}

	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	if err := mgr.Add(errorpages.Runnable(errorPagesAddr)); err != nil {
		setupLog.Error(err, "unable to start the error page server")
		os.Exit(1)
	}

	// The Service and Traefik objects are cluster-wide singletons, but they are
	// not optional: Traefik applies the stackdome-errors middleware to every
	// router, and a router whose middleware does not resolve serves a 500. So
	// the objects are ensured by a Runnable that retries until it succeeds
	// rather than once at start-up with a log-and-continue. The uncached client
	// is used because the objects are Traefik CRs the manager's cache has no
	// informer for.
	errorPagesPodSelector := parseSelector(errorPagesSelector)
	if len(errorPagesPodSelector) == 0 {
		// Ensure refuses an empty selector, so this only ever costs the error
		// pages — but the raw value is the one thing needed to fix it.
		setupLog.Error(errEmptyErrorPagesSelector, "--error-pages-selector parsed to no labels; error pages will not be created",
			"value", errorPagesSelector)
	}
	if err := mgr.Add(errorpages.EnsureRunnable(uncachedClient, errorPagesNamespace, errorPagesPodSelector)); err != nil {
		setupLog.Error(err, "unable to schedule error page setup")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
