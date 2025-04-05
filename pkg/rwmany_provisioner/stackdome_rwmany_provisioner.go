package rwmany_provisioner

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/kubectl/pkg/polymorphichelpers"
	"k8s.io/kubectl/pkg/util/podutils"
	"sigs.k8s.io/controller-runtime/pkg/client"
	externalprovisioner "sigs.k8s.io/sig-storage-lib-external-provisioner/v11/controller"
	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"
)

type PodExecuter interface {
	ExecuteCommand(ctx context.Context, targetPod *corev1.Pod, containerName string, command []string) (string, error)
}

const (
	// Annotations for tracking provisioning details
	ProvisionedByAnnotation = "stackdome.io/provisioned-by"
	NamespaceAnnotation     = "stackdome.io/nfs-server-namespace"
)

type stackdomeRWManyProvisioner struct {
	podExecuter PodExecuter
	logger      logr.Logger
	client      client.Client
	controller  *externalprovisioner.ProvisionController
}

type StackdomeRWManyProvisionerSpec struct {
	ClientSet       kubernetes.Interface
	ProvisionerName string
	PodExecuter     PodExecuter
	Logger          logr.Logger
	Client          client.Client
}

func NewStackdomeRWManyProvisioner(ctx context.Context, spec StackdomeRWManyProvisionerSpec) (*stackdomeRWManyProvisioner, error) {
	s := &stackdomeRWManyProvisioner{
		podExecuter: spec.PodExecuter,
		logger:      spec.Logger,
		client:      spec.Client,
	}

	provisionController := externalprovisioner.NewProvisionController(
		ctx,
		spec.ClientSet,
		spec.ProvisionerName,
		s,
		externalprovisioner.LeaderElection(true),
	)

	s.controller = provisionController
	return s, nil
}

func (r *stackdomeRWManyProvisioner) Start(ctx context.Context) error {
	r.controller.Run(ctx)
	return nil
}

func (r *stackdomeRWManyProvisioner) Provision(ctx context.Context, options externalprovisioner.ProvisionOptions) (*corev1.PersistentVolume, externalprovisioner.ProvisioningState, error) {
	nfsServerSelector, err := metav1.LabelSelectorAsSelector(options.PVC.Spec.Selector)
	if err != nil {
		return nil, externalprovisioner.ProvisioningFinished, fmt.Errorf("failed to convert label selector: %w", err)
	}

	if !contains(options.PVC.Spec.AccessModes, corev1.ReadWriteMany) {
		return nil, externalprovisioner.ProvisioningFinished, fmt.Errorf("RWMany access mode is not requested")
	}

	nfsServerList := storagev1alpha1.NFSServerList{}
	listOpts := &client.ListOptions{
		Namespace:     options.PVC.Namespace,
		LabelSelector: nfsServerSelector,
	}
	if err := r.client.List(ctx, &nfsServerList, listOpts); err != nil {
		return nil, externalprovisioner.ProvisioningFinished, fmt.Errorf("failed to list nfs Servers in pvc namespace %s: %w", options.PVC.Namespace, err)
	}

	if len(nfsServerList.Items) == 0 {
		return nil, externalprovisioner.ProvisioningFinished, fmt.Errorf("no nfs servers found for this pvc")
	}

	selectedNFSServer := r.selectNFServer(ctx, &nfsServerList, options.PVC)
	if !nfsServerAvailable(selectedNFSServer) {
		return nil, externalprovisioner.ProvisioningInBackground, fmt.Errorf("backing nfs server not yet available")
	}

	nfsServerPod, err := r.findNFSServerPod(ctx, selectedNFSServer)
	if err != nil || nfsServerPod == nil {
		return nil, externalprovisioner.ProvisioningInBackground, fmt.Errorf("failed to get nfs server pod: %w", err)
	}

	// Create a subdirectory for this pvc in the nfs server.
	pvcDir, err := r.provisionDirectoryForPVC(ctx, selectedNFSServer, nfsServerPod, &options)
	if err != nil {
		return nil, externalprovisioner.ProvisioningInBackground, err
	}

	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: options.PVName,
			Annotations: map[string]string{
				ProvisionedByAnnotation: selectedNFSServer.Name,
				NamespaceAnnotation:     selectedNFSServer.Namespace,
			},
		},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeReclaimPolicy: *options.StorageClass.ReclaimPolicy,
			AccessModes:                   options.PVC.Spec.AccessModes,
			MountOptions:                  options.StorageClass.MountOptions,
			Capacity: corev1.ResourceList{
				corev1.ResourceName(corev1.ResourceStorage): options.PVC.Spec.Resources.Requests[corev1.ResourceName(corev1.ResourceStorage)],
			},
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				NFS: &corev1.NFSVolumeSource{
					Server: selectedNFSServer.Status.NFSServerURL,
					// NFS servers consider the path starting from the exported directory.
					// So "{selectedNFSServer.Spec.ExportedDir}/{pvcDir}" => "/{pvcDir}"
					Path:     fmt.Sprintf("/%s", pvcDir),
					ReadOnly: false,
				},
			},
		},
	}
	return pv, externalprovisioner.ProvisioningFinished, nil
}

