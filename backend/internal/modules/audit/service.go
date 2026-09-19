package audit

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
	"github.com/uptrace/bun"
)

var (
	ErrInvalidArgument    = errors.New("audit invalid argument")
	ErrForbidden          = errors.New("audit forbidden")
	ErrNotFound           = errors.New("audit fact not found")
	ErrRetentionProtected = errors.New("audit retention policy prevents cleanup")
	ErrInternal           = errors.New("audit internal failure")
)

type Permission string

const (
	PermissionRead    Permission = "audit.records.read"
	PermissionCleanup Permission = "audit.records.cleanup"
)

const CleanupConfirmation = "delete-expired-audit-records"

type Principal struct{ ID string }

type AuthorizationDecision uint8

const (
	AuthorizationGranted AuthorizationDecision = iota + 1
	AuthorizationDenied
)

type Authorizer interface {
	Authorize(context.Context, database.Tx, Principal, Permission) (AuthorizationDecision, error)
}

type Observation struct {
	Operation string        `json:"operation"`
	Outcome   string        `json:"outcome"`
	Count     int           `json:"count,omitempty"`
	Duration  time.Duration `json:"duration"`
}

type Observer interface {
	Observe(Observation)
}

type RetentionPolicy struct {
	MinimumAge   time.Duration
	CleanupLimit int
	Now          func() time.Time
	Observer     Observer
}

type Filter struct {
	Page     int
	PageSize int
	Kind     Kind
	Action   string
	Outcome  Outcome
	Source   Source
	From     time.Time
	To       time.Time
}

type Fact struct {
	EventID    *string   `json:"eventId,omitempty"`
	TraceID    *string   `json:"traceId,omitempty"`
	Operation  *string   `json:"operation,omitempty"`
	ID         string    `json:"id"`
	Kind       Kind      `json:"kind"`
	Action     string    `json:"action"`
	Outcome    Outcome   `json:"outcome"`
	ActorType  ActorType `json:"actorType"`
	Source     Source    `json:"source"`
	Subject    string    `json:"subject"`
	ActorRef   *string   `json:"actorRef,omitempty"`
	OccurredAt time.Time `json:"occurredAt"`
}

type Page struct {
	Records  []Fact `json:"records"`
	Total    int    `json:"total"`
	Page     int    `json:"page"`
	PageSize int    `json:"pageSize"`
}

type CleanupCommand struct {
	Before       time.Time
	Confirmation string
}

type CleanupResult struct {
	Deleted      int  `json:"deleted"`
	MoreEligible bool `json:"moreEligible"`
}

type Service struct {
	db           *database.Database
	authorizer   Authorizer
	minimumAge   time.Duration
	cleanupLimit int
	now          func() time.Time
	observer     Observer
}

func NewService(db *database.Database, authorizer Authorizer, retention RetentionPolicy) (*Service, error) {
	if db == nil || authorizer == nil || retention.MinimumAge <= 0 || retention.CleanupLimit < 1 || retention.CleanupLimit > 1000 {
		return nil, ErrInvalidArgument
	}
	if retention.Now == nil {
		retention.Now = time.Now
	}
	if retention.Observer == nil {
		retention.Observer = discardObserver{}
	}
	return &Service{db: db, authorizer: authorizer, minimumAge: retention.MinimumAge, cleanupLimit: retention.CleanupLimit, now: retention.Now, observer: retention.Observer}, nil
}

func (service *Service) List(ctx context.Context, principal Principal, filter Filter) (result Page, resultErr error) {
	started := service.now()
	defer func() { service.observe("list", resultErr, len(result.Records), started) }()
	if err := validatePrincipal(principal); err != nil {
		return Page{}, err
	}
	if err := validateFilter(filter); err != nil {
		return Page{}, err
	}
	err := service.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		if err := authorize(ctx, service.authorizer, tx, principal, PermissionRead); err != nil {
			return err
		}
		var err error
		result, err = listFacts(ctx, tx, service.db.Dialect(), filter)
		return err
	})
	if errors.Is(err, ErrForbidden) {
		return Page{}, ErrForbidden
	}
	if err != nil {
		return Page{}, conceal(err)
	}
	return result, nil
}

