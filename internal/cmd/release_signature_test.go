package cmd

import (
	"strings"
	"testing"

	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

func TestReleaseSignatureBindsWorkflowTagAndContents(t *testing.T) {
	trusted, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatal(err)
	}
	// VirtualSigstore signs real test certificates and log entries but does not
	// issue SCTs. Production additionally requires SCTs from the public root.
	v, err := verify.NewVerifier(trusted, verify.WithTransparencyLog(1), verify.WithObserverTimestamps(1))
	if err != nil {
		t.Fatal(err)
	}
	manifest := []byte(strings.Repeat("a", 64) + "  agend-test.tar.gz\n")
	for _, tt := range []struct {
		name, identity, issuer, tag string
		contents                    []byte
		valid                       bool
	}{
		{"valid", releaseIdentity("v1.2.4"), releaseIssuer, "v1.2.4", manifest, true},
		{"wrong tag", releaseIdentity("v1.2.3"), releaseIssuer, "v1.2.4", manifest, false},
		{"wrong workflow", "https://github.com/agend-sh/cli/.github/workflows/ci.yml@refs/tags/v1.2.4", releaseIssuer, "v1.2.4", manifest, false},
		{"wrong issuer", releaseIdentity("v1.2.4"), "https://issuer.invalid", "v1.2.4", manifest, false},
		{"altered checksums", releaseIdentity("v1.2.4"), releaseIssuer, "v1.2.4", []byte("changed"), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			entity, err := trusted.Sign(tt.identity, tt.issuer, manifest)
			if err != nil {
				t.Fatal(err)
			}
			err = verifyReleaseEntity(tt.tag, tt.contents, entity, v)
			if (err == nil) != tt.valid {
				t.Fatalf("valid=%v, error=%v", tt.valid, err)
			}
		})
	}
}

func TestChecksumManifestRejectsMissingMalformedAndDuplicateEntries(t *testing.T) {
	line := strings.Repeat("a", 64) + "  archive.tar.gz\n"
	for _, content := range []string{"", strings.Replace(line, "a", "z", 1), line + line} {
		if _, err := parseChecksum([]byte(content), "archive.tar.gz"); err == nil {
			t.Fatalf("accepted invalid manifest %q", content)
		}
	}
	if _, err := parseChecksum([]byte(line), "archive.tar.gz"); err != nil {
		t.Fatal(err)
	}
}
