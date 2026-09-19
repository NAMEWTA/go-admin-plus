package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/NAMEWTA/go-admin-plus/backend/internal/platform/database"
)

type ParameterKind string

const (
	ParameterString  ParameterKind = "string"
	ParameterInteger ParameterKind = "integer"
	ParameterBoolean ParameterKind = "boolean"
)

type ParameterField struct {
	Name, Label      string
	Kind             ParameterKind
	Required         bool
	Minimum, Maximum *int64
	AllowedValues    []string
}
type TaskType struct {
	Key, Label string
	Fields     []ParameterField
}

// TaskHandler 在独立的短事务中运行，只修改所属表或写入 outbox，禁止直接外部 I/O。
// ExecutionID(ctx) 在崩溃恢复中保持不变，外部系统用它去重；返回业务错误不会自动重试。
type TaskHandler[P any] func(context.Context, database.Tx, P) error

type Registration struct{ task registeredTask }
type registeredTask struct {
	descriptor TaskType
	normalize  func([]byte) ([]byte, error)
	run        func(context.Context, database.Tx, []byte) error
}
type Registry struct {
	tasks       map[string]registeredTask
	descriptors []TaskType
}

func NewTaskRegistration[P any](key, label string, fields []ParameterField, handler TaskHandler[P]) (Registration, error) {
	typeOf := reflect.TypeFor[P]()
	if !taskKeyPattern.MatchString(key) || !validLabel(label, 100) || typeOf.Kind() != reflect.Struct || handler == nil {
		return Registration{}, ErrValidation
	}
	normalizedFields, ok := normalizeFields(fields, typeOf)
	if !ok {
		return Registration{}, ErrValidation
	}
	task := registeredTask{descriptor: TaskType{Key: key, Label: strings.TrimSpace(label), Fields: normalizedFields}}
	task.normalize = func(raw []byte) ([]byte, error) {
		if !validRawParameterObject(raw, normalizedFields) {
			return nil, ErrValidation
		}
		var value P
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&value); err != nil {
			return nil, ErrValidation
		}
		if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
			return nil, ErrValidation
		}
		canonical, err := json.Marshal(value)
		if err != nil || len(canonical) > maximumParametersBytes || !validateParameterObject(canonical, normalizedFields) {
			return nil, ErrValidation
		}
		return canonical, nil
	}
	task.run = func(ctx context.Context, tx database.Tx, raw []byte) error {
		canonical, err := task.normalize(raw)
		if err != nil {
			return err
		}
		var value P
		if json.Unmarshal(canonical, &value) != nil {
			return ErrValidation
		}
		return handler(ctx, tx, value)
	}
	return Registration{task: task}, nil
}

