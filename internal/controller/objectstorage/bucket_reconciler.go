package objectstorage

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"
)

type S3BucketCreator interface {
	CreateBucket(ctx context.Context, bucket string) error
}

type s3BucketCreator struct {
	client *s3.Client
}

func newS3BucketCreator(endpoint, accessKey, secretKey string) *s3BucketCreator {
	cfg := aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
	}
	s3Client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
	return &s3BucketCreator{client: s3Client}
}

func (c *s3BucketCreator) CreateBucket(ctx context.Context, bucket string) error {
	_, err := c.client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		var alreadyOwned *s3types.BucketAlreadyOwnedByYou
		var alreadyExists *s3types.BucketAlreadyExists
		if errors.As(err, &alreadyOwned) || errors.As(err, &alreadyExists) {
			return nil
		}
		return err
	}
	return nil
}

type bucketReconciler struct {
	client        client.Client
	bucketCreator S3BucketCreator
}

func newBucketReconciler(c client.Client) *bucketReconciler {
	return &bucketReconciler{client: c}
}

func (r *bucketReconciler) name() string { return "bucket-reconciler" }

func (r *bucketReconciler) reconcile(ctx context.Context, resource *storagev1alpha1.ObjectStorage) (subReconcilerResult, error) {
	if len(resource.Spec.Buckets) == 0 {
		return resultNil, nil
	}

	logger := log.FromContext(ctx)

	if r.bucketCreator == nil {
		secret := &corev1.Secret{}
		if err := r.client.Get(ctx, client.ObjectKey{
			Name:      resource.Status.CredentialsSecretName,
			Namespace: resource.Namespace,
		}, secret); err != nil {
			return resultNil, err
		}

		accessKey := string(secret.Data[storagev1alpha1.ObjectStorageSecretKeyAWSAccessKey])
		secretKey := string(secret.Data[storagev1alpha1.ObjectStorageSecretKeyAWSSecretKey])

		r.bucketCreator = newS3BucketCreator(resource.Status.Endpoint, accessKey, secretKey)
	}

	resource.Status.Buckets = make([]storagev1alpha1.BucketStatus, 0, len(resource.Spec.Buckets))
	for _, bucket := range resource.Spec.Buckets {
		if err := r.bucketCreator.CreateBucket(ctx, bucket.Name); err != nil {
			logger.Error(err, "Failed to create bucket", "bucket", bucket.Name)
			resource.Status.Buckets = append(resource.Status.Buckets, storagev1alpha1.BucketStatus{
				Name:    bucket.Name,
				Created: false,
			})
			setStatusCondition(resource, storagev1alpha1.ObjectStorageConditionBucketsReady, metav1.ConditionFalse, "BucketCreationFailed", "Failed to create bucket: "+bucket.Name)
			return resultStop, nil
		}
		bucketURL := fmt.Sprintf("%s/%s", resource.Status.Endpoint, bucket.Name)
		resource.Status.Buckets = append(resource.Status.Buckets, storagev1alpha1.BucketStatus{
			Name:    bucket.Name,
			Created: true,
			URL:     bucketURL,
		})
	}

	setStatusCondition(resource, storagev1alpha1.ObjectStorageConditionBucketsReady, metav1.ConditionTrue, "BucketsReady", "All buckets created successfully")
	return resultNil, nil
}
