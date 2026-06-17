package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// S3Source is a Source backed by an Amazon S3 bucket (or compatible
// service reachable via AWS SDK v2). Keys are canonical, slash-separated
// paths relative to the configured prefix.
type S3Source struct {
	client *s3.Client
	bucket string
	// prefix is the canonical S3 key prefix. It is either the empty
	// string or ends with "/". Source-relative keys are concatenated
	// onto this to form full S3 object keys.
	prefix string
}

// S3Options configures an S3Source. It mirrors config.S3Config but is
// decoupled from the config package so the source layer has no upward
// dependency on config.
type S3Options struct {
	// Bucket is the S3 bucket name. Required.
	Bucket string
	// Prefix is an optional key prefix (e.g. "photos/") that scopes
	// the source to a sub-tree of the bucket. If non-empty and not
	// already ending in "/", a trailing slash is appended during
	// normalization. Source-relative keys never include this prefix.
	Prefix string
	// Region is the AWS region (e.g. "eu-central-1"). Required.
	Region string
	// AccessKey is the static AWS access key ID. Required.
	AccessKey string
	// SecretAccessKey is the static AWS secret access key. Required.
	SecretAccessKey string
}

// NewS3Source builds an S3Source using static credentials. It loads the
// default AWS config with the supplied region and credential provider
// and constructs an s3.Client. Returns an error if any required option
// is empty or if AWS config loading fails.
func NewS3Source(ctx context.Context, opts S3Options) (*S3Source, error) {
	if opts.Bucket == "" {
		return nil, errors.New("source: s3 bucket is required")
	}
	if opts.Region == "" {
		return nil, errors.New("source: s3 region is required")
	}
	if opts.AccessKey == "" {
		return nil, errors.New("source: s3 access_key is required")
	}
	if opts.SecretAccessKey == "" {
		return nil, errors.New("source: s3 secret_access_key is required")
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(opts.Region),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(opts.AccessKey, opts.SecretAccessKey, ""),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("source: load aws config: %w", err)
	}

	return &S3Source{
		client: s3.NewFromConfig(cfg),
		bucket: opts.Bucket,
		prefix: normalizePrefix(opts.Prefix),
	}, nil
}

// normalizePrefix returns the empty string if p is empty, otherwise p
// with a guaranteed trailing slash.
func normalizePrefix(p string) string {
	if p == "" {
		return ""
	}
	if strings.HasSuffix(p, "/") {
		return p
	}
	return p + "/"
}

// List paginates over every object under the configured prefix, filters
// the results to allowlisted extensions, strips the prefix to produce
// canonical source-relative keys, and returns them sorted by Key.
func (s *S3Source) List(ctx context.Context) ([]Entry, error) {
	prefixPtr := aws.String(s.prefix)
	if s.prefix == "" {
		// An empty prefix is fine, but keep the pointer non-nil for
		// API symmetry; the SDK treats empty as "no prefix".
		prefixPtr = nil
	}
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: prefixPtr,
	})

	var entries []Entry
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("source: list s3: %w", err)
		}
		for _, obj := range page.Contents {
			entry, ok := s.objectToEntry(obj)
			if !ok {
				continue
			}
			entries = append(entries, entry)
		}
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	return entries, nil
}

// objectToEntry converts a single S3 object to an Entry, filtering by
// extension and stripping the configured prefix. The bool reports
// whether the object survived the filter.
func (s *S3Source) objectToEntry(obj s3types.Object) (Entry, bool) {
	if obj.Key == nil {
		return Entry{}, false
	}
	full := *obj.Key
	relKey := strings.TrimPrefix(full, s.prefix)
	if relKey == "" || strings.HasSuffix(relKey, "/") {
		return Entry{}, false
	}
	if !IsAllowedExt(strings.ToLower(filepath.Ext(relKey))) {
		return Entry{}, false
	}
	var size int64
	if obj.Size != nil {
		size = *obj.Size
	}
	var modTime time.Time
	if obj.LastModified != nil {
		modTime = *obj.LastModified
	}
	var etag string
	if obj.ETag != nil {
		etag = strings.Trim(*obj.ETag, `"`)
	}
	return Entry{
		Key:     relKey,
		Size:    size,
		ModTime: modTime,
		ETag:    etag,
	}, true
}

