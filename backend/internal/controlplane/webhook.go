package controlplane

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	WebhookDeliveryHeader  = "X-Switchyard-Delivery"
	WebhookTimestampHeader = "X-Switchyard-Timestamp"
	WebhookKeyIDHeader     = "X-Switchyard-Key-Id"
	WebhookSignatureHeader = "X-Switchyard-Signature"
	webhookMaxBodyBytes    = int64(64 << 10)
)

var webhookIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
var webhookSecretBasename = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

type WebhookConfig struct {
	Version          int                    `json:"version"`
	Owner            string                 `json:"owner"`
	Generation       int64                  `json:"generation"`
	ReceiptRetention string                 `json:"receipt_retention"`
	Triggers         []WebhookTriggerConfig `json:"triggers"`
}

type WebhookTriggerConfig struct {
	TriggerID   string                       `json:"trigger_id"`
	Revision    string                       `json:"revision"`
	Keys        []WebhookKeyConfig           `json:"keys"`
	Projections map[string]WebhookProjection `json:"projections,omitempty"`
}

type WebhookKeyConfig struct {
	ID         string    `json:"id"`
	SecretFile string    `json:"secret_file"`
	NotBefore  time.Time `json:"not_before"`
	NotAfter   time.Time `json:"not_after"`
}

type WebhookProjection struct {
	Pointer string `json:"pointer"`
}

type webhookAuthSnapshot struct {
	generation  int64
	revision    string
	keys        map[string]webhookSnapshotKey
	projections map[string]WebhookProjection
}

type webhookSnapshotKey struct {
	secret    []byte
	notBefore time.Time
	notAfter  time.Time
}

type WebhookDispatcher struct {
	store         Store
	authConfig    WebhookAuthConfig
	now           func() time.Time
	timeout       time.Duration
	semaphore     chan struct{}
	limiter       *requestLimiter
	snapshots     map[string]webhookAuthSnapshot
	metadata      []WebhookAuthMetadata
	secretDigests []string
}

type WebhookDispatcherOptions struct {
	Now            func() time.Time
	Timeout        time.Duration
	MaxConcurrency int
	RatePerSecond  int
	RunRetention   time.Duration
	Owner          string
}

