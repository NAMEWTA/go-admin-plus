package administration

import (
	"context"
	"errors"

	transport "github.com/NAMEWTA/go-admin-plus/backend/internal/modules/iam/administration/transport"
)

func (s *HTTPServer) ListIamUsers(ctx context.Context, request transport.ListIamUsersRequestObject) (transport.ListIamUsersResponseObject, error) {
	search, page, size := "", 1, 20
	if request.Params.Search != nil {
		search = *request.Params.Search
	}
	if request.Params.Page != nil {
		page = *request.Params.Page
	}
	if request.Params.PageSize != nil {
		size = *request.Params.PageSize
	}
	value, err := s.service.ListUsers(ctx, requestHTTP(ctx).actorID, search, page, size)
	if err != nil {
		if errors.Is(err, ErrValidation) {
			return transport.ListIamUsers400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(ctx)}, nil
		}
		if errors.Is(err, ErrDenied) {
			return transport.ListIamUsers403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(ctx)}, nil
		}
		return transport.ListIamUsers500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(ctx)}, nil
	}
	rows := make([]transport.User, 0, len(value.Rows))
	for _, item := range value.Rows {
		rows = append(rows, transportUser(item))
	}
	csrf, cookie := responseHeaders(ctx)
	return transport.ListIamUsers200JSONResponse{Body: transport.UserPage{Rows: rows, Total: value.Total}, Headers: transport.ListIamUsers200ResponseHeaders{XCSRFToken: csrf, SetCookie: cookie}}, nil
}

func (s *HTTPServer) CreateIamUser(ctx context.Context, request transport.CreateIamUserRequestObject) (transport.CreateIamUserResponseObject, error) {
	if request.Body == nil {
		return transport.CreateIamUser400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(ctx)}, nil
	}
	value, err := s.service.CreateUser(ctx, requestHTTP(ctx).actorID, CreateUser{Username: request.Body.Username, DisplayName: request.Body.DisplayName, Email: string(request.Body.Email), Password: request.Body.Password})
	if err != nil {
		if errors.Is(err, ErrValidation) {
			return transport.CreateIamUser400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(ctx)}, nil
		}
		if errors.Is(err, ErrDenied) {
			return transport.CreateIamUser403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(ctx)}, nil
		}
		if errors.Is(err, ErrConflict) {
			return transport.CreateIamUser409ApplicationProblemPlusJSONResponse{ConflictProblemApplicationProblemPlusJSONResponse: conflictProblem(ctx)}, nil
		}
		return transport.CreateIamUser500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(ctx)}, nil
	}
	csrf, cookie := responseHeaders(ctx)
	return transport.CreateIamUser201JSONResponse{Body: transportUser(value), Headers: transport.CreateIamUser201ResponseHeaders{XCSRFToken: csrf, SetCookie: cookie}}, nil
}

func (s *HTTPServer) GetIamUser(ctx context.Context, request transport.GetIamUserRequestObject) (transport.GetIamUserResponseObject, error) {
	value, err := s.service.GetUser(ctx, requestHTTP(ctx).actorID, request.UserId)
	if err != nil {
		if errors.Is(err, ErrDenied) {
			return transport.GetIamUser403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(ctx)}, nil
		}
		if errors.Is(err, ErrNotFound) {
			return transport.GetIamUser404ApplicationProblemPlusJSONResponse{NotFoundProblemApplicationProblemPlusJSONResponse: notFoundProblem(ctx)}, nil
		}
		return transport.GetIamUser500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(ctx)}, nil
	}
	csrf, cookie := responseHeaders(ctx)
	return transport.GetIamUser200JSONResponse{Body: transportUser(value), Headers: transport.GetIamUser200ResponseHeaders{XCSRFToken: csrf, SetCookie: cookie}}, nil
}