// When the pv provisioned by us gets deleted, we
// 1. Find the nfs server which provisioned this PV
// 2. Delete the directory corresponding to this PV
func (r *stackdomeRWManyProvisioner) Delete(ctx context.Context, volume *corev1.PersistentVolume) error {
	backingNfsServerName, ok := volume.Annotations[ProvisionedByAnnotation]
	if !ok {
		return fmt.Errorf("missing nfs server details in pv")
	}
	backingNfsServerNS, ok := volume.Annotations[NamespaceAnnotation]
	if !ok {
		return fmt.Errorf("missing nfs server details in pv")
	}

	backingNfsServer := &storagev1alpha1.NFSServer{}
	if err := r.client.Get(ctx, types.NamespacedName{
		Name:      backingNfsServerName,
		Namespace: backingNfsServerNS,
	}, backingNfsServer); err != nil {
		return fmt.Errorf("failed to fetch backing NFS server: %w", err)
	}

	if volume.Spec.PersistentVolumeSource.NFS == nil || len(volume.Spec.PersistentVolumeSource.NFS.Path) == 0 {
		return fmt.Errorf("failed to delete pv: missing nfs server details in the provisioned volume")
	}

	pvDirPathInNfsServer := filepath.Join(backingNfsServer.Spec.ExportDir, volume.Spec.PersistentVolumeSource.NFS.Path)

	nfsServerPod, err := r.findNFSServerPod(ctx, backingNfsServer)
	if err != nil || nfsServerPod == nil {
		if nfsServerPod == nil {
			return fmt.Errorf("missing nfs server pod")
		}
		return fmt.Errorf("failed to get nfs server pod: %w", err)
	}

	dirExists, err := r.checkIfPathExistsInPod(ctx, nfsServerPod, pvDirPathInNfsServer)
	if err != nil {
		return fmt.Errorf("failed to check if pv exists in nfs server: %w", err)
	}

	if !dirExists {
		// NOOP, path is already removed from the backing nfs server.
		return nil
	}

	switch volume.Spec.PersistentVolumeReclaimPolicy {
	case corev1.PersistentVolumeReclaimDelete:
		return r.deletePVDirectory(ctx, nfsServerPod, pvDirPathInNfsServer)
	case corev1.PersistentVolumeReclaimRecycle:
		return r.cleanUpPVDirectory(ctx, nfsServerPod, pvDirPathInNfsServer)
	default:
		// NOOP otherwise
		return nil
	}
}

func (r *stackdomeRWManyProvisioner) deletePVDirectory(ctx context.Context, nfsServerPod *corev1.Pod, pvPath string) error {
	rmCmd := []string{
		"/bin/sh",
		"-c",
		fmt.Sprintf("rm -rf %s", pvPath),
	}
	_, err := r.podExecuter.ExecuteCommand(ctx, nfsServerPod, "", rmCmd)
	return err
}

func (r *stackdomeRWManyProvisioner) cleanUpPVDirectory(ctx context.Context, nfsServerPod *corev1.Pod, pvPath string) error {
	rmCmd := []string{
		"/bin/sh",
		"-c",
		fmt.Sprintf("rm -rf %s/*", pvPath),
	}
	_, err := r.podExecuter.ExecuteCommand(ctx, nfsServerPod, "", rmCmd)
	return err
}