func PrepareWebhookDispatcher(configPath, secretDir string, store Store, options WebhookDispatcherOptions) (*WebhookDispatcher, error) {
	if store == nil {
		return nil, ErrInvalid
	}
	configBytes, err := readPrivatePath(configPath, false, 64<<10)
	if err != nil {
		return nil, fmt.Errorf("read webhook config: %w", err)
	}
	if err := validateUniqueJSON(configBytes); err != nil {
		return nil, fmt.Errorf("decode webhook config: %w", err)
	}
	secretFD, err := openPrivateDirectory(secretDir)
	if err != nil {
		return nil, fmt.Errorf("validate webhook secret directory: %w", err)
	}
	defer unix.Close(secretFD)
	var config WebhookConfig
	decoder := json.NewDecoder(strings.NewReader(string(configBytes)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("decode webhook config: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil || config.Version != 1 || len(config.Triggers) == 0 || !validWebhookIdentifier(config.Owner, 128) || config.Generation < 1 || config.Owner != options.Owner {
		return nil, fmt.Errorf("invalid webhook config")
	}
	receiptRetention, err := time.ParseDuration(config.ReceiptRetention)
	if err != nil || receiptRetention < webhookMinReceiptRetention || receiptRetention > webhookMaxReceiptRetention || options.RunRetention <= 0 || receiptRetention < options.RunRetention {
		return nil, fmt.Errorf("invalid webhook receipt retention")
	}
	dispatcher := &WebhookDispatcher{store: store, snapshots: map[string]webhookAuthSnapshot{}}
	dispatcher.now = options.Now
	if dispatcher.now == nil {
		dispatcher.now = time.Now
	}
	dispatcher.timeout = options.Timeout
	if dispatcher.timeout <= 0 || dispatcher.timeout > 30*time.Second {
		dispatcher.timeout = 5 * time.Second
	}
	if options.MaxConcurrency < 1 || options.MaxConcurrency > 1024 {
		options.MaxConcurrency = 32
	}
	if options.RatePerSecond < 1 || options.RatePerSecond > 10000 {
		options.RatePerSecond = 20
	}
	dispatcher.semaphore = make(chan struct{}, options.MaxConcurrency)
	dispatcher.limiter = &requestLimiter{windows: map[string]requestWindow{}, limit: options.RatePerSecond}
	for _, trigger := range config.Triggers {
		if err := dispatcher.loadTrigger(secretFD, config.Generation, trigger); err != nil {
			return nil, fmt.Errorf("load webhook trigger %q: %w", trigger.TriggerID, err)
		}
	}
	digest := sha256.New()
	_, _ = digest.Write(configBytes)
	sort.Strings(dispatcher.secretDigests)
	for _, fingerprint := range dispatcher.secretDigests {
		_, _ = digest.Write([]byte("\n" + fingerprint))
	}
	authConfig := WebhookAuthConfig{Owner: config.Owner, Generation: config.Generation, Digest: hex.EncodeToString(digest.Sum(nil)), ReceiptRetention: receiptRetention, Triggers: dispatcher.metadata}
	if err := validateWebhookAuthConfig(authConfig); err != nil {
		return nil, fmt.Errorf("validate webhook authentication: %w", err)
	}
	dispatcher.authConfig = authConfig
	dispatcher.metadata = nil
	return dispatcher, nil
}

func (d *WebhookDispatcher) Activate(ctx context.Context) error {
	if err := d.store.ReplaceWebhookAuth(ctx, d.authConfig); err != nil {
		return fmt.Errorf("configure webhook authentication: %w", err)
	}
	return nil
}

func (d *WebhookDispatcher) Deactivate(ctx context.Context) error {
	return d.store.RevokeWebhookAuth(ctx, d.authConfig.Owner, d.authConfig.Generation, d.authConfig.Digest)
}

func (d *WebhookDispatcher) loadTrigger(secretFD int, generation int64, config WebhookTriggerConfig) error {
	if !validWebhookIdentifier(config.TriggerID, 128) || !validWebhookIdentifier(config.Revision, 128) {
		return ErrInvalid
	}
	if _, exists := d.snapshots[config.TriggerID]; exists {
		return ErrInvalid
	}
	metadata := WebhookAuthMetadata{TriggerID: config.TriggerID, Revision: config.Revision}
	snapshot := webhookAuthSnapshot{generation: generation, revision: config.Revision, keys: map[string]webhookSnapshotKey{}, projections: map[string]WebhookProjection{}}
	for _, key := range config.Keys {
		if !validWebhookIdentifier(key.ID, 128) || !webhookSecretBasename.MatchString(key.SecretFile) || filepath.Base(key.SecretFile) != key.SecretFile {
			return ErrInvalid
		}
		if _, exists := snapshot.keys[key.ID]; exists {
			return ErrInvalid
		}
		secret, err := readPrivateFileAt(secretFD, key.SecretFile, true, 4096)
		if err != nil || len(secret) < 32 {
			return ErrInvalid
		}
		snapshot.keys[key.ID] = webhookSnapshotKey{secret: append([]byte(nil), secret...), notBefore: key.NotBefore.UTC(), notAfter: key.NotAfter.UTC()}
		secretDigest := sha256.Sum256(secret)
		d.secretDigests = append(d.secretDigests, config.TriggerID+"\x00"+key.ID+"\x00"+hex.EncodeToString(secretDigest[:]))
		metadata.Keys = append(metadata.Keys, WebhookAuthKey{ID: key.ID, NotBefore: key.NotBefore, NotAfter: key.NotAfter})
	}
	if len(config.Projections) > webhookMaxProjections {
		return ErrInvalid
	}
	for input, projection := range config.Projections {
		if input == "" || len(input) > 128 || sensitiveJSONKey(input) || !validJSONPointer(projection.Pointer) || len(projection.Pointer) > 512 {
			return ErrInvalid
		}
		snapshot.projections[input] = projection
	}
	trigger, ok := d.store.Trigger(config.TriggerID)
	if !ok || trigger.Type != TriggerWebhook || !trigger.Enabled {
		return ErrInvalid
	}
	definition, err := d.store.ActiveWorkflowDefinition(trigger.WorkflowID)
	if err != nil {
		return ErrInvalid
	}
	shapeInputs, projectedTypes := cloneMap(trigger.Inputs), map[string]string{}
	if shapeInputs == nil {
		shapeInputs = map[string]string{}
	}
	for input := range snapshot.projections {
		if _, exists := shapeInputs[input]; exists {
			return ErrInvalid
		}
		property, exists := definition.InputShape.Properties[input]
		if !exists {
			return ErrInvalid
		}
		switch property.Type {
		case "string":
			shapeInputs[input], projectedTypes[input] = "value", "string"
		case "number":
			shapeInputs[input], projectedTypes[input] = "0", "number"
		case "integer":
			shapeInputs[input], projectedTypes[input] = "0", "number"
		case "boolean":
			shapeInputs[input], projectedTypes[input] = "false", "boolean"
		default:
			return ErrInvalid
		}
	}
	if err := validateWebhookRunInputs(definition.InputShape, shapeInputs, projectedTypes); err != nil {
		return err
	}
	d.metadata = append(d.metadata, metadata)
	d.snapshots[config.TriggerID] = snapshot
	return nil
}

func (d *WebhookDispatcher) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /hooks/v1/{triggerID}", d.dispatch)
	return mux
}

func (d *WebhookDispatcher) dispatch(w http.ResponseWriter, r *http.Request) {
	if !d.limiter.allow(webhookClientIP(r), d.now()) {
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "webhook rate limit exceeded")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), d.timeout)
	defer cancel()
	r = r.WithContext(ctx)
	if deadline, ok := ctx.Deadline(); ok {
		_ = http.NewResponseController(w).SetReadDeadline(deadline)
	}
	triggerID := r.PathValue("triggerID")
	snapshot, triggerKnown := d.snapshots[triggerID]
	if r.ContentLength > webhookMaxBodyBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "webhook body exceeds 64 KiB")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, webhookMaxBodyBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read webhook body")
		return
	}
	if int64(len(body)) > webhookMaxBodyBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "webhook body exceeds 64 KiB")
		return
	}
	delivery, timestampText, keyID, signature, ok := webhookHeaders(r.Header)
	timestamp, err := strconv.ParseInt(timestampText, 10, 64)
	now := d.now().UTC()
	requestTime := time.Unix(timestamp, 0).UTC()
	timestampValid := err == nil && strconv.FormatInt(timestamp, 10) == timestampText && !requestTime.Before(now.Add(-5*time.Minute)) && !requestTime.After(now.Add(5*time.Minute))
	key, keyKnown := snapshot.keys[keyID]
	keyActive := keyKnown && !now.Before(key.notBefore) && now.Before(key.notAfter)
	secret := key.secret
	if !triggerKnown || !keyKnown {
		secret = []byte("switchyard-webhook-dummy-key-32b")
	}
	digest := sha256.Sum256(body)
	canonical := WebhookCanonicalString(triggerID, delivery, timestampText, hex.EncodeToString(digest[:]))
	want := hmac.New(sha256.New, secret)
	_, _ = want.Write([]byte(canonical))
	provided := make([]byte, sha256.Size)
	decoded, decodeErr := hex.DecodeString(strings.TrimPrefix(signature, "v1="))
	if len(decoded) == sha256.Size {
		copy(provided, decoded)
	}
	signatureValid := subtle.ConstantTimeCompare(provided, want.Sum(nil)) == 1
	if !ok || !timestampValid || decodeErr != nil || len(decoded) != sha256.Size || !signatureValid || !triggerKnown || !keyKnown || !keyActive || !validWebhookIdentifier(triggerID, 128) {
		writeAuthFailure(w)
		return
	}
	contentType, params, err := mime.ParseMediaType(singleHeader(r.Header, "Content-Type"))
	if err != nil || contentType != "application/json" || len(params) != 0 {
		writeError(w, http.StatusUnsupportedMediaType, "content type must be application/json")
		return
	}
	encoding := singleHeader(r.Header, "Content-Encoding")
	if encoding == "!duplicate!" || (encoding != "" && encoding != "identity") {
		writeError(w, http.StatusUnsupportedMediaType, "content encoding must be identity")
		return
	}
	select {
	case d.semaphore <- struct{}{}:
		defer func() { <-d.semaphore }()
	default:
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, "webhook concurrency limit exceeded")
		return
	}
	inputs, types, err := projectWebhookInputs(body, snapshot.projections)
	if err != nil {
		writeError(w, http.StatusBadRequest, "webhook payload does not satisfy projections")
		return
	}
	admission, err := d.store.AdmitWebhookContext(ctx, AdmitWebhookInput{TriggerID: triggerID, DeliveryID: delivery, BodySHA256: hex.EncodeToString(digest[:]), AuthGeneration: snapshot.generation, AuthRevision: snapshot.revision, KeyID: keyID, Inputs: inputs, InputTypes: types})
	if err != nil {
		switch {
		case errors.Is(err, ErrNotFound):
			writeError(w, http.StatusNotFound, "webhook trigger not found")
		case errors.Is(err, ErrConflict):
			writeError(w, http.StatusConflict, "webhook delivery conflict")
		case errors.Is(err, ErrInvalid):
			writeError(w, http.StatusUnprocessableEntity, "webhook trigger cannot admit payload")
		case errors.Is(err, context.DeadlineExceeded), errors.Is(ctx.Err(), context.DeadlineExceeded):
			writeError(w, http.StatusGatewayTimeout, "webhook admission timed out")
		default:
			writeError(w, http.StatusServiceUnavailable, "webhook admission unavailable")
		}
		return
	}
	status := http.StatusAccepted
	if admission.Replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"run_id": admission.Run.ID, "replayed": admission.Replayed})
}

