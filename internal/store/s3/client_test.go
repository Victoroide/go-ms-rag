package s3

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Victoroide/quarry-rag/internal/extractor"
	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
)

func s3EndpointClient(t *testing.T, endpoint, owner string) *Client {
	t.Helper()
	return newClient(aws.Config{
		Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test-key", "test-secret", ""), RetryMaxAttempts: 1,
	}, "owned-bucket", owner, func(o *awss3.Options) {
		o.BaseEndpoint = &endpoint
		o.UsePathStyle = true
	})
}

func TestS3RequestsCarryExpectedOwner(t *testing.T) {
	for _, owner := range []string{"123456789012", ""} {
		t.Run(owner, func(t *testing.T) {
			methods := make(chan string, 3)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				methods <- r.Method
				if got := r.Header.Get("x-amz-expected-bucket-owner"); got != owner {
					t.Errorf("%s owner = %q, want %q", r.Method, got, owner)
				}
				if _, err := io.Copy(io.Discard, r.Body); err != nil {
					t.Error(err)
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			client := s3EndpointClient(t, server.URL, owner)
			key, err := client.Upload(t.Context(), "id", "document", "file.pdf", extractor.FileTypePDF, strings.NewReader("content"))
			if err != nil {
				t.Fatal(err)
			}
			if err := client.Delete(t.Context(), key); err != nil {
				t.Fatal(err)
			}
			if err := client.Ping(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := strings.Join([]string{<-methods, <-methods, <-methods}, ","); got != "PUT,DELETE,HEAD" {
				t.Fatalf("requests = %s", got)
			}
		})
	}
}

func TestPresignForcesAttachment(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	client := s3EndpointClient(t, server.URL, "123456789012")
	for _, file := range []struct{ name, contentType string }{{"report.pdf", "application/pdf"}, {"notes.txt", "text/plain; charset=utf-8"}, {"résumé.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"}, {"unknown.unknown", "application/octet-stream"}} {
		t.Run(file.name, func(t *testing.T) {
			raw, err := client.PresignGet(t.Context(), "documents/document/id/"+file.name)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := url.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			query := parsed.Query()
			disposition, params, err := mime.ParseMediaType(query.Get("response-content-disposition"))
			if err != nil || disposition != "attachment" || params["filename"] != file.name {
				t.Errorf("download disposition = %q, error = %v", query.Get("response-content-disposition"), err)
			}
			if query.Get("response-content-type") != file.contentType {
				t.Errorf("content type = %q, want %q", query.Get("response-content-type"), file.contentType)
			}
			for name, values := range query {
				if strings.Contains(strings.ToLower(name), "expected-bucket-owner") || (strings.EqualFold(name, "X-Amz-SignedHeaders") && strings.Contains(strings.Join(values, ";"), "expected-bucket-owner")) {
					t.Errorf("download requires owner header: %s", name)
				}
			}
		})
	}
}

func TestVerifyMapsAccessDeniedToConfigurationError(t *testing.T) {
	for _, code := range []int{http.StatusOK, http.StatusForbidden, http.StatusNotFound, http.StatusInternalServerError} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodHead || r.Header.Get("x-amz-expected-bucket-owner") != "123456789012" {
					t.Errorf("verification request = %s, owner = %q", r.Method, r.Header.Get("x-amz-expected-bucket-owner"))
				}
				w.WriteHeader(code)
			}))
			defer server.Close()
			client := s3EndpointClient(t, server.URL, "123456789012")
			err := client.Verify(t.Context())
			if code == http.StatusOK {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatal("inaccessible bucket accepted")
			}
			unavailable := code == http.StatusForbidden || code == http.StatusNotFound
			if errors.Is(err, ErrBucketUnavailable) != unavailable {
				t.Errorf("bucket error classification = %v", err)
			}
			var responseErr *awshttp.ResponseError
			if !errors.As(err, &responseErr) || responseErr.HTTPStatusCode() != code {
				t.Errorf("underlying response error lost: %v", err)
			}
		})
	}
}

func TestUploadSanitizesKey(t *testing.T) {
	received := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- strings.TrimPrefix(r.URL.Path, "/owned-bucket/")
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := s3EndpointClient(t, server.URL, "")
	for _, tt := range []struct{ name, docType, filename, wantType, wantFile string }{
		{"separators", " ../sales\\team\n. ", " ../report\\part\x00.pdf. ", "_sales_team_", "_report_part_.pdf"},
		{"empty", " .. ", ". .", "document", "file"},
		{"bounded unicode", strings.Repeat("界", 30), strings.Repeat("é", 110), strings.Repeat("界", 21), strings.Repeat("é", 100)},
		{"plain", "reference", "report.pdf", "reference", "report.pdf"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			key, err := client.Upload(t.Context(), "id", tt.docType, tt.filename, extractor.FileTypePDF, strings.NewReader("content"))
			if err != nil {
				t.Fatal(err)
			}
			want := "documents/" + tt.wantType + "/id/" + tt.wantFile
			stored := <-received
			if key != want || stored != want || !utf8.ValidString(key) {
				t.Fatalf("key = %q, stored key = %q, want %q", key, stored, want)
			}
		})
	}
}
