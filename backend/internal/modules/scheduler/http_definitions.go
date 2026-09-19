package scheduler

import (
	"context"
	"errors"
	"github.com/google/uuid"

	transport "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/scheduler/transport"
)

func (s *HTTPServer) ListSchedulerTaskTypes(ctx context.Context, _ transport.ListSchedulerTaskTypesRequestObject) (transport.ListSchedulerTaskTypesResponseObject, error) {
	values, err := s.service.TaskTypes(ctx, requestHTTP(ctx).actorID)
	if err != nil {
		if errors.Is(err, ErrDenied) {
			return transport.ListSchedulerTaskTypes403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(ctx)}, nil
		}
		return transport.ListSchedulerTaskTypes500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(ctx)}, nil
	}
	body := make([]transport.TaskType, 0, len(values))
	for _, value := range values {
		body = append(body, transportTaskType(value))
	}
	csrf, cookie := responseHeaders(ctx)
	return transport.ListSchedulerTaskTypes200JSONResponse{Body: body, Headers: transport.ListSchedulerTaskTypes200ResponseHeaders{XCSRFToken: csrf, SetCookie: cookie}}, nil
}

func (s *HTTPServer) ListSchedulerDefinitions(ctx context.Context, request transport.ListSchedulerDefinitionsRequestObject) (transport.ListSchedulerDefinitionsResponseObject, error) {
	query := DefinitionQuery{Page: 1, PageSize: 20}
	if request.Params.Search != nil {
		query.Search = *request.Params.Search
	}
	if request.Params.Page != nil {
		query.Page = *request.Params.Page
	}
	if request.Params.PageSize != nil {
		query.PageSize = *request.Params.PageSize
	}
	value, err := s.service.ListDefinitions(ctx, requestHTTP(ctx).actorID, query)
	if err != nil {
		if errors.Is(err, ErrValidation) {
			return transport.ListSchedulerDefinitions400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(ctx)}, nil
		}
		if errors.Is(err, ErrDenied) {
			return transport.ListSchedulerDefinitions403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(ctx)}, nil
		}
		return transport.ListSchedulerDefinitions500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(ctx)}, nil
	}
	rows := make([]transport.Definition, 0, len(value.Rows))
	for _, item := range value.Rows {
		converted, err := transportDefinition(item)
		if err != nil {
			return transport.ListSchedulerDefinitions500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(ctx)}, nil
		}
		rows = append(rows, converted)
	}
	csrf, cookie := responseHeaders(ctx)
	return transport.ListSchedulerDefinitions200JSONResponse{Body: transport.DefinitionPage{Rows: rows, Total: value.Total}, Headers: transport.ListSchedulerDefinitions200ResponseHeaders{XCSRFToken: csrf, SetCookie: cookie}}, nil
}

func (s *HTTPServer) CreateSchedulerDefinition(ctx context.Context, request transport.CreateSchedulerDefinitionRequestObject) (transport.CreateSchedulerDefinitionResponseObject, error) {
	if request.Body == nil {
		return transport.CreateSchedulerDefinition400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(ctx)}, nil
	}
	input, err := input(request.Body.Name, request.Body.TaskType, request.Body.Schedule, request.Body.Parameters)
	if err != nil {
		return transport.CreateSchedulerDefinition400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(ctx)}, nil
	}
	value, err := s.service.CreateDefinition(ctx, requestHTTP(ctx).actorID, input)
	if err != nil {
		switch {
		case errors.Is(err, ErrValidation):
			return transport.CreateSchedulerDefinition400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(ctx)}, nil
		case errors.Is(err, ErrDenied):
			return transport.CreateSchedulerDefinition403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(ctx)}, nil
		case errors.Is(err, ErrConflict):
			return transport.CreateSchedulerDefinition409ApplicationProblemPlusJSONResponse{ConflictProblemApplicationProblemPlusJSONResponse: conflictProblem(ctx)}, nil
		default:
			return transport.CreateSchedulerDefinition500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(ctx)}, nil
		}
	}
	body, err := transportDefinition(value)
	if err != nil {
		return transport.CreateSchedulerDefinition500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(ctx)}, nil
	}
	csrf, cookie := responseHeaders(ctx)
	return transport.CreateSchedulerDefinition201JSONResponse{Body: body, Headers: transport.CreateSchedulerDefinition201ResponseHeaders{XCSRFToken: csrf, SetCookie: cookie}}, nil
}