func WebhookCanonicalString(triggerID, deliveryID, timestamp, bodySHA256 string) string {
	return "switchyard-hmac-v1\ntrigger:" + triggerID + "\ndelivery:" + deliveryID + "\ntimestamp:" + timestamp + "\nbody-sha256:" + bodySHA256
}

func webhookHeaders(header http.Header) (string, string, string, string, bool) {
	delivery := singleHeader(header, WebhookDeliveryHeader)
	timestamp := singleHeader(header, WebhookTimestampHeader)
	keyID := singleHeader(header, WebhookKeyIDHeader)
	signature := singleHeader(header, WebhookSignatureHeader)
	if !validWebhookIdentifier(delivery, 128) || !validWebhookIdentifier(keyID, 128) || len(timestamp) < 1 || len(timestamp) > 11 || len(signature) != 67 || !strings.HasPrefix(signature, "v1=") || !lowerHex(signature[3:]) {
		return "", "", "", "", false
	}
	return delivery, timestamp, keyID, signature, true
}

func singleHeader(header http.Header, name string) string {
	values := header.Values(name)
	if len(values) != 1 || strings.Contains(values[0], ",") || values[0] != strings.TrimSpace(values[0]) {
		if len(values) == 0 {
			return ""
		}
		return "!duplicate!"
	}
	return values[0]
}

