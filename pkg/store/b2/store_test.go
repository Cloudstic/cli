package b2

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Backblaze/blazer/b2"

	"github.com/cloudstic/cli/pkg/store"
	"github.com/cloudstic/cli/pkg/store/storetest"
)

func TestWithPrefix_NormalizesPrefix(t *testing.T) {
	var opts b2Options
	WithPrefix("nested/prefix")(&opts)
	if opts.prefix != "nested/prefix/" {
		t.Fatalf("prefix = %q", opts.prefix)
	}
}

// --- fake B2 REST server -------------------------------------------------
//
// blazer (github.com/Backblaze/blazer/b2) talks to Backblaze over a plain
// JSON REST API (see its base package). b2.APIBase lets a *b2.Client be
// pointed at an arbitrary URL, so a small httptest.Server emulating the
// handful of calls Store actually makes is enough to exercise it without
// live B2 credentials or network access.

// fakeB2Object is the single stored version of an object under a given name;
// the fake server never versions objects, which is all Store needs.
type fakeB2Object struct {
	id        string
	name      string
	data      []byte
	sha1      string
	timestamp int64
}

// fakeB2Server emulates the b2_authorize_account handshake, bucket lookup,
// upload, download, list, and delete calls that Store's Put/Get/GetRange/
// Exists/Delete/Size/List/TotalSize/DeletePrefix issue through blazer.
type fakeB2Server struct {
	mu      sync.Mutex
	objects map[string]*fakeB2Object
	nextID  int

	baseURL    string
	bucketName string
	bucketID   string
}

