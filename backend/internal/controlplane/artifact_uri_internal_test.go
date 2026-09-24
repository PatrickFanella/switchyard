package controlplane

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestInlineArtifactURIRoundTripsReadableText(t *testing.T) {
	body := "# Devlog\n\nStep 1 finished at 10:00 (+2 retries); 100% done & tested?\n\n```json\n{\"ok\": true}\n```\n"
	uri := inlineArtifactURI("text/markdown;charset=utf-8", body)
	if !strings.HasPrefix(uri, "data:text/markdown;charset=utf-8,") {
		t.Fatalf("unexpected prefix: %q", uri)
	}
	if strings.ContainsAny(uri, " #?\n") {
		t.Fatalf("uri must percent-encode whitespace and delimiters: %q", uri)
	}
	metadata, err := artifactMetadata(uri, time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("artifactMetadata: %v", err)
	}
	decoded, err := url.PathUnescape(uri[strings.IndexByte(uri, ',')+1:])
	if err != nil || decoded != body {
		t.Fatalf("payload did not round-trip: err=%v decoded=%q", err, decoded)
	}
	sum := sha256.Sum256([]byte(body))
	if metadata.SHA256 != hex.EncodeToString(sum[:]) || metadata.ByteSize != len(body) {
		t.Fatalf("metadata must describe the readable body: got size=%d sha=%s", metadata.ByteSize, metadata.SHA256)
	}
	// artifactMetadata records the media type alone; charset stays in the URI.
	if metadata.ContentType != "text/markdown" || metadata.StorageClass != "inline-data-uri" {
		t.Fatalf("unexpected metadata: %+v", metadata)
	}
}
