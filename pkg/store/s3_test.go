package store

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/testcontainers/testcontainers-go/modules/minio"
)

func TestS3Store(t *testing.T) {
	// Check if docker is available and running
	cmd := exec.Command("docker", "info")
	if err := cmd.Run(); err != nil {
		t.Skipf("docker is not available or not running, skipping test: %v", err)
	}

	ctx := context.Background()

	// 1. Spin up MinIO container
	minioContainer, err := minio.Run(ctx, "minio/minio:latest")
	if err != nil {
		t.Fatalf("failed to start minio container: %v", err)
	}
	defer func() {
		if err := minioContainer.Terminate(ctx); err != nil {
			t.Fatalf("failed to terminate minio container: %v", err)
		}
	}()

	// 2. Fetch connection details
	url, err := minioContainer.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("failed to get minio connection string: %v", err)
	}

	username := minioContainer.Username
	password := minioContainer.Password

	bucketName := "test-bucket"

	// 3. Create the bucket properly using the AWS client
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(username, password, "")),
	)
	if err != nil {
		t.Fatalf("failed to load initial config: %v", err)
	}

	endpoint := url
	if !strings.HasPrefix(endpoint, "http") {
		endpoint = "http://" + endpoint
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})

	_, err = client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(bucketName),
	})
	if err != nil {
		t.Fatalf("failed to create bucket %q: %v", bucketName, err)
	}

	// 4. Test NewS3Store
	store, err := NewS3Store(
		ctx,
		bucketName,
		WithS3Endpoint(endpoint),
		WithS3Region("us-east-1"),
		WithS3Credentials(username, password),
		WithS3Prefix("prefix/"),
	)
	if err != nil {
		t.Fatalf("failed to create S3Store: %v", err)
	}

	key := "test/file.txt"
	data := []byte("hello s3!")

	// --- 5. Run standard store lifecycle tests ---

	// Put
	if err := store.Put(ctx, key, data); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Get
	fetched, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if string(fetched) != string(data) {
		t.Fatalf("Get mismatch. want %q, got %q", string(data), string(fetched))
	}

	// Exists (true)
	exists, err := store.Exists(ctx, key)
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}
	if !exists {
		t.Fatalf("Expected key to exist")
	}

	// Exists (false)
	exists, err = store.Exists(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("Exists(nonexistent) failed: %v", err)
	}
	if exists {
		t.Fatalf("Expected nonexistent key to report false")
	}

	// Size
	size, err := store.Size(ctx, key)
	if err != nil {
		t.Fatalf("Size failed: %v", err)
	}
	if size != int64(len(data)) {
		t.Fatalf("Expected size %d, got %d", len(data), size)
	}

	// List
	if err := store.Put(ctx, "test/another.txt", data); err != nil {
		t.Fatalf("Put another.txt failed: %v", err)
	}
	keys, err := store.List(ctx, "test/")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("Expected 2 keys in list, got %d", len(keys))
	}

	// TotalSize
	total, err := store.TotalSize(ctx)
	if err != nil {
		t.Fatalf("TotalSize failed: %v", err)
	}
	if total != int64(len(data)*2) {
		t.Fatalf("Expected TotalSize %d, got %d", len(data)*2, total)
	}

	// Delete
	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	exists, _ = store.Exists(ctx, key)
	if exists {
		t.Fatalf("Expected key to be deleted")
	}
}
