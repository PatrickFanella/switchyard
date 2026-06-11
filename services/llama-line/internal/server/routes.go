package server

var inferenceEndpoints = []string{
	"/api/generate",
	"/api/chat",
	"/api/embed",
	"/v1/chat/completions",
	"/v1/completions",
	"/v1/embeddings",
}

// IsInferenceEndpoint reports whether path is a GPU-consuming endpoint subject to queuing.
func IsInferenceEndpoint(path string) bool {
	for _, ep := range inferenceEndpoints {
		if path == ep {
			return true
		}
	}
	return false
}