// Get fetches the object at key and returns its body and metadata. The
// caller must Close the returned reader. NoSuchKey responses are mapped
// to ErrNotFound.
func (s *S3Source) Get(ctx context.Context, key string) (io.ReadCloser, Metadata, error) {
	cleaned, err := CleanKey(key)
	if err != nil {
		return nil, Metadata{}, err
	}
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.prefix + cleaned),
	})
	if err != nil {
		return nil, Metadata{}, mapS3Err(err)
	}
	meta := Metadata{
		ContentType: contentTypeForKey(cleaned),
	}
	if out.ContentLength != nil {
		meta.Size = *out.ContentLength
	}
	if out.ContentType != nil && *out.ContentType != "" {
		meta.ContentType = *out.ContentType
	}
	if out.ETag != nil {
		meta.ETag = strings.Trim(*out.ETag, `"`)
	}
	return out.Body, meta, nil
}

// Exists reports whether the object at key is present. A missing object
// returns (false, nil); other errors are surfaced. HeadObject in the
// AWS SDK v2 typically returns a smithy.APIError with code "NotFound"
// for missing keys rather than a typed *types.NoSuchKey, so both forms
// are checked.
func (s *S3Source) Exists(ctx context.Context, key string) (bool, error) {
	cleaned, err := CleanKey(key)
	if err != nil {
		return false, err
	}
	_, err = s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.prefix + cleaned),
	})
	if err == nil {
		return true, nil
	}
	if isNotFound(err) {
		return false, nil
	}
	return false, err
}

// Put writes the bytes from r to key with the given content type. S3
// PutObject is atomic at the API level, so no temp/rename dance is
// required.
func (s *S3Source) Put(ctx context.Context, key string, r io.Reader, contentType string) error {
	cleaned, err := CleanKey(key)
	if err != nil {
		return err
	}
	input := &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.prefix + cleaned),
		Body:   r,
	}
	if contentType != "" {
		input.ContentType = aws.String(contentType)
	}
	if _, err := s.client.PutObject(ctx, input); err != nil {
		return fmt.Errorf("source: s3 put: %w", err)
	}
	return nil
}

// Delete removes the object at key. S3 DeleteObject is idempotent and
// does not surface an error when the key is absent, so we HEAD the
// object first to honor the Source contract that deleting a missing
// key returns ErrNotFound. This is a deliberate two-call pattern.
func (s *S3Source) Delete(ctx context.Context, key string) error {
	cleaned, err := CleanKey(key)
	if err != nil {
		return err
	}
	exists, err := s.Exists(ctx, cleaned)
	if err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.prefix + cleaned),
	})
	if err != nil {
		return fmt.Errorf("source: s3 delete: %w", err)
	}
	return nil
}

// mapS3Err converts an AWS SDK error to the package's sentinel errors
// where applicable. Currently it maps NoSuchKey / NotFound responses to
// ErrNotFound and otherwise returns the original error unchanged.
func mapS3Err(err error) error {
	if err == nil {
		return nil
	}
	if isNotFound(err) {
		return ErrNotFound
	}
	return err
}

// isNotFound reports whether err is a NoSuchKey response or an APIError
// with code "NotFound". The latter is what HeadObject typically returns.
func isNotFound(err error) bool {
	var nsk *s3types.NoSuchKey
	if errors.As(err, &nsk) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code := apiErr.ErrorCode()
		if code == "NotFound" || code == "NoSuchKey" {
			return true
		}
	}
	return false
}