func (service *Service) Detail(ctx context.Context, principal Principal, id string) (result Fact, resultErr error) {
	started := service.now()
	defer func() { service.observe("detail", resultErr, boolCount(resultErr == nil), started) }()
	topic, businessKey, validID := decodeFactID(id)
	if err := validatePrincipal(principal); err != nil || !validID {
		return Fact{}, ErrInvalidArgument
	}
	row := storedFact{}
	err := service.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		if err := authorize(ctx, service.authorizer, tx, principal, PermissionRead); err != nil {
			return err
		}
		return tx.NewSelect().Table("audit_facts").Column("topic", "business_key", "actor_ref", "payload", "occurred_at", "event_id", "target_type", "target_id", "operation_code", "outcome", "trace_id").Where("topic = ? AND business_key = ?", topic, businessKey).Limit(1).Scan(ctx, &row)
	})
	if errors.Is(err, ErrForbidden) {
		return Fact{}, ErrForbidden
	}
	if errors.Is(err, sql.ErrNoRows) {
		return Fact{}, ErrNotFound
	}
	if err != nil {
		return Fact{}, conceal(err)
	}
	return present(row)
}

func (service *Service) Cleanup(ctx context.Context, principal Principal, command CleanupCommand) (result CleanupResult, resultErr error) {
	started := service.now()
	defer func() { service.observe("cleanup", resultErr, result.Deleted, started) }()
	if err := validatePrincipal(principal); err != nil || command.Confirmation != CleanupConfirmation || command.Before.IsZero() {
		return CleanupResult{}, ErrInvalidArgument
	}
	cutoff := service.now().UTC().Add(-service.minimumAge)
	if command.Before.UTC().After(cutoff) {
		return CleanupResult{}, ErrRetentionProtected
	}
	err := service.db.WithinTx(ctx, func(ctx context.Context, tx database.Tx) error {
		if err := authorize(ctx, service.authorizer, tx, principal, PermissionCleanup); err != nil {
			return err
		}
		keys := make([]storedFactKey, 0, service.cleanupLimit+1)
		if err := tx.NewSelect().Table("audit_facts").Column("topic", "business_key").Where("occurred_at < ?", command.Before.UTC()).OrderExpr("occurred_at ASC, topic ASC, business_key ASC").Limit(service.cleanupLimit+1).Scan(ctx, &keys); err != nil {
			return err
		}
		result.MoreEligible = len(keys) > service.cleanupLimit
		if result.MoreEligible {
			keys = keys[:service.cleanupLimit]
		}
		if len(keys) == 0 {
			return nil
		}
		for _, key := range keys {
			sqlResult, err := tx.NewDelete().Table("audit_facts").Where("topic = ? AND business_key = ?", key.Topic, key.BusinessKey).Exec(ctx)
			if err != nil {
				return err
			}
			deleted, err := sqlResult.RowsAffected()
			if err != nil || deleted != 1 {
				return ErrInternal
			}
		}
		result.Deleted = len(keys)
		return (OperationRecorder{}).RecordOperation(ctx, tx, Operation{Action: "delete", ResourceType: "audit", ResourceID: "retention", ActorID: principal.ID, Code: "CleanupAudit"})
	})
	if errors.Is(err, ErrForbidden) {
		return CleanupResult{}, ErrForbidden
	}
	if err != nil {
		return CleanupResult{}, conceal(err)
	}
	return result, nil
}

type storedFact struct {
	EventID     *string   `bun:"event_id"`
	TargetType  *string   `bun:"target_type"`
	TargetID    *string   `bun:"target_id"`
	Operation   *string   `bun:"operation_code"`
	Outcome     *Outcome  `bun:"outcome"`
	TraceID     *string   `bun:"trace_id"`
	Topic       string    `bun:"topic"`
	BusinessKey string    `bun:"business_key"`
	ActorRef    *string   `bun:"actor_ref"`
	Payload     []byte    `bun:"payload"`
	OccurredAt  time.Time `bun:"occurred_at"`
}

type storedFactKey struct {
	Topic       string `bun:"topic"`
	BusinessKey string `bun:"business_key"`
}

func listFacts(ctx context.Context, tx database.Tx, dialect database.Dialect, filter Filter) (Page, error) {
	total, err := applyFilter(tx.NewSelect().Table("audit_facts"), dialect, filter).Count(ctx)
	if err != nil {
		return Page{}, err
	}
	rows := make([]storedFact, 0, filter.PageSize)
	err = applyFilter(tx.NewSelect().Table("audit_facts"), dialect, filter).Column("topic", "business_key", "actor_ref", "payload", "occurred_at", "event_id", "target_type", "target_id", "operation_code", "outcome", "trace_id").OrderExpr("occurred_at DESC, topic ASC, business_key ASC").Limit(filter.PageSize).Offset((filter.Page-1)*filter.PageSize).Scan(ctx, &rows)
	if err != nil {
		return Page{}, err
	}
	records := make([]Fact, 0, len(rows))
	for _, row := range rows {
		fact, err := present(row)
		if err != nil {
			return Page{}, err
		}
		records = append(records, fact)
	}
	return Page{Records: records, Total: total, Page: filter.Page, PageSize: filter.PageSize}, nil
}

