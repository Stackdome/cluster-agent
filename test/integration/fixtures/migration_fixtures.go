package fixtures

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	addonsv1alpha1 "stackdome.io/cluster-agent/api/addons/v1alpha1"
	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
)

func StackWithAllOptionalFields(name string) *corev1alpha1.Stack {
	resourceName := name + "-mig"
	return &corev1alpha1.Stack{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: defaultNamespace,
		},
		Spec: corev1alpha1.StackSpec{
			StackResources: []corev1alpha1.StackResourceTemplate{
				{
					Name: resourceName,
					Spec: corev1alpha1.StackResourceSpec{
						ImageSpec: &corev1alpha1.ImageSpec{
							Image: "nginx:1.25-alpine",
						},
						Ports: []corev1alpha1.Port{
							{
								Number: 80,
								FQDN:   resourceName + ".local",
							},
						},
						EnvironmentVariables: []corev1alpha1.EnvironmentVariables{
							{Name: "MIGRATION_TEST", Value: "true"},
							{Name: "DEBUG", Value: "1"},
						},
						Command: []string{"nginx", "-g", "daemon off;"},
						Args:    []string{"-c", "/etc/nginx/nginx.conf"},
						Init: &corev1alpha1.InitSpec{
							Command: []string{"sh"},
							Args:    []string{"-c", "echo init"},
						},
					},
				},
			},
		},
	}
}

func PostgresClusterWithCustomConfig(name string) *addonsv1alpha1.PostgresCluster {
	pg := SimplePostgresCluster(name)
	pg.Spec.ReplicasSpec.NumSynchronousReplicas = 1
	pg.Spec.ReplicasSpec.SynchronousReplicaDataDurability = addonsv1alpha1.PreferredDataDurability
	pg.Spec.PostgreSQLSpec.PostgresConf = map[string]string{
		"max_connections": "200",
		"shared_buffers":  "256MB",
	}
	return pg
}
