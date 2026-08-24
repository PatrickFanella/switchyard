package controlplane

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

type webhookReceipt struct {
	bodySHA256 string
	runID      string
	acceptedAt time.Time
}

const (
	webhookMaxProjections       = 32
	webhookMaxScalarBytes       = 4096
	webhookMaxEncodedInputBytes = 16 << 10
	webhookMinReceiptRetention  = 24 * time.Hour
	webhookMaxReceiptRetention  = 366 * 24 * time.Hour
)

func validateWebhookAuthConfig(config WebhookAuthConfig) error {
	if !validWebhookIdentifier(config.Owner, 128) || config.Generation < 1 || len(config.Digest) != 64 || !lowerHex(config.Digest) || config.ReceiptRetention < webhookMinReceiptRetention || config.ReceiptRetention > webhookMaxReceiptRetention || config.ReceiptRetention%time.Second != 0 || len(config.Triggers) == 0 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, metadata := range config.Triggers {
		if seen[metadata.TriggerID] || validateWebhookAuthMetadata(metadata) != nil {
			return ErrInvalid
		}
		seen[metadata.TriggerID] = true
	}
	return nil
}

func validateWebhookAuthMetadata(metadata WebhookAuthMetadata) error {
	metadata.TriggerID = strings.TrimSpace(metadata.TriggerID)
	metadata.Revision = strings.TrimSpace(metadata.Revision)
	if metadata.TriggerID == "" || metadata.Revision == "" || len(metadata.Revision) > 128 || len(metadata.Keys) < 1 || len(metadata.Keys) > 2 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, key := range metadata.Keys {
		if key.ID == "" || len(key.ID) > 128 || seen[key.ID] || key.NotBefore.IsZero() || !key.NotAfter.After(key.NotBefore) || key.NotAfter.After(key.NotBefore.Add(webhookMaxReceiptRetention)) {
			return ErrInvalid
		}
		seen[key.ID] = true
	}
	if len(metadata.Keys) == 2 {
		start := metadata.Keys[0].NotBefore
		if metadata.Keys[1].NotBefore.After(start) {
			start = metadata.Keys[1].NotBefore
		}
		end := metadata.Keys[0].NotAfter
		if metadata.Keys[1].NotAfter.Before(end) {
			end = metadata.Keys[1].NotAfter
		}
		if end.After(start.Add(24 * time.Hour)) {
			return ErrInvalid
		}
	}
	return nil
}

func (s *MemoryStore) ReplaceWebhookAuth(ctx context.Context, config WebhookAuthConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateWebhookAuthConfig(config); err != nil {
		return err
	}
	if err := lockWebhookStore(ctx, &s.mu); err != nil {
		return err
	}
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.webhookAuthOwner != "" && s.webhookAuthOwner != config.Owner {
		return ErrConflict
	}
	if config.Generation < s.webhookAuthGeneration || (config.Generation == s.webhookAuthGeneration && config.Digest != s.webhookAuthDigest) {
		return ErrConflict
	}
	replacement := make(map[string]WebhookAuthMetadata, len(config.Triggers))
	for _, metadata := range config.Triggers {
		trigger, ok := s.triggers[metadata.TriggerID]
		if !ok {
			return ErrNotFound
		}
		if trigger.Type != TriggerWebhook {
			return ErrInvalid
		}
		copy := metadata
		copy.Keys = append([]WebhookAuthKey(nil), metadata.Keys...)
		replacement[metadata.TriggerID] = copy
	}
	s.webhookAuth = replacement
	s.webhookAuthOwner, s.webhookAuthGeneration, s.webhookAuthDigest = config.Owner, config.Generation, config.Digest
	s.webhookReceiptRetention = config.ReceiptRetention
	return nil
}

func (s *MemoryStore) RevokeWebhookAuth(ctx context.Context, owner string, generation int64, digest string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validWebhookIdentifier(owner, 128) || generation < 1 || len(digest) != 64 || !lowerHex(digest) {
		return ErrInvalid
	}
	if err := lockWebhookStore(ctx, &s.mu); err != nil {
		return err
	}
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.webhookAuthOwner == owner && s.webhookAuthGeneration == generation && s.webhookAuthDigest == digest {
		s.webhookAuth = map[string]WebhookAuthMetadata{}
	}
	return nil
}

type webhookMutex interface {
	TryLock() bool
}

