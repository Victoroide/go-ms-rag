package awsconf

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Victoroide/quarry-rag/internal/config"
	"github.com/aws/smithy-go"
)

func awsTestConfig(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	for _, key := range []string{"AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_ROLE_ARN", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_CONTAINER_CREDENTIALS_FULL_URI"} {
		t.Setenv(key, "")
	}
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	return &config.Config{AWSRegion: "us-east-1", AWSMaxAttempts: 4}
}

func TestStaticCredentialsWhenKeysProvided(t *testing.T) {
	cfg := awsTestConfig(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "env-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "env-secret")
	cfg.AWSAccessKeyID, cfg.AWSSecretKey, cfg.AWSSessionToken = "static-key", "static-secret", "static-token"
	loaded, err := Load(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := loaded.Credentials.Retrieve(t.Context())
	if err != nil || creds.AccessKeyID != "static-key" || creds.SecretAccessKey != "static-secret" || creds.SessionToken != "static-token" {
		t.Fatalf("static credentials were not selected: error=%v", err)
	}
	if loaded.Region != "us-east-1" {
		t.Fatalf("region = %q", loaded.Region)
	}
}

func TestDefaultChainWhenKeysAbsent(t *testing.T) {
	cfg := awsTestConfig(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "env-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "env-secret")
	t.Setenv("AWS_SESSION_TOKEN", "env-token")
	loaded, err := Load(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := loaded.Credentials.Retrieve(t.Context())
	if err != nil || creds.AccessKeyID != "env-key" || creds.SecretAccessKey != "env-secret" || creds.SessionToken != "env-token" {
		t.Fatalf("environment credentials were not selected: error=%v", err)
	}
}

func TestDefaultChainUsesSharedCredentials(t *testing.T) {
	cfg := awsTestConfig(t)
	credentialsFile := filepath.Join(t.TempDir(), "credentials")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", credentialsFile)
	if err := os.WriteFile(credentialsFile, []byte("[default]\naws_access_key_id = shared-key\naws_secret_access_key = shared-secret\naws_session_token = shared-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := loaded.Credentials.Retrieve(t.Context())
	if err != nil || creds.AccessKeyID != "shared-key" || creds.SessionToken != "shared-token" {
		t.Fatalf("shared credentials were not selected: error=%v", err)
	}
}

func TestModelTimeoutIsRetryable(t *testing.T) {
	cfg := awsTestConfig(t)
	loaded, err := Load(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"ModelTimeoutException", "ModelNotReadyException", "ThrottlingException"} {
		if !loaded.Retryer().IsErrorRetryable(&smithy.GenericAPIError{Code: code}) {
			t.Errorf("%s is not retryable", code)
		}
	}
	if loaded.Retryer().IsErrorRetryable(&smithy.GenericAPIError{Code: "ValidationException"}) {
		t.Error("validation failure is retryable")
	}
}

func TestRetryQuotaIsDisabled(t *testing.T) {
	cfg := awsTestConfig(t)
	loaded, err := Load(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	r := loaded.Retryer()
	for i := range 1000 {
		if _, err := r.GetRetryToken(t.Context(), errors.New("retryable error")); err != nil {
			t.Fatalf("retry %d exhausted quota: %v", i, err)
		}
	}
}

func TestRetryAttemptsAndBackoffAreBounded(t *testing.T) {
	cfg := awsTestConfig(t)
	cfg.AWSMaxAttempts = 7
	loaded, err := Load(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	r := loaded.Retryer()
	if r.MaxAttempts() != 7 {
		t.Fatalf("max attempts = %d, want 7", r.MaxAttempts())
	}
	for attempt := 1; attempt <= 20; attempt++ {
		delay, err := r.RetryDelay(attempt, errors.New("retryable error"))
		if err != nil || delay < 0 || delay > 10*time.Second {
			t.Fatalf("attempt %d backoff = %s, error = %v", attempt, delay, err)
		}
	}
}
