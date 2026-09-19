package audit

import (
	"context"
	"encoding/json"
	"regexp"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/auditing"
	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/google/uuid"
)

type Operation = auditing.Operation
type OperationPort = auditing.OperationPort
type OperationRecorder struct{}
type requestMetadata struct {
	source Source
	trace  string
}
type metadataKey struct{}

var operationPart = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func WithRequestMetadata(ctx context.Context, source Source, trace string) context.Context {
	return context.WithValue(ctx, metadataKey{}, requestMetadata{source: source, trace: trace})
}
func RequestSource(ctx context.Context) Source {
	if value, ok := ctx.Value(metadataKey{}).(requestMetadata); ok {
		return value.source
	}
	return SourceServer
}

// RecordOperation 必须与成功业务变更使用同一事务，失败会使业务一并回滚。
func (OperationRecorder) RecordOperation(ctx context.Context, tx database.Tx, fact Operation) error {
	if tx == nil || !operationPart.MatchString(fact.ResourceType) || !operationPart.MatchString(fact.ResourceID) || len(fact.Code) > 100 {
		return ErrInvalidArgument
	}
	topic := map[string]string{"create": TopicOperationCreated, "update": TopicOperationUpdated, "delete": TopicOperationDeleted}[fact.Action]
	if topic == "" {
		return ErrInvalidArgument
	}
	id := uuid.NewString()
	key := "resource:" + fact.ResourceType + ":" + id
	var actorRef *string
	if fact.ActorID != "" {
		if !operationPart.MatchString(fact.ActorID) || len(fact.ActorID) < 8 {
			return ErrInvalidArgument
		}
		value := "account:" + fact.ActorID
		actorRef = &value
		key += ":" + fact.ActorID
	}
	metadata, _ := ctx.Value(metadataKey{}).(requestMetadata)
	source := RequestSource(ctx)
	payload, err := json.Marshal(struct {
		Source Source `json:"source"`
	}{source})
	if err != nil {
		return err
	}
	var trace any
	if len(metadata.trace) >= 16 && len(metadata.trace) <= 64 {
		trace = metadata.trace
	}
	outcome := OutcomeSucceeded
	if fact.Failed {
		outcome = OutcomeFailed
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_facts(topic,business_key,actor_ref,payload,occurred_at,event_id,target_type,target_id,operation_code,outcome,trace_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, topic, key, actorRef, payload, time.Now().UTC(), id, fact.ResourceType, fact.ResourceID, fact.Code, outcome, trace)
	return err
}
