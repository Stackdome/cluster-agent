package v1alpha1

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func conditionsFixture() []metav1.Condition {
	return []metav1.Condition{{
		Type:               "Available",
		Status:             metav1.ConditionTrue,
		Reason:             "Ready",
		LastTransitionTime: metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
	}}
}

func TestStackResourceStatusHashDoesNotMutateReceiver(t *testing.T) {
	sr := &StackResource{Status: StackResourceStatus{Conditions: conditionsFixture()}}
	_ = sr.StatusHash()
	if sr.Status.Conditions[0].LastTransitionTime.IsZero() {
		t.Fatal("StatusHash zeroed the live object's LastTransitionTime")
	}
}

func TestStackResourceStatusHashIgnoresConditionTimestamps(t *testing.T) {
	a := &StackResource{Status: StackResourceStatus{Phase: StackResourcePhaseReady, Conditions: conditionsFixture()}}
	b := a.DeepCopy()
	b.Status.Conditions[0].LastTransitionTime = metav1.NewTime(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	if a.StatusHash() != b.StatusHash() {
		t.Fatal("hash must be invariant under condition-timestamp changes")
	}
}

func TestStackResourceStatusHashChangesOnPhaseChange(t *testing.T) {
	a := &StackResource{Status: StackResourceStatus{Phase: StackResourcePhasePending}}
	b := a.DeepCopy()
	b.Status.Phase = StackResourcePhaseReady
	if a.StatusHash() == b.StatusHash() {
		t.Fatal("hash must change when status content changes")
	}
}

func TestStackStatusHashDoesNotMutateReceiver(t *testing.T) {
	s := &Stack{Status: StackStatus{Conditions: conditionsFixture()}}
	_ = s.StatusHash()
	if s.Status.Conditions[0].LastTransitionTime.IsZero() {
		t.Fatal("StatusHash zeroed the live object's LastTransitionTime")
	}
}