func (s *HTTPServer) UpdateIamUser(ctx context.Context, request transport.UpdateIamUserRequestObject) (transport.UpdateIamUserResponseObject, error) {
	if request.Body == nil {
		return transport.UpdateIamUser400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(ctx)}, nil
	}
	value, err := s.service.UpdateUser(ctx, requestHTTP(ctx).actorID, request.UserId, request.Body.DisplayName, string(request.Body.Email), request.Body.Enabled)
	if err != nil {
		if errors.Is(err, ErrValidation) {
			return transport.UpdateIamUser400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(ctx)}, nil
		}
		if errors.Is(err, ErrDenied) {
			return transport.UpdateIamUser403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(ctx)}, nil
		}
		if errors.Is(err, ErrNotFound) {
			return transport.UpdateIamUser404ApplicationProblemPlusJSONResponse{NotFoundProblemApplicationProblemPlusJSONResponse: notFoundProblem(ctx)}, nil
		}
		if errors.Is(err, ErrConflict) {
			return transport.UpdateIamUser409ApplicationProblemPlusJSONResponse{ConflictProblemApplicationProblemPlusJSONResponse: conflictProblem(ctx)}, nil
		}
		return transport.UpdateIamUser500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(ctx)}, nil
	}
	csrf, cookie := responseHeaders(ctx)
	return transport.UpdateIamUser200JSONResponse{Body: transportUser(value), Headers: transport.UpdateIamUser200ResponseHeaders{XCSRFToken: csrf, SetCookie: cookie}}, nil
}

func (s *HTTPServer) SetIamUserRoles(ctx context.Context, request transport.SetIamUserRolesRequestObject) (transport.SetIamUserRolesResponseObject, error) {
	if request.Body == nil {
		return transport.SetIamUserRoles400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(ctx)}, nil
	}
	err := s.service.SetUserRoles(ctx, requestHTTP(ctx).actorID, request.UserId, request.Body.RoleIds)
	if err != nil {
		if errors.Is(err, ErrValidation) {
			return transport.SetIamUserRoles400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(ctx)}, nil
		}
		if errors.Is(err, ErrDenied) {
			return transport.SetIamUserRoles403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(ctx)}, nil
		}
		if errors.Is(err, ErrNotFound) {
			return transport.SetIamUserRoles404ApplicationProblemPlusJSONResponse{NotFoundProblemApplicationProblemPlusJSONResponse: notFoundProblem(ctx)}, nil
		}
		if errors.Is(err, ErrConflict) {
			return transport.SetIamUserRoles409ApplicationProblemPlusJSONResponse{ConflictProblemApplicationProblemPlusJSONResponse: conflictProblem(ctx)}, nil
		}
		return transport.SetIamUserRoles500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(ctx)}, nil
	}
	csrf, cookie := responseHeaders(ctx)
	return transport.SetIamUserRoles204Response{Headers: transport.SetIamUserRoles204ResponseHeaders{XCSRFToken: csrf, SetCookie: cookie}}, nil
}

func (s *HTTPServer) ResetIamUserPassword(ctx context.Context, request transport.ResetIamUserPasswordRequestObject) (transport.ResetIamUserPasswordResponseObject, error) {
	if request.Body == nil {
		return transport.ResetIamUserPassword400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(ctx)}, nil
	}
	err := s.service.ResetPassword(ctx, requestHTTP(ctx).actorID, request.UserId, request.Body.Password)
	if err != nil {
		if errors.Is(err, ErrValidation) {
			return transport.ResetIamUserPassword400ApplicationProblemPlusJSONResponse{ValidationProblemApplicationProblemPlusJSONResponse: validationProblem(ctx)}, nil
		}
		if errors.Is(err, ErrDenied) {
			return transport.ResetIamUserPassword403ApplicationProblemPlusJSONResponse{AuthorizationProblemApplicationProblemPlusJSONResponse: authorizationProblem(ctx)}, nil
		}
		if errors.Is(err, ErrNotFound) {
			return transport.ResetIamUserPassword404ApplicationProblemPlusJSONResponse{NotFoundProblemApplicationProblemPlusJSONResponse: notFoundProblem(ctx)}, nil
		}
		if errors.Is(err, ErrConflict) {
			return transport.ResetIamUserPassword409ApplicationProblemPlusJSONResponse{ConflictProblemApplicationProblemPlusJSONResponse: conflictProblem(ctx)}, nil
		}
		return transport.ResetIamUserPassword500ApplicationProblemPlusJSONResponse{InternalProblemApplicationProblemPlusJSONResponse: internalProblem(ctx)}, nil
	}
	csrf, cookie := responseHeaders(ctx)
	return transport.ResetIamUserPassword204Response{Headers: transport.ResetIamUserPassword204ResponseHeaders{XCSRFToken: csrf, SetCookie: cookie}}, nil
}
