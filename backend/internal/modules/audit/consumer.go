package audit

import (
	"strings"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/outbox"
)

// TransactionalConsumers builds the only consumers allowed to mutate Audit-owned tables.
func TransactionalConsumers() (map[string]outbox.TransactionalConsumer, error) {
	consumers := make(map[string]outbox.TransactionalConsumer, len(topicDefinitions))
	for _, topic := range []string{TopicOperationCreated, TopicOperationUpdated, TopicOperationDeleted} {
		consumer, err := outbox.NewTransactionalConsumer(
			"audit",
			"audit-projector-"+strings.ReplaceAll(topic, ".", "-"),
			[]string{"audit_facts"},
			outbox.Mutation{
				Operation: outbox.OperationInsert,
				Table:     "audit_facts",
				Values: []outbox.ColumnBinding{
					{Column: "topic", Field: outbox.FieldTopic},
					{Column: "business_key", Field: outbox.FieldBusinessKey},
					{Column: "payload", Field: outbox.FieldPayload},
					{Column: "occurred_at", Field: outbox.FieldOccurredAt},
				},
				ExpectExactly: 1,
			},
		)
		if err != nil {
			return nil, err
		}
		consumers[topic] = consumer
	}
	return consumers, nil
}