func newFakeB2Server(t *testing.T, bucketName string) *fakeB2Server {
	t.Helper()

	s := &fakeB2Server{
		objects:    make(map[string]*fakeB2Object),
		bucketName: bucketName,
		bucketID:   "test-bucket-id",
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/b2api/v3/b2_authorize_account", s.handleAuthorizeAccount)
	mux.HandleFunc("/b2api/v3/b2_list_buckets", s.handleListBuckets)
	mux.HandleFunc("/b2api/v3/b2_get_upload_url", s.handleGetUploadURL)
	mux.HandleFunc("/b2api/v3/b2_upload_file", s.handleUploadFile)
	mux.HandleFunc("/b2api/v3/b2_list_file_names", s.handleListFileNames)
	mux.HandleFunc("/b2api/v3/b2_get_file_info", s.handleGetFileInfo)
	mux.HandleFunc("/b2api/v3/b2_delete_file_version", s.handleDeleteFileVersion)
	mux.HandleFunc("/file/", s.handleDownloadFileByName)

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	s.baseURL = ts.URL
	return s
}

// client returns a *b2.Client authorized against the fake server.
func (s *fakeB2Server) client(ctx context.Context, t *testing.T) *b2.Client {
	t.Helper()
	client, err := b2.NewClient(ctx, "test-key-id", "test-app-key", b2.APIBase(s.baseURL))
	if err != nil {
		t.Fatalf("b2.NewClient: %v", err)
	}
	return client
}

// Wire types below mirror the JSON field names from blazer's internal
// b2types package (unexported to this module, so not importable) - only the
// fields Store's call paths actually populate or read.

type fakeStorageAPIInfo struct {
	AbsMinPartSize int    `json:"absoluteMinimumPartSize"`
	URI            string `json:"apiUrl"`
	Bucket         string `json:"bucketId"`
	Name           string `json:"bucketName"`
	DownloadURI    string `json:"downloadUrl"`
	PartSize       int    `json:"recommendedPartSize"`
}

type fakeAPIInfo struct {
	StorageAPIInfo fakeStorageAPIInfo `json:"storageApi"`
}

type fakeAuthorizeAccountResponse struct {
	AccountID string      `json:"accountId"`
	APIInfo   fakeAPIInfo `json:"apiInfo"`
	AuthToken string      `json:"authorizationToken"`
}

type fakeListBucketsRequest struct {
	Name string `json:"bucketName"`
}

type fakeBucketInfo struct {
	BucketID string `json:"bucketId"`
	Name     string `json:"bucketName"`
	Type     string `json:"bucketType"`
}

type fakeListBucketsResponse struct {
	Buckets []fakeBucketInfo `json:"buckets"`
}

type fakeGetUploadURLResponse struct {
	URI   string `json:"uploadUrl"`
	Token string `json:"authorizationToken"`
}

// fakeFileInfo is shared by b2_upload_file, b2_list_file_names entries, and
// b2_get_file_info responses - they are all the same shape on the wire.
type fakeFileInfo struct {
	FileID    string `json:"fileId,omitempty"`
	Name      string `json:"fileName,omitempty"`
	Size      int64  `json:"contentLength,omitempty"`
	SHA1      string `json:"contentSha1,omitempty"`
	Action    string `json:"action,omitempty"`
	Timestamp int64  `json:"uploadTimestamp,omitempty"`
}

type fakeListFileNamesRequest struct {
	Prefix string `json:"prefix"`
}

type fakeListFileNamesResponse struct {
	Continuation string         `json:"nextFileName"`
	Files        []fakeFileInfo `json:"files"`
}

type fakeGetFileInfoRequest struct {
	ID string `json:"fileId"`
}

type fakeDeleteFileVersionRequest struct {
	Name string `json:"fileName"`
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeB2Error(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Status  int    `json:"status"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}{status, code, msg})
}

// unescapeB2 reverses blazer's header escaping (base.escape), which is
// url.QueryEscape with "%2F" restored to a literal slash - so plain
// url.QueryUnescape round-trips it.
func unescapeB2(s string) (string, error) {
	return url.QueryUnescape(s)
}

func (s *fakeB2Server) handleAuthorizeAccount(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, fakeAuthorizeAccountResponse{
		AccountID: "test-account-id",
		APIInfo: fakeAPIInfo{
			StorageAPIInfo: fakeStorageAPIInfo{
				AbsMinPartSize: 5000000,
				URI:            s.baseURL,
				Bucket:         s.bucketID,
				Name:           s.bucketName,
				DownloadURI:    s.baseURL,
				PartSize:       100000000,
			},
		},
		AuthToken: "test-auth-token",
	})
}

func (s *fakeB2Server) handleListBuckets(w http.ResponseWriter, r *http.Request) {
	var req fakeListBucketsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeB2Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	var resp fakeListBucketsResponse
	if req.Name == "" || req.Name == s.bucketName {
		resp.Buckets = append(resp.Buckets, fakeBucketInfo{
			BucketID: s.bucketID,
			Name:     s.bucketName,
			Type:     "allPrivate",
		})
	}
	writeJSON(w, resp)
}

func (s *fakeB2Server) handleGetUploadURL(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, fakeGetUploadURLResponse{
		URI:   s.baseURL + "/b2api/v3/b2_upload_file",
		Token: "test-upload-token",
	})
}

func (s *fakeB2Server) handleUploadFile(w http.ResponseWriter, r *http.Request) {
	name, err := unescapeB2(r.Header.Get("X-Bz-File-Name"))
	if err != nil {
		writeB2Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeB2Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	s.mu.Lock()
	s.nextID++
	obj := &fakeB2Object{
		id:        fmt.Sprintf("file-%d", s.nextID),
		name:      name,
		data:      data,
		sha1:      fmt.Sprintf("%x", sha1.Sum(data)),
		timestamp: time.Now().UnixMilli(),
	}
	s.objects[name] = obj
	s.mu.Unlock()

	writeJSON(w, fakeFileInfo{
		FileID:    obj.id,
		Name:      obj.name,
		Size:      int64(len(data)),
		SHA1:      obj.sha1,
		Action:    "upload",
		Timestamp: obj.timestamp,
	})
}

func (s *fakeB2Server) handleListFileNames(w http.ResponseWriter, r *http.Request) {
	var req fakeListFileNamesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeB2Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	s.mu.Lock()
	var names []string
	for name := range s.objects {
		if strings.HasPrefix(name, req.Prefix) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	files := make([]fakeFileInfo, 0, len(names))
	for _, name := range names {
		obj := s.objects[name]
		files = append(files, fakeFileInfo{
			FileID:    obj.id,
			Name:      obj.name,
			Size:      int64(len(obj.data)),
			SHA1:      obj.sha1,
			Action:    "upload",
			Timestamp: obj.timestamp,
		})
	}
	s.mu.Unlock()

	// No pagination: every matching object is returned in one page, so an
	// empty continuation token correctly signals "no more results".
	writeJSON(w, fakeListFileNamesResponse{Files: files})
}

func (s *fakeB2Server) handleGetFileInfo(w http.ResponseWriter, r *http.Request) {
	var req fakeGetFileInfoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeB2Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	s.mu.Lock()
	var found *fakeB2Object
	for _, obj := range s.objects {
		if obj.id == req.ID {
			found = obj
			break
		}
	}
	s.mu.Unlock()

	if found == nil {
		writeB2Error(w, http.StatusNotFound, "not_found", "file not found")
		return
	}

	writeJSON(w, fakeFileInfo{
		FileID:    found.id,
		Name:      found.name,
		Size:      int64(len(found.data)),
		SHA1:      found.sha1,
		Action:    "upload",
		Timestamp: found.timestamp,
	})
}

func (s *fakeB2Server) handleDeleteFileVersion(w http.ResponseWriter, r *http.Request) {
	var req fakeDeleteFileVersionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeB2Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	s.mu.Lock()
	delete(s.objects, req.Name)
	s.mu.Unlock()

	writeJSON(w, struct{}{})
}

// handleDownloadFileByName backs b2_download_file_by_name, used for both GET
// (Get/GetRange) and HEAD (the existence check behind Exists/Size/Delete).
// http.ServeContent is relied on for correct Range/416/HEAD handling, which
// is what lets a single small fake object exercise Store's ranged-read and
// past-the-end-of-object error paths.
func (s *fakeB2Server) handleDownloadFileByName(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/file/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[0] != s.bucketName {
		writeB2Error(w, http.StatusNotFound, "file_not_present", "not found")
		return
	}
	name := parts[1]

	s.mu.Lock()
	obj, ok := s.objects[name]
	s.mu.Unlock()
	if !ok {
		writeB2Error(w, http.StatusNotFound, "file_not_present", fmt.Sprintf("%s: not present", name))
		return
	}

	w.Header().Set("X-Bz-File-Id", obj.id)
	w.Header().Set("X-Bz-Content-Sha1", obj.sha1)
	http.ServeContent(w, r, name, time.UnixMilli(obj.timestamp), bytes.NewReader(obj.data))
}

func TestStore(t *testing.T) {
	ctx := context.Background()

	bucketName := "test-bucket"
	srv := newFakeB2Server(t, bucketName)
	client := srv.client(ctx, t)

	st, err := New(bucketName, WithClient(client), WithPrefix("prefix/"))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	key := "test/file.txt"
	data := []byte("hello b2!")

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

	// Get (not found) - this is the error-mapping path other stores map to
	// store.ErrNotFound; blazer exposes no typed not-found error, so Store
	// gets there via b2.IsNotExist on the underlying b2err.
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
		t.Fatalf("Expected 2 keys in list, got %d: %v", len(keys), keys)
	}

	// TotalSize
	total, err := st.TotalSize(ctx)
	if err != nil {
		t.Fatalf("TotalSize failed: %v", err)
	}
	if total != int64(len(data)*2) {
		t.Fatalf("Expected TotalSize %d, got %d", len(data)*2, total)
	}

	// Flush is a documented no-op for Store.
	if err := st.Flush(ctx); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Delete
	if err := st.Delete(ctx, key); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	exists, _ = st.Exists(ctx, key)
	if exists {
		t.Fatalf("Expected key to be deleted")
	}

	// DeletePrefix
	if err := st.DeletePrefix(ctx, "test/"); err != nil {
		t.Fatalf("DeletePrefix failed: %v", err)
	}
	keys, err = st.List(ctx, "test/")
	if err != nil {
		t.Fatalf("List after DeletePrefix failed: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("Expected 0 keys after DeletePrefix, got %d: %v", len(keys), keys)
	}

	// GetRange, including the ranged-read edge cases (past-the-end,
	// zero-length, negative offset, missing object).
	t.Run("RangeGetter", func(t *testing.T) {
		storetest.AssertRangeGetterConformance(t, st)
	})

	// B2's native API deletes one file version per call, so DeleteAll is a
	// loop here — held to the same contract regardless.
	t.Run("BatchDeleter", func(t *testing.T) {
		storetest.AssertBatchDeleterConformance(t, st)
	})
}