func lockWebhookStore(ctx context.Context, mutex webhookMutex) error {
	for !mutex.TryLock() {
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
	return nil
}

func (s *MemoryStore) AdmitWebhook(input AdmitWebhookInput) (WebhookAdmission, error) {
	return s.AdmitWebhookContext(context.Background(), input)
}

func (s *MemoryStore) AdmitWebhookContext(ctx context.Context, input AdmitWebhookInput) (WebhookAdmission, error) {
	if err := ctx.Err(); err != nil {
		return WebhookAdmission{}, err
	}
	if err := validateWebhookAdmissionInput(input); err != nil {
		return WebhookAdmission{}, err
	}
	if err := lockWebhookStore(ctx, &s.mu); err != nil {
		return WebhookAdmission{}, err
	}
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return WebhookAdmission{}, err
	}
	trigger, ok := s.triggers[input.TriggerID]
	if !ok {
		return WebhookAdmission{}, ErrNotFound
	}
	if trigger.Type != TriggerWebhook || !trigger.Enabled {
		return WebhookAdmission{}, ErrInvalid
	}
	auth, ok := s.webhookAuth[input.TriggerID]
	if !ok || s.webhookAuthGeneration != input.AuthGeneration || auth.Revision != input.AuthRevision || !webhookKeyValid(auth.Keys, input.KeyID, s.now().UTC()) {
		return WebhookAdmission{}, ErrConflict
	}
	receiptKey := input.TriggerID + "\x00" + input.DeliveryID
	now := s.now().UTC()
	receipt, receiptExists := s.webhookReceipts[receiptKey]
	receiptExpired := receiptExists && !receipt.acceptedAt.After(now.Add(-s.webhookReceiptRetention))
	if receiptExists && !receiptExpired {
		if receipt.bodySHA256 != input.BodySHA256 {
			return WebhookAdmission{}, ErrConflict
		}
		run, ok := s.runs[receipt.runID]
		if !ok {
			run.ID = receipt.runID
		}
		return WebhookAdmission{Run: run, Replayed: true}, nil
	}
	definition, ok := s.workflowDefinitions[trigger.WorkflowID]
	if !ok || !isActiveWorkflowDefinition(definition) {
		return WebhookAdmission{}, ErrInvalid
	}
	inputs := cloneMap(trigger.Inputs)
	if inputs == nil {
		inputs = map[string]string{}
	}
	for key, value := range input.Inputs {
		if _, exists := inputs[key]; exists {
			return WebhookAdmission{}, ErrInvalid
		}
		inputs[key] = value
	}
	if err := validateWebhookRunInputs(definition.InputShape, inputs, input.InputTypes); err != nil {
		return WebhookAdmission{}, err
	}
	snapshot := pipelineFromDefinition(definition)
	run := Run{ID: newID("run"), WorkflowID: trigger.WorkflowID, TriggerID: trigger.ID, Status: RunQueued, Inputs: cloneMap(inputs), CreatedAt: now, QueuedAt: now, AvailableAt: now, MaxAttempts: defaultRunMaxAttempts(), PipelineID: snapshot.ID, PipelineVersion: pipelineVersion(snapshot), PipelineSnapshot: &snapshot}
	if err := ctx.Err(); err != nil {
		return WebhookAdmission{}, err
	}
	if receiptExpired {
		delete(s.webhookReceipts, receiptKey)
		s.webhookReceiptsPruned++
	}
	s.runs[run.ID] = run
	s.webhookReceipts[receiptKey] = webhookReceipt{bodySHA256: input.BodySHA256, runID: run.ID, acceptedAt: now}
	s.events[run.ID] = []RunEvent{
		{ID: newID("evt"), RunID: run.ID, Type: "run.created", Message: "Run queued", CreatedAt: now},
		{ID: newID("evt"), RunID: run.ID, Type: "webhook.accepted", Message: "Webhook delivery accepted", CreatedAt: now},
	}
	return WebhookAdmission{Run: run}, nil
}

func validateWebhookAdmissionInput(input AdmitWebhookInput) error {
	if input.TriggerID == "" || input.DeliveryID == "" || len(input.DeliveryID) > 128 || len(input.BodySHA256) != 64 || input.AuthGeneration < 1 || input.AuthRevision == "" || input.KeyID == "" || len(input.Inputs) > webhookMaxProjections || len(input.InputTypes) != len(input.Inputs) {
		return ErrInvalid
	}
	for _, char := range input.BodySHA256 {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return ErrInvalid
		}
	}
	for key, value := range input.Inputs {
		inputType, ok := input.InputTypes[key]
		if !ok || sensitiveJSONKey(key) || len(key) > 128 || len(value) > webhookMaxScalarBytes || (inputType != "string" && inputType != "number" && inputType != "boolean") {
			return ErrInvalid
		}
	}
	if encoded, err := json.Marshal(input.Inputs); err != nil || len(encoded) > webhookMaxEncodedInputBytes {
		return ErrInvalid
	}
	return nil
}

func webhookKeyValid(keys []WebhookAuthKey, keyID string, now time.Time) bool {
	for _, key := range keys {
		if key.ID == keyID && !now.Before(key.NotBefore) && now.Before(key.NotAfter) {
			return true
		}
	}
	return false
}

func validateWebhookRunInputs(shape JSONShape, inputs, projectedTypes map[string]string) error {
	if shape.Type != "object" || len(projectedTypes) > webhookMaxProjections {
		return ErrInvalid
	}
	for key, value := range inputs {
		property, ok := shape.Properties[key]
		if !ok || sensitiveJSONKey(key) || len(value) > webhookMaxScalarBytes {
			return ErrInvalid
		}
		if projectedType := projectedTypes[key]; projectedType != "" {
			if projectedType != property.Type && !(projectedType == "number" && property.Type == "integer") {
				return ErrInvalid
			}
		}
		switch property.Type {
		case "string":
		case "number":
			if _, err := strconv.ParseFloat(inputs[key], 64); err != nil {
				return ErrInvalid
			}
		case "integer":
			if _, err := strconv.ParseInt(inputs[key], 10, 64); err != nil {
				return ErrInvalid
			}
		case "boolean":
			if inputs[key] != "true" && inputs[key] != "false" {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
	}
	for key := range projectedTypes {
		if _, ok := inputs[key]; !ok {
			return ErrInvalid
		}
	}
	for _, required := range shape.Required {
		if _, ok := inputs[required]; !ok {
			return ErrInvalid
		}
	}
	encoded, err := json.Marshal(inputs)
	if err != nil || len(encoded) > webhookMaxEncodedInputBytes {
		return ErrInvalid
	}
	return nil
}