func projectWebhookInputs(body []byte, projections map[string]WebhookProjection) (map[string]string, map[string]string, error) {
	if err := validateUniqueJSON(body); err != nil {
		return nil, nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, nil, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, nil, err
	}
	inputs, types := map[string]string{}, map[string]string{}
	for input, projection := range projections {
		projected, ok := jsonPointer(value, projection.Pointer)
		if !ok {
			return nil, nil, ErrInvalid
		}
		switch typed := projected.(type) {
		case string:
			if len(typed) > webhookMaxScalarBytes {
				return nil, nil, ErrInvalid
			}
			inputs[input], types[input] = typed, "string"
		case json.Number:
			if len(typed.String()) > webhookMaxScalarBytes {
				return nil, nil, ErrInvalid
			}
			inputs[input], types[input] = typed.String(), "number"
		case bool:
			inputs[input], types[input] = strconv.FormatBool(typed), "boolean"
		default:
			return nil, nil, ErrInvalid
		}
	}
	if encoded, err := json.Marshal(inputs); err != nil || len(encoded) > webhookMaxEncodedInputBytes {
		return nil, nil, ErrInvalid
	}
	return inputs, types, nil
}

func jsonPointer(value any, pointer string) (any, bool) {
	tokens, ok := parseJSONPointer(pointer)
	if !ok {
		return nil, false
	}
	current := value
	for _, token := range tokens {
		switch typed := current.(type) {
		case map[string]any:
			var ok bool
			current, ok = typed[token]
			if !ok {
				return nil, false
			}
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(typed) || strconv.Itoa(index) != token {
				return nil, false
			}
			current = typed[index]
		default:
			return nil, false
		}
	}
	return current, true
}

