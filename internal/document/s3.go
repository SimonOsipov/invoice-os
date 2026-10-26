package document

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// Sentinels for the document error model — mirrors internal/importer's naming.
var (
	// ErrNotFound is returned when the key does not exist.
	ErrNotFound = errors.New("document: not found")

	// ErrRangeNotSatisfiable is returned when the requested range cannot be
	// served, so a handler can answer 416 rather than 500.
	ErrRangeNotSatisfiable = errors.New("document: range not satisfiable")
)

// Config is the object store's connection settings.
type Config struct {
	Bucket          string
	Endpoint        string
	Region          string
	AccessKeyID     string
	SecretAccessKey string
}

// ConfigFromEnv reads the five DOCUMENT_* variables, all required.
//
// Empty fails like unset: an empty value is a ${{source-documents.*}} reference
// that did not render, which is a misconfiguration and not a default.
func ConfigFromEnv() (Config, error) {
	var cfg Config
	for _, f := range []struct {
		key  string
		dest *string
	}{
		{"DOCUMENT_BUCKET", &cfg.Bucket},
		{"DOCUMENT_ENDPOINT", &cfg.Endpoint},
		{"DOCUMENT_REGION", &cfg.Region},
		{"DOCUMENT_ACCESS_KEY_ID", &cfg.AccessKeyID},
		{"DOCUMENT_SECRET_ACCESS_KEY", &cfg.SecretAccessKey},
	} {
		v := os.Getenv(f.key)
		if v == "" {
			// The zero Config, never the partially populated one: a caller that
			// ignores the error must not get something half usable.
			return Config{}, fmt.Errorf("document: %s is required", f.key)
		}
		*f.dest = v
	}
	return cfg, nil
}

type s3Store struct {
	client *s3.Client
	bucket string
	// readRetryer is passed per Get only; Put keeps the client default.
	readRetryer aws.Retryer
}

// NewS3Store returns an ObjectStore over cfg's bucket. A nil hc yields the SDK
// default — same nil->default idiom as invoice.NewValidator; the injectable
// client exists so tests can record the outbound request.
func NewS3Store(cfg Config, hc *http.Client) (ObjectStore, error) {
	awsCfg := aws.Config{
		Region: cfg.Region,
		// aws.CredentialsProviderFunc is in the core module; aws-sdk-go-v2/credentials
		// would add six modules to hold one static key pair.
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{
				AccessKeyID:     cfg.AccessKeyID,
				SecretAccessKey: cfg.SecretAccessKey,
				Source:          "document.Config",
			}, nil
		}),
	}
	if hc != nil {
		awsCfg.HTTPClient = hc
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(cfg.Endpoint)
		// UsePathStyle is deliberately left unset: against a hostname endpoint the
		// SDK addresses the bucket as a subdomain, matching the credential's
		// virtual-host url style.
		o.APIOptions = append(o.APIOptions, unsignAcceptEncoding)
	})
	// Reads also retry a 408 and a RequestCanceled code (TestS3Store_GetRetries408RequestCanceled).
	readRetryer := retry.NewStandard(func(o *retry.StandardOptions) {
		o.Retryables = append(o.Retryables,
			retry.RetryableHTTPStatusCode{Codes: map[int]struct{}{http.StatusRequestTimeout: {}}},
			retry.RetryableErrorCode{Codes: map[string]struct{}{"RequestCanceled": {}}},
		)
	})
	return &s3Store{client: client, bucket: cfg.Bucket, readRetryer: readRetryer}, nil
}

type acceptEncodingKey struct{}

