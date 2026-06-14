package stackresource

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"stackdome.io/cluster-agent/api/core/v1alpha1"
)

func TestDeploymentRolledOut(t *testing.T) {
	tests := []struct {
		name                                    string
		generation, observed, replicas, updated int32
		want                                    bool
	}{
		{"fully rolled out", 2, 2, 3, 3, true},
		{"old pods still present (replicas>updated)", 2, 2, 4, 3, false},
		{"generation not observed", 3, 2, 3, 3, false},
		{"same-revision restart, count dipped together", 2, 2, 2, 2, true},
		{"no pods yet", 1, 1, 0, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Generation: int64(tc.generation)},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: int64(tc.observed),
					Replicas:           tc.replicas,
					UpdatedReplicas:    tc.updated,
				},
			}
			if got := deploymentRolledOut(d); got != tc.want {
				t.Fatalf("deploymentRolledOut = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDeploymentServing(t *testing.T) {
	tests := []struct {
		name                 string
		generation, observed int32
		availCondition       corev1.ConditionStatus
		want                 bool
	}{
		{"available condition true", 2, 2, corev1.ConditionTrue, true},
		{"available condition false", 2, 2, corev1.ConditionFalse, false},
		{"generation not observed", 3, 2, corev1.ConditionTrue, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Generation: int64(tc.generation)},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: int64(tc.observed),
					Conditions: []appsv1.DeploymentCondition{
						{Type: appsv1.DeploymentAvailable, Status: tc.availCondition},
					},
				},
			}
			if got := deploymentServing(d); got != tc.want {
				t.Fatalf("deploymentServing = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDeploymentConverged(t *testing.T) {
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

	if !deploymentConverged(base()) {
		t.Fatal("expected converged for a fully rolled-out, fully available deployment")
	}

	notObserved := base()
	notObserved.Status.ObservedGeneration = 1
	if deploymentConverged(notObserved) {
		t.Fatal("must not be converged when generation is unobserved")
	}

	oneUnavailable := base()
	oneUnavailable.Status.AvailableReplicas = 2
	oneUnavailable.Status.UnavailableReplicas = 1
	if deploymentConverged(oneUnavailable) {
		t.Fatal("must not be converged when a replica is unavailable (strict)")
	}

	notFullyUpdated := base()
	notFullyUpdated.Status.UpdatedReplicas = 2
	if deploymentConverged(notFullyUpdated) {
		t.Fatal("must not be converged when not all replicas are updated")
	}
}

func TestMaxUnavailableForReplicas(t *testing.T) {
	tests := []struct {
		replicas int32
		want     int32
	}{
		{0, 0},
		{1, 0},
		{2, 1},
		{3, 1},
		{4, 1},
		{5, 1},
		{8, 2},
		{10, 2},
		{12, 3},
	}
	for _, tc := range tests {
		if got := maxUnavailableForReplicas(tc.replicas); got != tc.want {
			t.Errorf("maxUnavailableForReplicas(%d) = %d, want %d", tc.replicas, got, tc.want)
		}
	}
}

func TestReportNotReadyClearsConverged(t *testing.T) {
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
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatal("precondition: Converged should be True before reportStackResourceNotReady")
	}

	reportStackResourceNotReady(resource, "DependenciesNotReady", "waiting for db")

	cond = meta.FindStatusCondition(resource.Status.Conditions, string(v1alpha1.StackResourceConverged))
	if cond == nil {
		t.Fatal("Converged condition should exist after reportStackResourceNotReady")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Fatalf("Converged must be False after reportStackResourceNotReady, got %s", cond.Status)
	}
}

func TestReportFailedClearsConverged(t *testing.T) {
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
	if cond == nil {
		t.Fatal("Converged condition should exist after reportStackResourceFailed")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Fatalf("Converged must be False after reportStackResourceFailed, got %s", cond.Status)
	}
}

func TestServingTolerantOfOnePodDownButNotConverged(t *testing.T) {
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
	if !deploymentServing(d) {
		t.Fatal("expected deploymentServing=true (minimum available with one pod down)")
	}
	if deploymentConverged(d) {
		t.Fatal("expected strict deploymentConverged=false (only 2 of 3 available)")
	}
}