func validRawParameterObject(raw []byte, fields []ParameterField) bool {
	if len(raw) == 0 || len(raw) > maximumParametersBytes {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return false
	}
	seen := make(map[string]struct{}, len(fields))
	for decoder.More() {
		name, err := decoder.Token()
		key, ok := name.(string)
		if err != nil || !ok {
			return false
		}
		if _, duplicate := seen[key]; duplicate {
			return false
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return false
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') || len(seen) != len(fields) {
		return false
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return false
	}
	for _, field := range fields {
		if _, exists := seen[field.Name]; !exists {
			return false
		}
	}
	return true
}

func NewRegistry(registrations ...Registration) (*Registry, error) {
	if len(registrations) == 0 || len(registrations) > 128 {
		return nil, ErrValidation
	}
	registry := &Registry{tasks: make(map[string]registeredTask, len(registrations))}
	for _, registration := range registrations {
		key := registration.task.descriptor.Key
		if key == "" || registration.task.run == nil {
			return nil, ErrValidation
		}
		if _, exists := registry.tasks[key]; exists {
			return nil, ErrConflict
		}
		registry.tasks[key] = registration.task
		registry.descriptors = append(registry.descriptors, cloneTaskType(registration.task.descriptor))
	}
	sort.Slice(registry.descriptors, func(i, j int) bool { return registry.descriptors[i].Key < registry.descriptors[j].Key })
	return registry, nil
}

func (registry *Registry) TaskTypes() []TaskType {
	if registry == nil {
		return nil
	}
	result := make([]TaskType, len(registry.descriptors))
	for index := range result {
		result[index] = cloneTaskType(registry.descriptors[index])
	}
	return result
}
func (registry *Registry) normalize(key string, raw []byte) ([]byte, error) {
	if registry == nil {
		return nil, ErrValidation
	}
	task, ok := registry.tasks[key]
	if !ok {
		return nil, ErrValidation
	}
	return task.normalize(raw)
}
func (registry *Registry) task(key string) (registeredTask, bool) {
	if registry == nil {
		return registeredTask{}, false
	}
	value, ok := registry.tasks[key]
	return value, ok
}

func decodeParameterMap(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var result map[string]any
	if err := decoder.Decode(&result); err != nil || result == nil {
		return nil, ErrInternal
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, ErrInternal
	}
	return result, nil
}

func cloneTaskType(value TaskType) TaskType {
	value.Fields = append([]ParameterField(nil), value.Fields...)
	for i := range value.Fields {
		value.Fields[i].AllowedValues = append([]string(nil), value.Fields[i].AllowedValues...)
	}
	return value
}

func normalizeFields(fields []ParameterField, value reflect.Type) ([]ParameterField, bool) {
	if len(fields) > 32 || len(fields) != value.NumField() {
		return nil, false
	}
	byName := make(map[string]ParameterField, len(fields))
	for _, field := range fields {
		field.Name = strings.TrimSpace(field.Name)
		field.Label = strings.TrimSpace(field.Label)
		if !validParameterName(field.Name) || !validLabel(field.Label, 80) || sensitiveName(field.Name) {
			return nil, false
		}
		if _, exists := byName[field.Name]; exists {
			return nil, false
		}
		field.AllowedValues = append([]string(nil), field.AllowedValues...)
		for index := range field.AllowedValues {
			field.AllowedValues[index] = strings.TrimSpace(field.AllowedValues[index])
		}
		sort.Strings(field.AllowedValues)
		for i, v := range field.AllowedValues {
			if !validLabel(v, 64) || i > 0 && field.AllowedValues[i-1] == v {
				return nil, false
			}
		}
		if field.Kind != ParameterString && len(field.AllowedValues) > 0 || field.Kind != ParameterInteger && (field.Minimum != nil || field.Maximum != nil) || field.Minimum != nil && (*field.Minimum < minimumSafeInteger || *field.Minimum > maximumSafeInteger) || field.Maximum != nil && (*field.Maximum < minimumSafeInteger || *field.Maximum > maximumSafeInteger) || field.Minimum != nil && field.Maximum != nil && *field.Minimum > *field.Maximum {
			return nil, false
		}
		byName[field.Name] = field
	}
	result := make([]ParameterField, 0, len(fields))
	for index := 0; index < value.NumField(); index++ {
		goField := value.Field(index)
		name, _, _ := strings.Cut(goField.Tag.Get("json"), ",")
		field, ok := byName[name]
		if !ok || name == "" || name == "-" || !field.Required {
			return nil, false
		}
		switch field.Kind {
		case ParameterString:
			ok = goField.Type.Kind() == reflect.String
		case ParameterInteger:
			ok = goField.Type.Kind() >= reflect.Int && goField.Type.Kind() <= reflect.Int64
		case ParameterBoolean:
			ok = goField.Type.Kind() == reflect.Bool
		default:
			ok = false
		}
		if !ok {
			return nil, false
		}
		result = append(result, field)
	}
	return result, true
}

func validateParameterObject(raw []byte, fields []ParameterField) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var object map[string]any
	if decoder.Decode(&object) != nil || len(object) != len(fields) {
		return false
	}
	for _, field := range fields {
		value, ok := object[field.Name]
		if !ok {
			return false
		}
		switch field.Kind {
		case ParameterString:
			text, ok := value.(string)
			if !ok || !utf8.ValidString(text) || utf8.RuneCountInString(text) > 256 || len(field.AllowedValues) > 0 && !contains(field.AllowedValues, text) {
				return false
			}
		case ParameterInteger:
			number, ok := value.(json.Number)
			if !ok {
				return false
			}
			integer, err := number.Int64()
			if err != nil || integer < minimumSafeInteger || integer > maximumSafeInteger || field.Minimum != nil && integer < *field.Minimum || field.Maximum != nil && integer > *field.Maximum {
				return false
			}
		case ParameterBoolean:
			if _, ok := value.(bool); !ok {
				return false
			}
		default:
			return false
		}
	}
	return true
}
func validParameterName(value string) bool {
	if value == "" || len(value) > 64 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
func validLabel(value string, max int) bool {
	value = strings.TrimSpace(value)
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) == 0 || utf8.RuneCountInString(value) > max {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
func sensitiveName(value string) bool {
	lower := strings.ToLower(value)
	for _, token := range []string{"password", "secret", "session", "token", "credential"} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}
func contains(values []string, target string) bool {
	index := sort.SearchStrings(values, target)
	return index < len(values) && values[index] == target
}
