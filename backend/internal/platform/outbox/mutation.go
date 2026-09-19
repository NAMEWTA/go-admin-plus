package outbox

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

type EventField uint8

const (
	FieldEventID EventField = iota + 1
	FieldTopic
	FieldBusinessKey
	FieldPayload
	FieldOccurredAt
)

type MutationOperation uint8

const (
	OperationInsert MutationOperation = iota + 1
	OperationUpdate
	OperationDelete
)

type ColumnBinding struct {
	Column string
	Field  EventField
}

type Mutation struct {
	Operation     MutationOperation
	Table         string
	Values        []ColumnBinding
	Keys          []ColumnBinding
	ExpectExactly int64
}

type TransactionalConsumer struct {
	owner     string
	name      string
	mutations []Mutation
}

var sqlIdentifierPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

func NewTransactionalConsumer(
	owner string,
	name string,
	allowedTables []string,
	mutations ...Mutation,
) (TransactionalConsumer, error) {
	if !sqlIdentifierPattern.MatchString(owner) || forbiddenOwner(owner) || !validOwner(name) ||
		len(allowedTables) == 0 || len(allowedTables) > 32 || len(mutations) == 0 || len(mutations) > 32 {
		return TransactionalConsumer{}, errors.New("outbox transactional consumer is invalid")
	}
	allowed := make(map[string]struct{}, len(allowedTables))
	for _, table := range allowedTables {
		if !validOwnedTable(owner, table) {
			return TransactionalConsumer{}, errors.New("outbox transactional consumer table is invalid")
		}
		if _, duplicate := allowed[table]; duplicate {
			return TransactionalConsumer{}, errors.New("outbox transactional consumer table is duplicated")
		}
		allowed[table] = struct{}{}
	}
	owned := make([]Mutation, len(mutations))
	for index, mutation := range mutations {
		if _, permitted := allowed[mutation.Table]; !permitted || !validMutation(mutation) {
			return TransactionalConsumer{}, errors.New("outbox transactional mutation is invalid")
		}
		owned[index] = cloneMutation(mutation)
	}
	return TransactionalConsumer{owner: owner, name: name, mutations: owned}, nil
}

func (c TransactionalConsumer) Name() string { return c.name }

func validMutation(mutation Mutation) bool {
	if mutation.ExpectExactly < 1 || mutation.ExpectExactly > 100 || !sqlIdentifierPattern.MatchString(mutation.Table) {
		return false
	}
	if !validBindings(mutation.Values) || !validBindings(mutation.Keys) || columnsOverlap(mutation.Values, mutation.Keys) {
		return false
	}
	switch mutation.Operation {
	case OperationInsert:
		return len(mutation.Values) > 0 && len(mutation.Keys) == 0 && hasBusinessKey(mutation.Values)
	case OperationUpdate:
		return len(mutation.Values) > 0 && len(mutation.Keys) > 0 && hasBusinessKey(mutation.Keys)
	case OperationDelete:
		return len(mutation.Values) == 0 && len(mutation.Keys) > 0 && hasBusinessKey(mutation.Keys)
	default:
		return false
	}
}

func validBindings(bindings []ColumnBinding) bool {
	seen := make(map[string]struct{}, len(bindings))
	for _, binding := range bindings {
		if !sqlIdentifierPattern.MatchString(binding.Column) || reservedIdentifier(binding.Column) ||
			binding.Field < FieldEventID || binding.Field > FieldOccurredAt {
			return false
		}
		if _, duplicate := seen[binding.Column]; duplicate {
			return false
		}
		seen[binding.Column] = struct{}{}
	}
	return true
}

func columnsOverlap(left, right []ColumnBinding) bool {
	seen := make(map[string]struct{}, len(left))
	for _, binding := range left {
		seen[binding.Column] = struct{}{}
	}
	for _, binding := range right {
		if _, exists := seen[binding.Column]; exists {
			return true
		}
	}
	return false
}

func hasBusinessKey(bindings []ColumnBinding) bool {
	for _, binding := range bindings {
		if binding.Field == FieldBusinessKey {
			return true
		}
	}
	return false
}

func cloneMutation(mutation Mutation) Mutation {
	mutation.Values = append([]ColumnBinding(nil), mutation.Values...)
	mutation.Keys = append([]ColumnBinding(nil), mutation.Keys...)
	return mutation
}

func validOwnedTable(owner, table string) bool {
	return sqlIdentifierPattern.MatchString(table) && strings.HasPrefix(table, owner+"_") &&
		!forbiddenTable(table) && !reservedIdentifier(table)
}

func forbiddenOwner(owner string) bool {
	switch owner {
	case "reliable", "platform", "goose", "sqlite", "postgres", "pg", "information", "schema":
		return true
	default:
		return false
	}
}

func forbiddenTable(table string) bool {
	for _, prefix := range []string{"reliable_", "goose_", "sqlite_", "pg_", "information_schema"} {
		if strings.HasPrefix(table, prefix) {
			return true
		}
	}
	return false
}

func reservedIdentifier(identifier string) bool {
	switch identifier {
	case "all", "and", "delete", "from", "insert", "into", "null", "or", "select", "table", "update", "where":
		return true
	default:
		return false
	}
}

func (c TransactionalConsumer) apply(ctx context.Context, tx database.Tx, event Event) error {
	for _, mutation := range c.mutations {
		query, arguments := buildMutation(mutation, event)
		result, err := tx.ExecContext(ctx, query, arguments...)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil || rows != mutation.ExpectExactly {
			return ErrConsumerFailed
		}
	}
	return nil
}

func buildMutation(mutation Mutation, event Event) (string, []any) {
	values := append([]ColumnBinding(nil), mutation.Values...)
	keys := append([]ColumnBinding(nil), mutation.Keys...)
	sort.Slice(values, func(i, j int) bool { return values[i].Column < values[j].Column })
	sort.Slice(keys, func(i, j int) bool { return keys[i].Column < keys[j].Column })
	arguments := make([]any, 0, len(values)+len(keys))
	columns := make([]string, len(values))
	for index, binding := range values {
		columns[index] = binding.Column
		arguments = append(arguments, eventValue(event, binding.Field))
	}
	conditions := make([]string, len(keys))
	for index, binding := range keys {
		conditions[index] = binding.Column + " = ?"
		arguments = append(arguments, eventValue(event, binding.Field))
	}
	switch mutation.Operation {
	case OperationInsert:
		return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", mutation.Table, strings.Join(columns, ", "), placeholders(len(columns))), arguments
	case OperationUpdate:
		assignments := make([]string, len(columns))
		for index, column := range columns {
			assignments[index] = column + " = ?"
		}
		return fmt.Sprintf("UPDATE %s SET %s WHERE %s", mutation.Table, strings.Join(assignments, ", "), strings.Join(conditions, " AND ")), arguments
	case OperationDelete:
		return fmt.Sprintf("DELETE FROM %s WHERE %s", mutation.Table, strings.Join(conditions, " AND ")), arguments
	default:
		panic("validated mutation operation")
	}
}

func placeholders(count int) string {
	items := make([]string, count)
	for index := range items {
		items[index] = "?"
	}
	return strings.Join(items, ", ")
}

func eventValue(event Event, field EventField) any {
	switch field {
	case FieldEventID:
		return event.ID
	case FieldTopic:
		return event.Topic
	case FieldBusinessKey:
		return event.BusinessKey
	case FieldPayload:
		return append([]byte(nil), event.Payload...)
	case FieldOccurredAt:
		return event.OccurredAt
	default:
		panic("validated event field")
	}
}
