package outbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
)

type PayloadKind uint8

const (
	PayloadString PayloadKind = iota + 1
	PayloadNumber
	PayloadBoolean
)

type PayloadFieldSchema struct {
	Name     string
	Kind     PayloadKind
	Required bool
	// AllowedStrings is the closed set of non-sensitive domain labels accepted by PayloadString.
	AllowedStrings []string
}

type BusinessKeySchema struct {
	Prefix   string
	MinParts int
	MaxParts int
}

type TopicSchema struct {
	Topic       string
	Payload     []PayloadFieldSchema
	BusinessKey BusinessKeySchema
}

type topicValidator struct {
	payload     map[string]payloadFieldValidator
	businessKey BusinessKeySchema
}

type payloadFieldValidator struct {
	kind           PayloadKind
	required       bool
	allowedStrings map[string]struct{}
}

var (
	payloadFieldPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,63}$`)
	keyPartPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	domainLabelPattern  = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
)

func compileTopicSchemas(schemas []TopicSchema) (map[string]topicValidator, error) {
	if len(schemas) == 0 {
		return nil, errors.New("outbox topic schema is required")
	}
	compiled := make(map[string]topicValidator, len(schemas))
	for _, schema := range schemas {
		if !topicPattern.MatchString(schema.Topic) || schema.BusinessKey.Prefix == "" ||
			!keyPartPattern.MatchString(schema.BusinessKey.Prefix) ||
			schema.BusinessKey.MinParts < 1 || schema.BusinessKey.MaxParts < schema.BusinessKey.MinParts ||
			schema.BusinessKey.MaxParts > 8 || isSensitiveBusinessPart(schema.BusinessKey.Prefix) {
			return nil, errors.New("outbox topic schema is invalid")
		}
		if _, exists := compiled[schema.Topic]; exists {
			return nil, errors.New("outbox topic schema is duplicated")
		}
		fields := make(map[string]payloadFieldValidator, len(schema.Payload))
		for _, field := range schema.Payload {
			if !payloadFieldPattern.MatchString(field.Name) || !validPayloadFieldName(field.Name) ||
				field.Kind < PayloadString || field.Kind > PayloadBoolean {
				return nil, errors.New("outbox payload schema is invalid")
			}
			if _, exists := fields[field.Name]; exists {
				return nil, errors.New("outbox payload schema is duplicated")
			}
			allowed, err := compileAllowedStrings(field)
			if err != nil {
				return nil, err
			}
			fields[field.Name] = payloadFieldValidator{
				kind: field.Kind, required: field.Required, allowedStrings: allowed,
			}
		}
		compiled[schema.Topic] = topicValidator{payload: fields, businessKey: schema.BusinessKey}
	}
	return compiled, nil
}

func compileAllowedStrings(field PayloadFieldSchema) (map[string]struct{}, error) {
	if field.Kind != PayloadString {
		if len(field.AllowedStrings) != 0 {
			return nil, errors.New("outbox non-string payload schema has string values")
		}
		return nil, nil
	}
	if len(field.AllowedStrings) == 0 || len(field.AllowedStrings) > 64 {
		return nil, errors.New("outbox string payload schema requires an explicit value set")
	}
	allowed := make(map[string]struct{}, len(field.AllowedStrings))
	for _, value := range field.AllowedStrings {
		if len(value) > 64 || !domainLabelPattern.MatchString(value) || hasSensitiveDomainToken(value) {
			return nil, errors.New("outbox string payload schema value is invalid")
		}
		if _, duplicate := allowed[value]; duplicate {
			return nil, errors.New("outbox string payload schema value is duplicated")
		}
		allowed[value] = struct{}{}
	}
	return allowed, nil
}

func hasSensitiveDomainToken(value string) bool {
	for _, token := range strings.Split(value, "-") {
		if sensitiveToken(token) {
			return true
		}
	}
	return false
}

func (validator topicValidator) normalize(payload []byte, businessKey string) ([]byte, bool) {
	parts := strings.Split(businessKey, ":")
	if len(parts) < 2 || parts[0] != validator.businessKey.Prefix {
		return nil, false
	}
	parts = parts[1:]
	if len(parts) < validator.businessKey.MinParts || len(parts) > validator.businessKey.MaxParts {
		return nil, false
	}
	for _, part := range parts {
		if !keyPartPattern.MatchString(part) || isSensitiveBusinessPart(part) {
			return nil, false
		}
	}

	if !json.Valid(payload) || !uniqueJSONMembers(payload) {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, false
	}
	for name, value := range object {
		field, allowed := validator.payload[name]
		if !allowed || !matchesPayloadKind(value, field) {
			return nil, false
		}
	}
	for name, field := range validator.payload {
		if _, exists := object[name]; field.required && !exists {
			return nil, false
		}
	}
	normalized, err := json.Marshal(object)
	return normalized, err == nil
}

func matchesPayloadKind(value any, field payloadFieldValidator) bool {
	switch field.kind {
	case PayloadString:
		text, ok := value.(string)
		_, allowed := field.allowedStrings[text]
		return ok && allowed
	case PayloadNumber:
		_, ok := value.(json.Number)
		return ok
	case PayloadBoolean:
		_, ok := value.(bool)
		return ok
	default:
		return false
	}
}

func uniqueJSONMembers(payload []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil || !scanJSONValue(decoder, token) {
		return false
	}
	_, err = decoder.Token()
	return errors.Is(err, io.EOF)
}

func scanJSONValue(decoder *json.Decoder, token any) bool {
	delimiter, structured := token.(json.Delim)
	if !structured {
		return true
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			key, ok := keyToken.(string)
			if err != nil || !ok {
				return false
			}
			if _, duplicate := seen[key]; duplicate {
				return false
			}
			seen[key] = struct{}{}
			valueToken, err := decoder.Token()
			if err != nil || !scanJSONValue(decoder, valueToken) {
				return false
			}
		}
		closing, err := decoder.Token()
		return err == nil && closing == json.Delim('}')
	case '[':
		for decoder.More() {
			valueToken, err := decoder.Token()
			if err != nil || !scanJSONValue(decoder, valueToken) {
				return false
			}
		}
		closing, err := decoder.Token()
		return err == nil && closing == json.Delim(']')
	default:
		return false
	}
}

func validPayloadFieldName(name string) bool {
	tokens := identifierTokens(name)
	for index, token := range tokens {
		if !sensitiveToken(token) {
			continue
		}
		if index+1 == len(tokens)-1 && metadataToken(tokens[index+1]) {
			continue
		}
		return false
	}
	return true
}

func identifierTokens(value string) []string {
	var builder strings.Builder
	for index, character := range value {
		if index > 0 && character >= 'A' && character <= 'Z' {
			builder.WriteByte(' ')
		}
		if character == '_' || character == '-' || character == '.' {
			builder.WriteByte(' ')
			continue
		}
		builder.WriteRune(character)
	}
	return strings.Fields(strings.ToLower(builder.String()))
}

func sensitiveToken(value string) bool {
	switch value {
	case "password", "secret", "session", "token", "credential":
		return true
	default:
		return false
	}
}

func metadataToken(value string) bool {
	switch value {
	case "count", "timeout", "ttl", "enabled", "required", "status", "type", "expires", "expiry":
		return true
	default:
		return false
	}
}

func isSensitiveBusinessPart(part string) bool {
	lower := strings.ToLower(part)
	for _, token := range []string{"password", "secret", "session", "token", "credential"} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}
