package controlplane

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

func maxModelResponseBytes() int {
	if parsed, err := strconv.Atoi(strings.TrimSpace(os.Getenv("SWITCHYARD_MAX_MODEL_RESPONSE_BYTES"))); err == nil && parsed >= 1024 && parsed <= 16<<20 {
		return parsed
	}
	return 1 << 20
}

func readModelResponseBody(reader io.Reader) ([]byte, error) {
	limit := maxModelResponseBytes()
	data, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("model response exceeds configured %d-byte limit", limit)
	}
	return data, nil
}

func validateModelResponse(response ModelResponse) error {
	if len(response.Content) > maxModelResponseBytes() {
		return fmt.Errorf("model response exceeds configured limit")
	}
	return nil
}