func (r *stackdomeRWManyProvisioner) findNFSServerPod(ctx context.Context, nfsServer *storagev1alpha1.NFSServer) (*corev1.Pod, error) {
	nfsServerSvc := &corev1.Service{}
	if err := r.client.Get(ctx, types.NamespacedName{
		Name:      nfsServer.Name,
		Namespace: nfsServer.Namespace,
	}, nfsServerSvc); err != nil {
		return nil, fmt.Errorf("failed to get nfs server svc: %w", err)
	}
	_, selector, err := polymorphichelpers.SelectorsForObject(nfsServerSvc)
	if err != nil {
		return nil, fmt.Errorf("failed to get nfs server svc selector: %w", err)
	}

	sortBy := func(pods []*corev1.Pod) sort.Interface { return sort.Reverse(podutils.ActivePods(pods)) }

	podList := corev1.PodList{}

	listOpts := &client.ListOptions{
		Namespace:     nfsServer.Namespace,
		LabelSelector: selector,
	}

	if err := r.client.List(ctx, &podList, listOpts); err != nil {
		return nil, fmt.Errorf("failed to find nfs server pod: %w", err)
	}

	if len(podList.Items) == 0 {
		return nil, fmt.Errorf("no nfs server pod found")
	}

	var sortedPods []*corev1.Pod

	for _, pod := range podList.Items {
		sortedPods = append(sortedPods, &pod)
	}
	sort.Sort(sortBy(sortedPods))

	return sortedPods[0], nil
}

func (r *stackdomeRWManyProvisioner) provisionDirectoryForPVC(ctx context.Context,
	selectedNfsServer *storagev1alpha1.NFSServer,
	nfsServerPod *corev1.Pod,
	options *externalprovisioner.ProvisionOptions) (string, error) {
	pvcSubDirectoryName := strings.Join([]string{options.PVC.Name, options.PVName}, "-")
	pvcPathInNFSServer := filepath.Join(selectedNfsServer.Spec.ExportDir, pvcSubDirectoryName)
	cmd := []string{
		"/bin/sh",
		"-c",
		fmt.Sprintf("mkdir -p %s && chmod %d %s", pvcPathInNFSServer, 777, pvcPathInNFSServer),
	}

	_, err := r.podExecuter.ExecuteCommand(ctx, nfsServerPod, "", cmd)
	if err != nil {
		return "", fmt.Errorf("failed to create subdirectory inside nfs server for pvc '%s' : %w", options.PVC.Name, err)
	}
	return pvcSubDirectoryName, nil
}

// TODO: Support provisioning pvcs accross multiple NFS servers in a namespace. For now we always select the first one.
func (r *stackdomeRWManyProvisioner) selectNFServer(ctx context.Context, nfsServerList *storagev1alpha1.NFSServerList, pvc *corev1.PersistentVolumeClaim) *storagev1alpha1.NFSServer {
	return &nfsServerList.Items[0]
}

func (r *stackdomeRWManyProvisioner) checkIfPathExistsInPod(ctx context.Context, pod *corev1.Pod, path string) (bool, error) {
	// Command to check if a path exists (test -e returns 0/success if the path exists)
	checkCommand := []string{
		"/bin/sh",
		"-c",
		fmt.Sprintf("test -e %s && echo 'exists' || echo 'not_exists'", path),
	}

	output, err := r.podExecuter.ExecuteCommand(ctx, pod, "", checkCommand)
	if err != nil {
		return false, fmt.Errorf("failed to check if path exists: %w", err)
	}

	output = strings.TrimSpace(output)
	return output == "exists", nil
}

func contains[T comparable](items []T, item T) bool {
	for _, k := range items {
		if k == item {
			return true
		}
	}
	return false
}

func nfsServerAvailable(nfsServer *storagev1alpha1.NFSServer) bool {
	availableCond := meta.FindStatusCondition(nfsServer.Status.Conditions, storagev1alpha1.NFSServerConditionTypeAvailable)
	return availableCond != nil && availableCond.Status == metav1.ConditionTrue
}
