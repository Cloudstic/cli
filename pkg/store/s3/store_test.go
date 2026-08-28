package s3

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/testcontainers/testcontainers-go/modules/minio"

	"github.com/cloudstic/cli/pkg/store"
	"github.com/cloudstic/cli/pkg/store/storetest"
)

func TestStore(t *testing.T) {
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

	// 4. Test New
	st, err := New(
		ctx,
		bucketName,
		WithEndpoint(endpoint),
		WithRegion("us-east-1"),
		WithCredentials(username, password),
		WithPrefix("prefix/"),
	)
	if err != nil {
		t.Fatalf("failed to create Store: %v", err)
	}

	key := "test/file.txt"
	data := []byte("hello s3!")

	// --- 5. Run standard store lifecycle tests ---

	// Put
	if err := st.Put(ctx, key, data); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Get
	fetched, err := st.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if string(fetched) != string(data) {
		t.Fatalf("Get mismatch. want %q, got %q", string(data), string(fetched))
	}

	// Exists (true)
	exists, err := st.Exists(ctx, key)
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}
	if !exists {
		t.Fatalf("Expected key to exist")
	}

	// Exists (false)
	exists, err = st.Exists(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("Exists(nonexistent) failed: %v", err)
	}
	if exists {
		t.Fatalf("Expected nonexistent key to report false")
	}

	// Get (not found)
	if _, err := st.Get(ctx, "nonexistent"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get(nonexistent) error = %v, want errors.Is(err, store.ErrNotFound)", err)
	}

	// Size
	size, err := st.Size(ctx, key)
	if err != nil {
		t.Fatalf("Size failed: %v", err)
	}
	if size != int64(len(data)) {
		t.Fatalf("Expected size %d, got %d", len(data), size)
	}

	// List
	if err := st.Put(ctx, "test/another.txt", data); err != nil {
		t.Fatalf("Put another.txt failed: %v", err)
	}
	keys, err := st.List(ctx, "test/")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("Expected 2 keys in list, got %d", len(keys))
	}

	// TotalSize
	total, err := st.TotalSize(ctx)
	if err != nil {
		t.Fatalf("TotalSize failed: %v", err)
	}
	if total != int64(len(data)*2) {
		t.Fatalf("Expected TotalSize %d, got %d", len(data)*2, total)
	}

	// Delete
	if err := st.Delete(ctx, key); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	exists, _ = st.Exists(ctx, key)
	if exists {
		t.Fatalf("Expected key to be deleted")
	}

	// Ranged reads are what let PackStore fetch a footer without pulling the
	// whole packfile, so hold S3 to the same contract as every other backend.
	t.Run("RangeGetter", func(t *testing.T) {
		storetest.AssertRangeGetterConformance(t, st)
	})

	// Batched deletes are what take a prune's sweep from one request per object
	// to one per thousand, so hold S3 to the same contract as every other
	// backend — against MinIO, which is what the benchmarks measure.
	t.Run("BatchDeleter", func(t *testing.T) {
		storetest.AssertBatchDeleterConformance(t, st)
	})
}

func TestWithPrefix_NormalizesPrefix(t *testing.T) {
	var opts s3Options
	WithPrefix("nested/prefix")(&opts)
	if opts.prefix != "nested/prefix/" {
		t.Fatalf("prefix = %q", opts.prefix)
	}
}
