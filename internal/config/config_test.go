package config

import "testing"

// baseValidConfig returns a Config that passes validate() in real (non-fake)
// mode, so each test can flip exactly one field and assert the guard it targets.
func baseValidConfig() *Config {
	return &Config{
		DatabaseURL:      "postgres://u:p@localhost:5432/db?sslmode=disable",
		AWSRegion:        "us-east-1",
		AWSAccessKeyID:   "AKIA_TEST",
		AWSSecretKey:     "secret_test",
		PineconeAPIKey:   "pc_test",
		WorkerQueueSize:  100,
		EmbedConcurrency: 10,
		ServiceToken:     "a-strong-service-token",
		FakeMode:         false,
	}
}

func TestValidate_NonFakeRequiresServiceToken(t *testing.T) {
	cfg := baseValidConfig()
	cfg.ServiceToken = ""
	if err := cfg.validate(); err == nil {
		t.Fatal("expected validate() to fail when SERVICE_TOKEN is empty in non-fake mode")
	}

	cfg.ServiceToken = "   " // whitespace must not count as a token
	if err := cfg.validate(); err == nil {
		t.Fatal("expected validate() to fail when SERVICE_TOKEN is only whitespace in non-fake mode")
	}
}

func TestValidate_NonFakeWithServiceTokenOK(t *testing.T) {
	cfg := baseValidConfig()
	if err := cfg.validate(); err != nil {
		t.Fatalf("expected valid non-fake config to pass, got %v", err)
	}
}

func TestValidate_FakeModeAllowsEmptyServiceToken(t *testing.T) {
	cfg := baseValidConfig()
	cfg.FakeMode = true
	cfg.ServiceToken = ""
	// Fake mode also runs without cloud creds.
	cfg.AWSRegion, cfg.AWSAccessKeyID, cfg.AWSSecretKey, cfg.PineconeAPIKey = "", "", "", ""
	if err := cfg.validate(); err != nil {
		t.Fatalf("expected fake-mode config with empty SERVICE_TOKEN to pass, got %v", err)
	}
}