func validJSONPointer(pointer string) bool {
	_, ok := parseJSONPointer(pointer)
	return ok
}

func parseJSONPointer(pointer string) ([]string, bool) {
	if pointer == "" || pointer[0] != '/' {
		return nil, false
	}
	for index := 0; index < len(pointer); index++ {
		if pointer[index] != '~' {
			continue
		}
		if index+1 >= len(pointer) || (pointer[index+1] != '0' && pointer[index+1] != '1') {
			return nil, false
		}
		index++
	}
	rawTokens := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	tokens := make([]string, len(rawTokens))
	for index, raw := range rawTokens {
		tokens[index] = strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		if sensitiveJSONKey(tokens[index]) {
			return nil, false
		}
	}
	return tokens, true
}

func validateUniqueJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := consumeUniqueJSONValue(decoder); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}

func consumeUniqueJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok || seen[key] {
				return ErrInvalid
			}
			seen[key] = true
			if err := consumeUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return ErrInvalid
		}
	case '[':
		for decoder.More() {
			if err := consumeUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return ErrInvalid
		}
		return err
	}
	return nil
}

func openAbsoluteDirectory(path string) (int, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return -1, ErrInvalid
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if part == "" {
			continue
		}
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if openErr != nil {
			return -1, ErrInvalid
		}
		fd = next
	}
	return fd, nil
}

func openPrivateDirectory(path string) (int, error) {
	fd, err := openAbsoluteDirectory(path)
	if err != nil {
		return -1, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0777 != 0700 || int(stat.Uid) != os.Geteuid() {
		unix.Close(fd)
		return -1, ErrInvalid
	}
	return fd, nil
}

func readPrivatePath(path string, readOnly bool, max int64) ([]byte, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrInvalid
	}
	dirFD, err := openAbsoluteDirectory(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer unix.Close(dirFD)
	return readPrivateFileAt(dirFD, filepath.Base(path), readOnly, max)
}

func readPrivateFileAt(dirFD int, name string, readOnly bool, max int64) ([]byte, error) {
	if name == "" || filepath.Base(name) != name {
		return nil, ErrInvalid
	}
	fd, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrInvalid
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || int(stat.Uid) != os.Geteuid() || stat.Size < 1 || stat.Size > max {
		return nil, ErrInvalid
	}
	mode := stat.Mode & 0777
	if (readOnly && mode != 0400) || (!readOnly && mode != 0400 && mode != 0600) {
		return nil, ErrInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(file, max+1))
	if err != nil || int64(len(raw)) > max || int64(len(raw)) != stat.Size {
		return nil, ErrInvalid
	}
	return raw, nil
}

func validWebhookIdentifier(value string, max int) bool {
	return len(value) >= 1 && len(value) <= max && webhookIdentifier.MatchString(value)
}

func lowerHex(value string) bool {
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func writeAuthFailure(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `HMAC realm="switchyard-webhook", algorithm="hmac-sha256"`)
	writeError(w, http.StatusUnauthorized, "webhook authentication failed")
}

func webhookClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	return r.RemoteAddr
}
