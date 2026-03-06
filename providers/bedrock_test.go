package providers

import (
	"strings"
	"testing"
)

func TestNewBedrock_DefaultRegion(t *testing.T) {
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	p, err := NewBedrock("")
	if err != nil {
		t.Fatalf("NewBedrock() error: %v", err)
	}
	if p.Name() != "bedrock" {
		t.Errorf("Name() = %q, want bedrock", p.Name())
	}
	if p.Region() != "us-east-1" {
		t.Errorf("region = %q, want us-east-1", p.Region())
	}
}

func TestNewBedrockWithOptions_StaticCredentials(t *testing.T) {
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	p, err := NewBedrockWithOptions(BedrockOptions{
		Region:          "us-west-2",
		AccessKeyID:     "test-access-key",
		SecretAccessKey: "test-secret-key",
		SessionToken:    "test-session-token",
	})
	if err != nil {
		t.Fatalf("NewBedrockWithOptions() error: %v", err)
	}
	if p.Region() != "us-west-2" {
		t.Errorf("region = %q, want us-west-2", p.Region())
	}
}

func TestNewBedrockWithOptions_InvalidStaticCredentials(t *testing.T) {
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	_, err := NewBedrockWithOptions(BedrockOptions{
		Region:      "us-east-1",
		AccessKeyID: "test-access-key",
	})
	if err == nil {
		t.Fatal("expected error for incomplete static credentials")
	}
	if !strings.Contains(err.Error(), "require both access key ID and secret access key") {
		t.Errorf("error = %q, want static-credentials validation message", err.Error())
	}
}
