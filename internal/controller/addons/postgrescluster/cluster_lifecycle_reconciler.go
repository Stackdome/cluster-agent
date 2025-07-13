package postgrescluster

import (
	"context"
	"time"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	cnpghibernation "github.com/cloudnative-pg/cloudnative-pg/pkg/reconciler/hibernation"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	addonsv1alpha1 "stackdome.io/cluster-agent/api/addons/v1alpha1"
)

const (
	fencedInstanceAnnotation = "cnpg.io/fencedInstances"
)

type clusterLifecycleReconciler struct {
	client client.Client
	scheme *runtime.Scheme
}

func newClusterLifecycleReconciler(client client.Client, scheme *runtime.Scheme) *clusterLifecycleReconciler {
	return &clusterLifecycleReconciler{
		client: client,
		scheme: scheme,
	}
}

func (r *clusterLifecycleReconciler) name() string {
	return "cluster-lifecycle-reconciler"
}

func (r *clusterLifecycleReconciler) reconcile(ctx context.Context, resource *addonsv1alpha1.PostgresCluster) (subReconcilerResult, error) {
	cnpgCluster := &cnpgv1.Cluster{}
	if err := r.client.Get(ctx, client.ObjectKey{
		Name:      resource.CnpgClusterName(),
		Namespace: resource.Namespace,
	}, cnpgCluster); err != nil {
		return resultNil, err
	}

	// Reconcile hibernation
	res, err := r.reconcileClusterHibernation(ctx, resource, cnpgCluster)
	if err != nil {
		return resultNil, err
	}
	if res != resultNil {
		return res, nil
	}

	// Reconcile fencing
	res, err = r.reconcileClusterFencing(ctx, resource, cnpgCluster)
	if err != nil {
		return resultNil, err
	}
	if res != resultNil {
		return res, nil
	}

	return resultNil, nil
}

func (r *clusterLifecycleReconciler) reconcileClusterHibernation(ctx context.Context, resource *addonsv1alpha1.PostgresCluster, cnpgCluster *cnpgv1.Cluster) (subReconcilerResult, error) {
	var currentHibernationValue string
	if resource.Spec.HibernationEnabled {
		currentHibernationValue = string(cnpghibernation.HibernationOn)
	} else {
		currentHibernationValue = string(cnpghibernation.HibernationOff)
	}

	if cnpgCluster.Annotations == nil {
		cnpgCluster.Annotations = make(map[string]string)
	}
	existingHibernationValue, ok := cnpgCluster.Annotations[cnpghibernation.HibernationConditionType]
	if !ok || existingHibernationValue != currentHibernationValue {
		cnpgCluster.Annotations[cnpghibernation.HibernationConditionType] = currentHibernationValue
		return resultRequeue, r.client.Update(ctx, cnpgCluster)
	}

	if currentHibernationValue == string(cnpghibernation.HibernationOn) {
		hibernatedCond := meta.FindStatusCondition(cnpgCluster.Status.Conditions, cnpghibernation.HibernationConditionType)
		if hibernatedCond != nil && hibernatedCond.Status == metav1.ConditionTrue {
			setStatusCondition(resource, addonsv1alpha1.ClusterHiberated, metav1.ConditionTrue, "ClusterHibernated", "The cluster is currently hibernated")
			// return resultNil, r.client.Status().Update(ctx, resource)
			return resultNil, nil

		}
		// We wait for the hibernation condition to be set to true.
		return resultStop, nil
	}

	if currentHibernationValue == string(cnpghibernation.HibernationOff) {
		hibernatedCond := meta.FindStatusCondition(cnpgCluster.Status.Conditions, cnpghibernation.HibernationConditionType)
		if hibernatedCond == nil || hibernatedCond.Status == metav1.ConditionFalse {
			meta.RemoveStatusCondition(&resource.Status.Conditions, string(addonsv1alpha1.ClusterHiberated))
			// return resultNil, r.client.Status().Update(ctx, resource)
			return resultNil, nil
		}
		// We wait for the hibernation condition to be set to false or for it to go missing.
		return resultStop, nil
	}
	return resultNil, nil
}

func (r *clusterLifecycleReconciler) reconcileClusterFencing(ctx context.Context, resource *addonsv1alpha1.PostgresCluster, cnpgCluster *cnpgv1.Cluster) (subReconcilerResult, error) {
	if resource.Spec.FencingSpec != nil && resource.Spec.FencingSpec.FenceCluster {
		clusterFencedAnnotationValue := `'["*"]'`
		if cnpgCluster.Annotations == nil {
			cnpgCluster.Annotations = make(map[string]string)
		}
		existingValue, ok := cnpgCluster.Annotations[fencedInstanceAnnotation]
		if !ok || existingValue != clusterFencedAnnotationValue {
			cnpgCluster.Annotations[fencedInstanceAnnotation] = clusterFencedAnnotationValue
			// We requeue after a delay  to ensure the annotation is applied.
			// We only set the Fenced status condition after 30s to allow the cnpg controller to process the annotation.
			return resultRequeueAfter(time.Second * 30), r.client.Update(ctx, cnpgCluster)
		}
		setStatusCondition(resource, addonsv1alpha1.ClusterFenced, metav1.ConditionTrue, "ClusterFenced", "The cluster is currently fenced")
		// return resultNil, r.client.Status().Update(ctx, resource)
		return resultNil, nil

	}

	// Fencing is disabled, we remove the fenced annotation if it exists.
	if _, ok := cnpgCluster.Annotations[fencedInstanceAnnotation]; ok {
		delete(cnpgCluster.Annotations, fencedInstanceAnnotation)
		// We requeue after a delay to ensure the annotation is removed.
		return resultRequeueAfter(time.Second * 30), r.client.Update(ctx, cnpgCluster)
	}
	if removed := meta.RemoveStatusCondition(&resource.Status.Conditions, string(addonsv1alpha1.ClusterFenced)); removed {
		// return resultNil, r.client.Status().Update(ctx, resource)
		return resultNil, nil

	}
	return resultNil, nil
}
