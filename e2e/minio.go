package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/testcontainers/testcontainers-go/modules/minio"
)

type minIOTestStore struct {
	endpoint  string
	accessKey string
	secretKey string
	bucket    string
}

// Ensure minIOTestStore implements TestStore
var _ TestStore = (*minIOTestStore)(nil)

func newMinIOTestStore(t *testing.T) *minIOTestStore {
	ctx := context.Background()

	// Spin up MinIO container
	minioContainer, err := minio.Run(ctx, "minio/minio:latest")
	if err != nil {
		t.Fatalf("failed to start minio container: %v", err)
	}

	// Clean up container after test finished
	t.Cleanup(func() {
		if err := minioContainer.Terminate(context.Background()); err != nil {
			t.Logf("failed to terminate minio container: %v", err)
		}
	})

	endpoint, err := minioContainer.Endpoint(ctx, "")
	if err != nil {
		t.Fatalf("failed to get minio endpoint: %v", err)
	}
	// The endpoint comes back without http scheme
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "http://" + endpoint
	}

	accessKey := minioContainer.Username
	secretKey := minioContainer.Password

	bucket := fmt.Sprintf("cloudstic-e2e-%d", time.Now().UnixNano())

	// Create bucket via standard AWS SDK inside the setup using the testcontainer credentials
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(credentials.StaticCredentialsProvider{
			Value: aws.Credentials{
				AccessKeyID:     accessKey,
				SecretAccessKey: secretKey,
			},
		}),
	)
	if err != nil {
		t.Fatalf("failed to load minio aws config: %v", err)
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})

	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	}); err != nil {
		t.Fatalf("failed to create minio test bucket: %v", err)
	}

	return &minIOTestStore{
		endpoint:  endpoint,
		accessKey: accessKey,
		secretKey: secretKey,
		bucket:    bucket,
	}
}

func (s *minIOTestStore) Name() string { return "minio" }
func (s *minIOTestStore) Env() TestEnv { return Hermetic }
func (s *minIOTestStore) Setup(t *testing.T) []string {
	prefix := "e2e-root/"
	return []string{
		"-store", "s3:" + s.bucket + "/" + prefix,
		"-s3-endpoint", s.endpoint,
		"-s3-region", "us-east-1",
		"-s3-access-key", s.accessKey,
		"-s3-secret-key", s.secretKey,
	}
}
