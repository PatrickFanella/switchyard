package controlplane

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

func terminalStepStatusForRun(status RunStatus) (RunStepStatus, bool) {
	switch status {
	case RunFailed:
		return StepFailed, true
	case RunTimedOut:
		return StepTimedOut, true
	case RunCanceled:
		return StepCanceled, true
	default:
		return "", false
	}
}

func validateArtifactInput(input CreateArtifactInput) error {
	if input.RunID == "" || input.Name == "" || input.Kind == "" || input.URI == "" || len(input.Name) > 256 || len(input.Kind) > 64 {
		return ErrInvalid
	}
	// Base64 expansion is expected. Cap encoded input defensively, then enforce
	// the actual storage limit on decoded bytes below.
	if len(input.URI) > artifactEncodedMaxBytes() {
		return ErrInvalid
	}
	_, err := artifactMetadata(input.URI, time.Now().UTC())
	if err != nil {
		return ErrInvalid
	}
	return nil
}

// artifactMetadata keeps the current inline data-URI contract explicit. Remote
// URLs are deliberately not accepted until a separate, authenticated object
// storage lifecycle exists.
func artifactMetadata(uri string, now time.Time) (Artifact, error) {
	contentType, decoded, err := decodeInlineArtifactURI(uri)
	if err != nil {
		return Artifact{}, err
	}
	sum := sha256.Sum256(decoded)
	until := now.Add(artifactRetentionWindow()).UTC()
	return Artifact{StorageClass: "inline-data-uri", ContentType: contentType, ByteSize: len(decoded), SHA256: hex.EncodeToString(sum[:]), RetentionUntil: &until}, nil
}

// decodeInlineArtifactURI returns the media type and exact decoded bytes of an
// inline data-URI artifact. The bytes are the ones described by the persisted
// size and SHA-256.
func decodeInlineArtifactURI(uri string) (string, []byte, error) {
	if !strings.HasPrefix(strings.ToLower(uri), "data:") {
		return "", nil, ErrInvalid
	}
	comma := strings.IndexByte(uri, ',')
	if comma < 5 {
		return "", nil, ErrInvalid
	}
	meta, payload := uri[5:comma], uri[comma+1:]
	parts := strings.Split(meta, ";")
	base64Encoded := false
	for _, parameter := range parts[1:] {
		parameter = strings.TrimSpace(parameter)
		if strings.EqualFold(parameter, "base64") && !base64Encoded {
			base64Encoded = true
			continue
		}
		if strings.HasPrefix(strings.ToLower(parameter), "charset=") && len(strings.TrimSpace(parameter[8:])) > 0 {
			continue
		}
		return "", nil, ErrInvalid
	}
	contentType := strings.TrimSpace(parts[0])
	if contentType == "" {
		contentType = "text/plain;charset=US-ASCII"
	}
	var decoded []byte
	var err error
	if base64Encoded {
		decoded, err = base64.StdEncoding.DecodeString(payload)
	} else {
		var text string
		text, err = url.PathUnescape(payload)
		decoded = []byte(text)
	}
	if err != nil || len(decoded) > artifactMaxBytes() {
		return "", nil, ErrInvalid
	}
	return contentType, decoded, nil
}

// inlineArtifactURI builds the inline data URI used for text artifacts. It
// percent-encodes the body with path escaping so that spaces round-trip as
// spaces through artifactMetadata and through browser data-URI decoding; the
// stored byte size and SHA-256 therefore describe the readable text rather than
// a query-escaped rendering of it.
func inlineArtifactURI(contentType, body string) string {
	return "data:" + contentType + "," + url.PathEscape(body)
}

func artifactMaxBytes() int {
	maxBytes := 1 << 20
	if parsed, err := strconv.Atoi(strings.TrimSpace(os.Getenv("SWITCHYARD_MAX_ARTIFACT_BYTES"))); err == nil && parsed >= 1024 && parsed <= 16<<20 {
		maxBytes = parsed
	}
	return maxBytes
}

func artifactEncodedMaxBytes() int { return artifactMaxBytes()*2 + 1024 }

func artifactRetentionWindow() time.Duration {
	if configured, err := time.ParseDuration(strings.TrimSpace(os.Getenv("SWITCHYARD_RETENTION"))); err == nil && configured > 0 {
		return configured
	}
	return 30 * 24 * time.Hour
}

func isTerminalStepStatus(status RunStepStatus) bool {
	return status == StepSucceeded || status == StepFailed || status == StepCanceled || status == StepTimedOut || status == StepPaused
}
