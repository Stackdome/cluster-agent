package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	barmanapi "github.com/cloudnative-pg/barman-cloud/pkg/api"
	machineryapi "github.com/cloudnative-pg/machinery/pkg/api"
	barmancloudv1 "github.com/cloudnative-pg/plugin-barman-cloud/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	s3MockNamespace   = "s3mock"
	s3MockServiceName = "s3mock"
	s3MockPort        = 9090
	ObjectStoreName   = "s3mock-object-store"
	s3MockBucketName  = "pg-backups"
)

type S3MockManager struct {
	client client.Client
	logger logr.Logger
}

func NewS3MockManager(c client.Client, logger logr.Logger) *S3MockManager {
	return &S3MockManager{client: c, logger: logger}
}

// DeployInfra creates the S3Mock namespace, Deployment, and Service.
// This is cluster-level infrastructure and should run during cluster bootstrap.
func (sm *S3MockManager) DeployInfra(ctx context.Context) error {
	sm.logger.Info("Deploying S3Mock infrastructure")

	// Create s3mock namespace
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: s3MockNamespace},
	}
	if err := sm.client.Create(ctx, ns); err != nil {
		return fmt.Errorf("creating s3mock namespace: %w", err)
	}

	// Deploy S3Mock
	replicas := int32(1)
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "s3mock",
			Namespace: s3MockNamespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "s3mock"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "s3mock"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "s3mock",
							Image: "adobe/s3mock:latest",
							Ports: []corev1.ContainerPort{
								{ContainerPort: int32(s3MockPort), Protocol: corev1.ProtocolTCP},
							},
							Env: []corev1.EnvVar{
								{Name: "initialBuckets", Value: s3MockBucketName},
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/",
										Port: intstr.FromInt(s3MockPort),
									},
								},
								InitialDelaySeconds: 5,
								PeriodSeconds:       5,
							},
						},
					},
				},
			},
		},
	}
	if err := sm.client.Create(ctx, deployment); err != nil {
		return fmt.Errorf("creating s3mock deployment: %w", err)
	}

	// Create Service
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      s3MockServiceName,
			Namespace: s3MockNamespace,
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "s3mock"},
			Ports: []corev1.ServicePort{
				{Port: int32(s3MockPort), TargetPort: intstr.FromInt(s3MockPort), Protocol: corev1.ProtocolTCP},
			},
		},
	}
	if err := sm.client.Create(ctx, svc); err != nil {
		return fmt.Errorf("creating s3mock service: %w", err)
	}

	// Wait for S3Mock to be ready
	sm.logger.Info("Waiting for S3Mock to be ready")
	if err := sm.waitForS3MockReady(ctx); err != nil {
		return fmt.Errorf("waiting for s3mock: %w", err)
	}

	sm.logger.Info("S3Mock infrastructure ready")
	return nil
}

// CreateObjectStore creates the S3 credentials Secret and Barman ObjectStore CR
// in the given test namespace. This is a test prerequisite, not infrastructure.
func (sm *S3MockManager) CreateObjectStore(ctx context.Context, testNamespace string) error {
	sm.logger.Info("Creating S3Mock credentials and ObjectStore", "namespace", testNamespace)

	credentialsSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "s3mock-credentials",
			Namespace: testNamespace,
		},
		StringData: map[string]string{
			"ACCESS_KEY_ID":     "testAccessKey",
			"ACCESS_SECRET_KEY": "testSecretKey",
		},
	}
	if err := sm.client.Create(ctx, credentialsSecret); err != nil {
		return fmt.Errorf("creating s3mock credentials secret: %w", err)
	}

	s3Endpoint := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", s3MockServiceName, s3MockNamespace, s3MockPort)
	objectStore := &barmancloudv1.ObjectStore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ObjectStoreName,
			Namespace: testNamespace,
		},
		Spec: barmancloudv1.ObjectStoreSpec{
			Configuration: barmanapi.BarmanObjectStoreConfiguration{
				DestinationPath: fmt.Sprintf("s3://%s/", s3MockBucketName),
				EndpointURL:     s3Endpoint,
				BarmanCredentials: barmanapi.BarmanCredentials{
					AWS: &barmanapi.S3Credentials{
						AccessKeyIDReference: &machineryapi.SecretKeySelector{
							LocalObjectReference: machineryapi.LocalObjectReference{Name: "s3mock-credentials"},
							Key:                  "ACCESS_KEY_ID",
						},
						SecretAccessKeyReference: &machineryapi.SecretKeySelector{
							LocalObjectReference: machineryapi.LocalObjectReference{Name: "s3mock-credentials"},
							Key:                  "ACCESS_SECRET_KEY",
						},
					},
				},
			},
		},
	}
	if err := sm.client.Create(ctx, objectStore); err != nil {
		return fmt.Errorf("creating barman objectstore: %w", err)
	}

	sm.logger.Info("ObjectStore created", "endpoint", s3Endpoint)
	return nil
}

func (sm *S3MockManager) waitForS3MockReady(ctx context.Context) error {
	timeout := time.After(2 * time.Minute)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-timeout:
			return fmt.Errorf("timed out waiting for s3mock deployment to be ready")
		case <-tick.C:
			dep := &appsv1.Deployment{}
			if err := sm.client.Get(ctx, client.ObjectKey{
				Name:      "s3mock",
				Namespace: s3MockNamespace,
			}, dep); err != nil {
				continue
			}
			if dep.Status.ReadyReplicas >= 1 {
				sm.logger.Info("S3Mock is ready")
				return nil
			}
		}
	}
}