func (s *HTTPServer) UpdateSchedulerDefinition(ctx context.Context, request transport.UpdateSchedulerDefinitionRequestObject) (transport.UpdateSchedulerDefinitionResponseObject, error) {
	if request.Body == nil {
		return transport.UpdateSchedulerDefinition400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(ctx)}, nil
	}
	input, err := input(request.Body.Name, request.Body.TaskType, request.Body.Schedule, request.Body.Parameters)
	if err != nil {
		return transport.UpdateSchedulerDefinition400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(ctx)}, nil
	}
	value, err := s.service.UpdateDefinition(ctx, requestHTTP(ctx).actorID, request.DefinitionId.String(), int64(request.Body.Revision), input)
	if err != nil {
		switch {
		case errors.Is(err, ErrValidation):
			return transport.UpdateSchedulerDefinition400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(ctx)}, nil
		case errors.Is(err, ErrDenied):
			return transport.UpdateSchedulerDefinition403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(ctx)}, nil
		case errors.Is(err, ErrNotFound):
			return transport.UpdateSchedulerDefinition404ApplicationProblemPlusJSONResponse{NotFoundProblemApplicationProblemPlusJSONResponse: notFoundProblem(ctx)}, nil
		case errors.Is(err, ErrConflict):
			return transport.UpdateSchedulerDefinition409ApplicationProblemPlusJSONResponse{ConflictProblemApplicationProblemPlusJSONResponse: conflictProblem(ctx)}, nil
		default:
			return transport.UpdateSchedulerDefinition500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(ctx)}, nil
		}
	}
	body, err := transportDefinition(value)
	if err != nil {
		return transport.UpdateSchedulerDefinition500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(ctx)}, nil
	}
	csrf, cookie := responseHeaders(ctx)
	return transport.UpdateSchedulerDefinition200JSONResponse{Body: body, Headers: transport.UpdateSchedulerDefinition200ResponseHeaders{XCSRFToken: csrf, SetCookie: cookie}}, nil
}

func (s *HTTPServer) EnableSchedulerDefinition(ctx context.Context, request transport.EnableSchedulerDefinitionRequestObject) (transport.EnableSchedulerDefinitionResponseObject, error) {
	if request.Body == nil {
		return transport.EnableSchedulerDefinition400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(ctx)}, nil
	}
	value, err := s.service.EnableDefinition(ctx, requestHTTP(ctx).actorID, request.DefinitionId.String(), int64(request.Body.Revision))
	if err != nil {
		switch {
		case errors.Is(err, ErrValidation):
			return transport.EnableSchedulerDefinition400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(ctx)}, nil
		case errors.Is(err, ErrDenied):
			return transport.EnableSchedulerDefinition403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(ctx)}, nil
		case errors.Is(err, ErrNotFound):
			return transport.EnableSchedulerDefinition404ApplicationProblemPlusJSONResponse{NotFoundProblemApplicationProblemPlusJSONResponse: notFoundProblem(ctx)}, nil
		case errors.Is(err, ErrConflict):
			return transport.EnableSchedulerDefinition409ApplicationProblemPlusJSONResponse{ConflictProblemApplicationProblemPlusJSONResponse: conflictProblem(ctx)}, nil
		default:
			return transport.EnableSchedulerDefinition500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(ctx)}, nil
		}
	}
	body, err := transportDefinition(value)
	if err != nil {
		return transport.EnableSchedulerDefinition500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(ctx)}, nil
	}
	csrf, cookie := responseHeaders(ctx)
	return transport.EnableSchedulerDefinition200JSONResponse{Body: body, Headers: transport.EnableSchedulerDefinition200ResponseHeaders{XCSRFToken: csrf, SetCookie: cookie}}, nil
}

func (s *HTTPServer) StopSchedulerDefinition(ctx context.Context, request transport.StopSchedulerDefinitionRequestObject) (transport.StopSchedulerDefinitionResponseObject, error) {
	if request.Body == nil {
		return transport.StopSchedulerDefinition400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(ctx)}, nil
	}
	value, err := s.service.StopDefinition(ctx, requestHTTP(ctx).actorID, request.DefinitionId.String(), int64(request.Body.Revision))
	if err != nil {
		switch {
		case errors.Is(err, ErrValidation):
			return transport.StopSchedulerDefinition400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(ctx)}, nil
		case errors.Is(err, ErrDenied):
			return transport.StopSchedulerDefinition403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(ctx)}, nil
		case errors.Is(err, ErrNotFound):
			return transport.StopSchedulerDefinition404ApplicationProblemPlusJSONResponse{NotFoundProblemApplicationProblemPlusJSONResponse: notFoundProblem(ctx)}, nil
		case errors.Is(err, ErrConflict):
			return transport.StopSchedulerDefinition409ApplicationProblemPlusJSONResponse{ConflictProblemApplicationProblemPlusJSONResponse: conflictProblem(ctx)}, nil
		default:
			return transport.StopSchedulerDefinition500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(ctx)}, nil
		}
	}
	body, err := transportDefinition(value)
	if err != nil {
		return transport.StopSchedulerDefinition500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(ctx)}, nil
	}
	csrf, cookie := responseHeaders(ctx)
	return transport.StopSchedulerDefinition200JSONResponse{Body: body, Headers: transport.StopSchedulerDefinition200ResponseHeaders{XCSRFToken: csrf, SetCookie: cookie}}, nil
}