// unsignAcceptEncoding keeps Accept-Encoding out of the SigV4 signature. Google's front end
// rewrites the header, so GCS refuses a signature that covers it (GKE-01 P6).
func unsignAcceptEncoding(stack *middleware.Stack) error {
	hide := middleware.FinalizeMiddlewareFunc("HideAcceptEncoding", func(ctx context.Context, in middleware.FinalizeInput, next middleware.FinalizeHandler) (middleware.FinalizeOutput, middleware.Metadata, error) {
		if req, ok := in.Request.(*smithyhttp.Request); ok {
			if v := req.Header.Get("Accept-Encoding"); v != "" {
				ctx = middleware.WithStackValue(ctx, acceptEncodingKey{}, v)
				req.Header.Del("Accept-Encoding")
			}
		}
		return next.HandleFinalize(ctx, in)
	})
	restore := middleware.FinalizeMiddlewareFunc("RestoreAcceptEncoding", func(ctx context.Context, in middleware.FinalizeInput, next middleware.FinalizeHandler) (middleware.FinalizeOutput, middleware.Metadata, error) {
		if req, ok := in.Request.(*smithyhttp.Request); ok {
			if v, _ := middleware.GetStackValue(ctx, acceptEncodingKey{}).(string); v != "" {
				req.Header.Set("Accept-Encoding", v)
			}
		}
		return next.HandleFinalize(ctx, in)
	})
	if err := stack.Finalize.Insert(hide, "Signing", middleware.Before); err != nil {
		return err
	}
	return stack.Finalize.Insert(restore, "Signing", middleware.After)
}

func (s *s3Store) Put(ctx context.Context, key string, body io.ReadSeeker, size int64) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          body,
		ContentLength: aws.Int64(size),
	})
	if err != nil {
		return fmt.Errorf("document: put %s: %w", key, err)
	}
	return nil
}

func (s *s3Store) Get(ctx context.Context, key, rangeHeader string) (Object, error) {
	in := &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}
	// Left nil when empty: aws.String("") puts an empty Range header on the wire.
	// The string is forwarded verbatim — RFC 7233 parsing belongs to the store.
	if rangeHeader != "" {
		in.Range = aws.String(rangeHeader)
	}

	out, err := s.client.GetObject(ctx, in, func(o *s3.Options) { o.Retryer = s.readRetryer })
	if err != nil {
		return Object{}, classifyGet(key, err)
	}

	obj := Object{Body: out.Body}
	if out.ContentLength != nil {
		obj.Size = *out.ContentLength
	}
	// GetObjectOutput carries no HTTP status, so Partial is derived from the
	// presence of Content-Range. Known gap: a non-conformant 206 that omits the
	// header entirely is indistinguishable here from a 200.
	if out.ContentRange != nil {
		obj.ContentRange = *out.ContentRange
		obj.Partial = true
	}
	return obj, nil
}

func classifyGet(key string, err error) error {
	var noSuchKey *types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return fmt.Errorf("document: get %s: %w", key, ErrNotFound)
	}
	// Keyed on the status, not the APIError code: a bodiless 416 reports
	// RequestedRangeNotSatisfiable while an XML-bodied one reports InvalidRange.
	var respErr *awshttp.ResponseError
	if errors.As(err, &respErr) && respErr.HTTPStatusCode() == http.StatusRequestedRangeNotSatisfiable {
		return fmt.Errorf("document: get %s: %w", key, ErrRangeNotSatisfiable)
	}
	// Defence in depth, not a fix: measured against the live Tigris bucket, a
	// real missing key DOES arrive as *types.NoSuchKey. But GetObject models only
	// NoSuchKey and InvalidObjectState, so a BODILESS 404 deserializes to a
	// generic APIError with the status-derived code "NotFound" and would
	// otherwise be a 500.
	//
	// NoSuchBucket answers 404 too and is a misconfiguration rather than a
	// missing document: it stays a 500, so a bucket outage never tells a caller
	// their evidence is gone. Pinned by TestS3Store_GetNotFoundIsDistinguishable.
	if errors.As(err, &respErr) && respErr.HTTPStatusCode() == http.StatusNotFound {
		var apiErr smithy.APIError
		if !errors.As(err, &apiErr) || apiErr.ErrorCode() != "NoSuchBucket" {
			return fmt.Errorf("document: get %s: %w", key, ErrNotFound)
		}
	}
	return fmt.Errorf("document: get %s: %w", key, err)
}
