package scheduler

import (
	"context"
	"errors"

	"github.com/google/uuid"

	transport "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/scheduler/transport"
)

func (s *HTTPServer) ListSchedulerExecutions(ctx context.Context, request transport.ListSchedulerExecutionsRequestObject) (transport.ListSchedulerExecutionsResponseObject, error) {
	query := ExecutionQuery{Page: 1, PageSize: 20}
	if request.Params.DefinitionId != nil {
		query.DefinitionID = request.Params.DefinitionId.String()
	}
	if request.Params.Status != nil {
		query.Status = ExecutionStatus(*request.Params.Status)
	}
	if request.Params.Page != nil {
		query.Page = *request.Params.Page
	}
	if request.Params.PageSize != nil {
		query.PageSize = *request.Params.PageSize
	}
	value, err := s.service.ListExecutions(ctx, requestHTTP(ctx).actorID, query)
	if err != nil {
		if errors.Is(err, ErrValidation) {
			return transport.ListSchedulerExecutions400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(ctx)}, nil
		}
		if errors.Is(err, ErrDenied) {
			return transport.ListSchedulerExecutions403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(ctx)}, nil
		}
		return transport.ListSchedulerExecutions500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(ctx)}, nil
	}
	rows := make([]transport.Execution, 0, len(value.Rows))
	for _, item := range value.Rows {
		id, idErr := uuid.Parse(item.ID)
		definitionID, definitionErr := uuid.Parse(item.DefinitionID)
		if idErr != nil || definitionErr != nil {
			return transport.ListSchedulerExecutions500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(ctx)}, nil
		}
		var code *string
		if item.ErrorCode != "" {
			converted := item.ErrorCode
			code = &converted
		}
		rows = append(rows, transport.Execution{Id: id, DefinitionId: definitionID, DefinitionRevision: int(item.DefinitionRevision), TaskType: item.TaskType, ScheduledFor: item.ScheduledFor, StartedAt: item.StartedAt, FinishedAt: item.FinishedAt, Status: transport.ExecutionStatus(item.Status), ErrorCode: code, ExecutorOwner: item.ExecutorOwner})
	}
	csrf, cookie := responseHeaders(ctx)
	return transport.ListSchedulerExecutions200JSONResponse{Body: transport.ExecutionPage{Rows: rows, Total: value.Total}, Headers: transport.ListSchedulerExecutions200ResponseHeaders{XCSRFToken: csrf, SetCookie: cookie}}, nil
}