func (s *HTTPServer) DeleteSchedulerDefinition(ctx context.Context, request transport.DeleteSchedulerDefinitionRequestObject) (transport.DeleteSchedulerDefinitionResponseObject, error) {
	err := s.service.DeleteDefinition(ctx, requestHTTP(ctx).actorID, request.DefinitionId.String(), int64(request.Params.Revision))
	if err != nil {
		switch {
		case errors.Is(err, ErrValidation):
			return transport.DeleteSchedulerDefinition400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(ctx)}, nil
		case errors.Is(err, ErrDenied):
			return transport.DeleteSchedulerDefinition403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(ctx)}, nil
		case errors.Is(err, ErrNotFound):
			return transport.DeleteSchedulerDefinition404ApplicationProblemPlusJSONResponse{NotFoundProblemApplicationProblemPlusJSONResponse: notFoundProblem(ctx)}, nil
		case errors.Is(err, ErrConflict):
			return transport.DeleteSchedulerDefinition409ApplicationProblemPlusJSONResponse{ConflictProblemApplicationProblemPlusJSONResponse: conflictProblem(ctx)}, nil
		default:
			return transport.DeleteSchedulerDefinition500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(ctx)}, nil
		}
	}
	csrf, cookie := responseHeaders(ctx)
	return transport.DeleteSchedulerDefinition204Response{Headers: transport.DeleteSchedulerDefinition204ResponseHeaders{XCSRFToken: csrf, SetCookie: cookie}}, nil
}

func transportTaskType(value TaskType) transport.TaskType {
	fields := make([]transport.ParameterField, 0, len(value.Fields))
	for _, value := range value.Fields {
		field := transport.ParameterField{Name: value.Name, Label: value.Label, Kind: transport.ParameterKind(value.Kind), Required: value.Required}
		if value.Minimum != nil {
			converted := int(*value.Minimum)
			field.Minimum = &converted
		}
		if value.Maximum != nil {
			converted := int(*value.Maximum)
			field.Maximum = &converted
		}
		if len(value.AllowedValues) > 0 {
			converted := append([]string(nil), value.AllowedValues...)
			field.AllowedValues = &converted
		}
		fields = append(fields, field)
	}
	return transport.TaskType{Key: value.Key, Label: value.Label, Fields: fields}
}

func (s *HTTPServer) RunSchedulerDefinition(ctx context.Context, request transport.RunSchedulerDefinitionRequestObject) (transport.RunSchedulerDefinitionResponseObject, error) {
	if request.Body == nil {
		return transport.RunSchedulerDefinition400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(ctx)}, nil
	}
	value, err := s.service.RunDefinition(ctx, requestHTTP(ctx).actorID, request.DefinitionId.String(), int64(request.Body.Revision))
	if err != nil {
		switch {
		case errors.Is(err, ErrValidation):
			return transport.RunSchedulerDefinition400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(ctx)}, nil
		case errors.Is(err, ErrDenied):
			return transport.RunSchedulerDefinition403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(ctx)}, nil
		case errors.Is(err, ErrNotFound):
			return transport.RunSchedulerDefinition404ApplicationProblemPlusJSONResponse{NotFoundProblemApplicationProblemPlusJSONResponse: notFoundProblem(ctx)}, nil
		case errors.Is(err, ErrConflict):
			return transport.RunSchedulerDefinition409ApplicationProblemPlusJSONResponse{ConflictProblemApplicationProblemPlusJSONResponse: conflictProblem(ctx)}, nil
		default:
			return transport.RunSchedulerDefinition500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(ctx)}, nil
		}
	}
	csrf, cookie := responseHeaders(ctx)
	id, err := uuid.Parse(value)
	if err != nil {
		return transport.RunSchedulerDefinition500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(ctx)}, nil
	}
	return transport.RunSchedulerDefinition202JSONResponse{Body: struct {
		ExecutionId uuid.UUID `json:"executionId"`
	}{ExecutionId: id}, Headers: transport.RunSchedulerDefinition202ResponseHeaders{XCSRFToken: csrf, SetCookie: cookie}}, nil
}