func applyFilter(query *bun.SelectQuery, dialect database.Dialect, filter Filter) *bun.SelectQuery {
	if filter.Kind != "" {
		query = query.Where("topic IN (?)", bun.In(matchingTopics(func(definition topicDefinition) bool { return definition.kind == filter.Kind })))
	}
	if filter.Action != "" {
		query = query.Where("topic IN (?)", bun.In(matchingTopics(func(definition topicDefinition) bool { return definition.action == filter.Action })))
	}
	if filter.Outcome != "" {
		query = query.Where("(outcome = ? OR (outcome IS NULL AND topic IN (?)))", filter.Outcome, bun.In(matchingTopics(func(definition topicDefinition) bool { return definition.outcome == filter.Outcome })))
	}
	if filter.Source != "" {
		if dialect == database.DialectPostgres {
			query = query.Where("convert_from(payload, 'UTF8')::jsonb ->> 'source' = ?", filter.Source)
		} else {
			query = query.Where("json_extract(CAST(payload AS TEXT), '$.source') = ?", filter.Source)
		}
	}
	if !filter.From.IsZero() {
		query = query.Where("occurred_at >= ?", filter.From.UTC())
	}
	if !filter.To.IsZero() {
		query = query.Where("occurred_at < ?", filter.To.UTC())
	}
	return query
}

func validateFilter(filter Filter) error {
	if filter.Page < 1 || filter.Page > 1_000_000 || filter.PageSize < 1 || filter.PageSize > 100 ||
		filter.Kind != "" && filter.Kind != KindLogin && filter.Kind != KindOperation ||
		filter.Action != "" && filter.Action != "login" && filter.Action != "create" && filter.Action != "update" && filter.Action != "delete" ||
		filter.Outcome != "" && filter.Outcome != OutcomeSucceeded && filter.Outcome != OutcomeFailed ||
		filter.Source != "" && filter.Source != SourceWeb && filter.Source != SourceDesktop && filter.Source != SourceServer ||
		!filter.From.IsZero() && !filter.To.IsZero() && !filter.From.Before(filter.To) {
		return ErrInvalidArgument
	}
	return nil
}

func matchingTopics(matches func(topicDefinition) bool) []string {
	result := make([]string, 0, len(topicDefinitions))
	for topic, definition := range topicDefinitions {
		if matches(definition) {
			result = append(result, topic)
		}
	}
	return result
}

func validatePrincipal(principal Principal) error {
	if len(principal.ID) < 8 || len(principal.ID) > 64 {
		return ErrInvalidArgument
	}
	return nil
}

var factTopicAliases = map[string]string{
	TopicLoginSucceeded:   "ls",
	TopicLoginFailed:      "lf",
	TopicOperationCreated: "oc",
	TopicOperationUpdated: "ou",
	TopicOperationDeleted: "od",
}

var aliasTopics = map[string]string{
	"ls": TopicLoginSucceeded,
	"lf": TopicLoginFailed,
	"oc": TopicOperationCreated,
	"ou": TopicOperationUpdated,
	"od": TopicOperationDeleted,
}

func encodeFactID(topic, businessKey string) (string, bool) {
	alias, ok := factTopicAliases[topic]
	if !ok || !validStoredBusinessKey(topic, businessKey) {
		return "", false
	}
	return "a1." + alias + "." + base64.RawURLEncoding.EncodeToString([]byte(businessKey)), true
}

func validStoredBusinessKey(topic, businessKey string) bool {
	definition, found := topicDefinitions[topic]
	parts := strings.Split(businessKey, ":")
	if !found || len(parts) < 2 || parts[0] != definition.keyPrefix {
		return false
	}
	for _, part := range parts[1:] {
		if !stableKeyPart.MatchString(part) {
			return false
		}
	}
	if definition.kind == KindLogin {
		return len(parts) == 2 && opaqueLoginBusinessID.MatchString(parts[1])
	}
	return len(parts) == 3 || len(parts) == 4 && validOperationActorID(parts[3])
}

func decodeFactID(id string) (string, string, bool) {
	parts := strings.Split(id, ".")
	if len(parts) != 3 || parts[0] != "a1" {
		return "", "", false
	}
	topic, ok := aliasTopics[parts[1]]
	if !ok {
		return "", "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", "", false
	}
	businessKey := string(raw)
	encoded, valid := encodeFactID(topic, businessKey)
	return topic, businessKey, valid && encoded == id
}

