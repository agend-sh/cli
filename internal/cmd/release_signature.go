package cmd

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"regexp"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

const releaseIssuer = "https://token.actions.githubusercontent.com"

var releaseTagPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`)

func releaseIdentity(tag string) string {
	return "https://github.com/agend-sh/cli/.github/workflows/release.yml@refs/tags/" + tag
}

// Verification uses Sigstore's TUF-authenticated public-good trust root, never
// a certificate/root supplied by the release being installed.
func verifyChecksums(tag string, checksums, proof []byte) error {
	var b bundle.Bundle
	if err := b.UnmarshalJSON(proof); err != nil {
		return fmt.Errorf("invalid release signature bundle: %w", err)
	}
	trusted, err := root.FetchTrustedRoot()
	if err != nil {
		return fmt.Errorf("load Sigstore trust root: %w", err)
	}
	v, err := verify.NewVerifier(trusted, verify.WithTransparencyLog(1), verify.WithObserverTimestamps(1), verify.WithSignedCertificateTimestamps(1))
	if err != nil {
		return err
	}
	return verifyReleaseEntity(tag, checksums, &b, v)
}

func verifyReleaseEntity(tag string, checksums []byte, entity verify.SignedEntity, v *verify.Verifier) error {
	if !releaseTagPattern.MatchString(tag) {
		return fmt.Errorf("invalid release tag")
	}
	identity, err := verify.NewShortCertificateIdentity(releaseIssuer, "", releaseIdentity(tag), "")
	if err != nil {
		return err
	}
	_, err = v.Verify(entity, verify.NewPolicy(verify.WithArtifact(bytes.NewReader(checksums)), verify.WithCertificateIdentity(identity)))
	if err != nil {
		return fmt.Errorf("release signature verification failed: %w", err)
	}
	return nil
}

func fetchReleaseMetadata(client *http.Client, tag, name string) ([]byte, error) {
	if !releaseTagPattern.MatchString(tag) {
		return nil, fmt.Errorf("invalid release tag")
	}
	resp, err := client.Get(fmt.Sprintf("%s/%s/%s", downloadURL, tag, name))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", name, resp.StatusCode)
	}
	const maxMetadata = 1 << 20
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadata+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxMetadata {
		return nil, fmt.Errorf("%s exceeds size limit", name)
	}
	return b, nil
}
