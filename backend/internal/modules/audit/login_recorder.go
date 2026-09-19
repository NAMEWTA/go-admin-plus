package audit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

var stableKeyPart = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
var stableActorRef = regexp.MustCompile(`^account:[a-z0-9][a-z0-9_-]{7,63}$`)
var opaqueLoginBusinessID = regexp.MustCompile(`^[a-f0-9]{32}$`)

type LoginFact struct {
	Outcome    Outcome
	ActorType  ActorType
	ActorRef   *string
	Source     Source
	OccurredAt time.Time
}

// LoginRecorder is the Audit-owned synchronous port for login facts that must commit with a caller transaction.
type LoginRecorder struct {
	newBusinessID func() (string, error)
}

func NewLoginRecorder(db *database.Database) (*LoginRecorder, error) {
	if db == nil {
		return nil, ErrInvalidArgument
	}
	return &LoginRecorder{newBusinessID: newLoginBusinessID}, nil
}

func (recorder *LoginRecorder) Record(ctx context.Context, tx database.Tx, fact LoginFact) (bool, error) {
	if recorder == nil || recorder.newBusinessID == nil {
		return false, ErrInvalidArgument
	}
	businessID, err := recorder.newBusinessID()
	if err != nil {
		return false, ErrInternal
	}
	return recorder.record(ctx, tx, businessID, fact)
}

// RecordAttempt preserves a caller-owned opaque attempt identifier while Audit owns validation
// and persistence of the mapped login fact.
func (recorder *LoginRecorder) RecordAttempt(ctx context.Context, tx database.Tx, attemptID string, fact LoginFact) (bool, error) {
	return recorder.record(ctx, tx, attemptID, fact)
}

func (recorder *LoginRecorder) record(ctx context.Context, tx database.Tx, businessID string, fact LoginFact) (bool, error) {
	if recorder == nil || tx == nil || !opaqueLoginBusinessID.MatchString(businessID) || fact.ActorType != ActorAccount ||
		fact.Outcome != OutcomeSucceeded && fact.Outcome != OutcomeFailed ||
		fact.Outcome == OutcomeSucceeded && (fact.ActorRef == nil || !validActorRef(*fact.ActorRef)) ||
		fact.Outcome == OutcomeFailed && fact.ActorRef != nil ||
		fact.Source != SourceWeb && fact.Source != SourceDesktop && fact.Source != SourceServer || fact.OccurredAt.IsZero() {
		return false, ErrInvalidArgument
	}
	topic := TopicLoginSucceeded
	if fact.Outcome == OutcomeFailed {
		topic = TopicLoginFailed
	}
	payload, err := json.Marshal(struct {
		ActorType ActorType `json:"actorType"`
		Source    Source    `json:"source"`
	}{ActorType: fact.ActorType, Source: fact.Source})
	if err != nil {
		return false, ErrInternal
	}
	row := &storedLoginFact{Topic: topic, BusinessKey: "login:" + businessID, ActorRef: cloneString(fact.ActorRef), Payload: payload, OccurredAt: fact.OccurredAt.UTC()}
	result, err := tx.NewInsert().Model(row).ModelTableExpr("audit_facts").Exec(ctx)
	if err != nil {
		return false, conceal(err)
	}
	created, err := result.RowsAffected()
	if err != nil {
		return false, ErrInternal
	}
	if created != 1 {
		return false, ErrInternal
	}
	return true, nil
}

type storedLoginFact struct {
	Topic       string    `bun:"topic"`
	BusinessKey string    `bun:"business_key"`
	ActorRef    *string   `bun:"actor_ref"`
	Payload     []byte    `bun:"payload"`
	OccurredAt  time.Time `bun:"occurred_at"`
}

func validActorRef(value string) bool {
	if !stableActorRef.MatchString(value) {
		return false
	}
	lower := strings.ToLower(value)
	for _, sensitive := range []string{"password", "secret", "session", "token", "credential"} {
		if strings.Contains(lower, sensitive) {
			return false
		}
	}
	return true
}

func validOperationActorID(value string) bool {
	if !stableKeyPart.MatchString(value) {
		return false
	}
	lower := strings.ToLower(value)
	for _, sensitive := range []string{"password", "secret", "session", "token", "credential"} {
		if strings.Contains(lower, sensitive) {
			return false
		}
	}
	return true
}

func newLoginBusinessID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}