func authorize(ctx context.Context, authorizer Authorizer, tx database.Tx, principal Principal, permission Permission) error {
	decision, err := authorizer.Authorize(ctx, tx, principal, permission)
	if err != nil {
		return err
	}
	switch decision {
	case AuthorizationGranted:
		return nil
	case AuthorizationDenied:
		return ErrForbidden
	default:
		return ErrInternal
	}
}

func present(row storedFact) (Fact, error) {
	definition, found := topicDefinitions[row.Topic]
	if !found {
		return Fact{}, ErrInternal
	}
	var envelope struct {
		ActorType ActorType `json:"actorType,omitempty"`
		Source    Source    `json:"source"`
	}
	decoder := json.NewDecoder(bytes.NewReader(row.Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return Fact{}, ErrInternal
	}
	if envelope.Source != SourceWeb && envelope.Source != SourceDesktop && envelope.Source != SourceServer {
		return Fact{}, ErrInternal
	}
	subject, actorType, actorRef, ok := presentIdentity(definition.kind, row.Topic, row.BusinessKey, envelope.ActorType, row.ActorRef)
	id, validID := encodeFactID(row.Topic, row.BusinessKey)
	if !ok || !validID {
		return Fact{}, ErrInternal
	}
	if row.TargetType != nil && row.TargetID != nil {
		subject = *row.TargetType + ":" + *row.TargetID
	}
	if row.Outcome != nil {
		definition.outcome = *row.Outcome
	}
	return Fact{EventID: row.EventID, TraceID: row.TraceID, Operation: row.Operation, ID: id, Kind: definition.kind, Action: definition.action, Outcome: definition.outcome, ActorType: actorType, Source: envelope.Source, Subject: subject, ActorRef: actorRef, OccurredAt: row.OccurredAt}, nil
}

func presentIdentity(kind Kind, topic, businessKey string, payloadActor ActorType, storedActorRef *string) (string, ActorType, *string, bool) {
	parts := strings.Split(businessKey, ":")
	definition, found := topicDefinitions[topic]
	if !found || !validStoredBusinessKey(topic, businessKey) {
		return "", "", nil, false
	}
	if kind == KindOperation {
		if topic == TopicOperationUpdated && len(parts) == 4 && parts[1] == "iam_recovery" && storedActorRef != nil && *storedActorRef == "account:"+parts[2] {
			actorRef := "account:" + parts[2]
			return strings.Join(parts[1:3], ":"), ActorAccount, &actorRef, true
		}
		// 显式操作者必须与稳定业务键中的身份一致，避免将系统事实伪装为账号事实。
		if payloadActor != "" || storedActorRef != nil && (len(parts) != 4 || *storedActorRef != "account:"+parts[3]) {
			return "", "", nil, false
		}
		subject := strings.Join(parts[1:3], ":")
		if len(parts) == 3 {
			return subject, ActorSystem, nil, true
		}
		if len(parts) == 4 {
			actorRef := "account:" + parts[3]
			return subject, ActorAccount, &actorRef, true
		}
		return "", "", nil, false
	}
	if len(parts) != 2 || payloadActor != ActorAccount ||
		definition.outcome == OutcomeSucceeded && (storedActorRef == nil || !validActorRef(*storedActorRef)) ||
		definition.outcome == OutcomeFailed && storedActorRef != nil {
		return "", "", nil, false
	}
	return strings.Join(parts[:2], ":"), payloadActor, cloneString(storedActorRef), true
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func (service *Service) observe(operation string, err error, count int, started time.Time) {
	outcome := "succeeded"
	if err != nil {
		outcome = "failed"
	}
	service.observer.Observe(Observation{Operation: operation, Outcome: outcome, Count: count, Duration: service.now().Sub(started)})
}

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}

type discardObserver struct{}

func (discardObserver) Observe(Observation) {}

func conceal(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return errors.Join(ErrInternal, err)
	}
	return ErrInternal
}

// CleanupExpiredInTx 供产品装配的受控维护任务调用；保留期不能由任务参数绕过。
func (service *Service) CleanupExpiredInTx(ctx context.Context, tx database.Tx) error {
	before := service.now().UTC().Add(-service.minimumAge)
	_, err := tx.ExecContext(ctx, `DELETE FROM audit_facts WHERE (topic, business_key) IN (SELECT topic, business_key FROM audit_facts WHERE occurred_at < ? ORDER BY occurred_at LIMIT ?)`, before, service.cleanupLimit)
	return err
}
