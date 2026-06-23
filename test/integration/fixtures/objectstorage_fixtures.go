package fixtures

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"
)

// SimpleObjectStorage creates a minimal ObjectStorage CR without buckets.
func SimpleObjectStorage(name string) *storagev1alpha1.ObjectStorage {
	return &storagev1alpha1.ObjectStorage{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: defaultNamespace,
		},
		Spec: storagev1alpha1.ObjectStorageSpec{
			Capacity:         defaultStorageSize,
			StorageClassName: stringPtr(defaultStorageClass),
			Ingress: storagev1alpha1.ObjectStorageIngressSpec{
				Hostname: name + ".example.com",
				TLS:      true,
			},
		},
	}
}

// ObjectStorageWithBuckets creates an ObjectStorage CR with predefined buckets.
func ObjectStorageWithBuckets(name string) *storagev1alpha1.ObjectStorage {
	os := SimpleObjectStorage(name)
	os.Spec.Buckets = []storagev1alpha1.BucketSpec{
		{Name: "uploads"},
		{Name: "backups"},
		{Name: "static-assets"},
	}
	return os
}

// ObjectStorageWithCustomCapacity creates an ObjectStorage CR with custom capacity.
func ObjectStorageWithCustomCapacity(name, capacity string) *storagev1alpha1.ObjectStorage {
	os := SimpleObjectStorage(name)
	os.Spec.Capacity = capacity
	return os
}

func stringPtr(s string) *string {
	return &s
}
