package backend

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"mime"
	"net/http"
	"reflect"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humafiber"
	"github.com/gofiber/fiber/v3"
)

type httpErrorBody struct {
	Error string `json:"error"`
}

type httpOperationError struct {
	httpErrorBody
	status  int
	headers http.Header
}

func (e *httpOperationError) Error() string           { return e.httpErrorBody.Error }
func (e *httpOperationError) GetStatus() int          { return e.status }
func (e *httpOperationError) GetHeaders() http.Header { return e.headers }

func httpHumaError(err error) error {
	status, code := 500, "internal_error"
	headers := http.Header{}
	var api *APIError
	if errors.As(err, &api) && api.Status >= 400 && api.Status < 600 {
		status, code = api.Status, api.Code
		if api.RetryAfter > 0 {
			headers.Set("Retry-After", strconv.FormatInt(int64(math.Ceil(api.RetryAfter.Seconds())), 10))
		}
	}
	if status == 500 {
		slog.Error("http_request_failed")
	}
	return &httpOperationError{httpErrorBody: httpErrorBody{Error: code}, status: status, headers: headers}
}

func newHTTPAPI(app *fiber.App, s *Service) huma.API {
	config := huma.DefaultConfig("Barone Sanitation API", "1.0.0")
	config.DocsPath, config.SchemasPath = "", ""
	config.CreateHooks = nil
	config.Info.Description = "Same-origin HTTPS API. Every mutation requires the signed __Host-csrf cookie, matching X-CSRF-Token and the exact PUBLIC_ORIGIN. Obtain CSRF with GET /csrf. Sessions and login challenges use Secure, HttpOnly, SameSite=Strict cookies. Prices are decimal strings. Validation errors are sanitized and never echo submitted credentials."
	config.Servers = []*huma.Server{{URL: "/"}}
	config.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"Session":        {Type: "apiKey", In: "cookie", Name: sessionCookie},
		"LoginChallenge": {Type: "apiKey", In: "cookie", Name: challengeCookie},
	}
	config.Transformers = []huma.Transformer{func(ctx huma.Context, status string, value any) (any, error) {
		if _, ok := value.(*huma.ErrorModel); ok {
			// Huma's raw validation details can include the entire password/OTP request.
			code := "invalid_request"
			switch status {
			case "400":
				code = "invalid_json"
			case "413":
				code = "request_too_large"
			case "415":
				code = "application_json_required"
			case "500":
				code = "internal_error"
			}
			ctx.SetHeader("Content-Type", "application/json")
			return httpErrorBody{Error: code}, nil
		}
		return value, nil
	}}
	config.OnAddOperation = append(config.OnAddOperation, func(oapi *huma.OpenAPI, op *huma.Operation) {
		schema := oapi.Components.Schemas.Schema(reflect.TypeFor[httpErrorBody](), true, "Error")
		for status := range op.Responses {
			if code, _ := strconv.Atoi(status); code >= 400 {
				op.Responses[status] = &huma.Response{
					Description: http.StatusText(code),
					Content:     map[string]*huma.MediaType{"application/json": {Schema: schema}},
				}
			}
		}
		if response := op.Responses["429"]; response != nil {
			response.Headers = map[string]*huma.Header{
				"Retry-After": {Description: "Seconds until retry is permitted.", Schema: &huma.Schema{Type: "string"}},
			}
		}
	})
	api := humafiber.New(app, config)
	api.UseMiddleware(func(ctx huma.Context, next func(huma.Context)) {
		for _, security := range ctx.Operation().Security {
			if _, protected := security["Session"]; protected {
				actor, err := s.Authenticate(ctx.Context(), humafiber.Unwrap(ctx).Cookies(sessionCookie))
				if err != nil {
					_ = httpError(humafiber.Unwrap(ctx), err)
					return
				}
				ctx = huma.WithValue(ctx, httpActorKey, actor)
			}
		}
		for _, param := range ctx.Operation().Parameters {
			if param.In == "path" {
				if _, err := httpPositiveID(ctx.Param(param.Name)); err != nil {
					_ = httpError(humafiber.Unwrap(ctx), err)
					return
				}
			}
		}
		if body := ctx.Operation().RequestBody; body != nil && body.Required && body.Content[avatarUploadType] == nil {
			mediaType, _, err := mime.ParseMediaType(ctx.Header("Content-Type"))
			if err != nil || mediaType != "application/json" {
				_ = httpError(humafiber.Unwrap(ctx), problem(415, "application_json_required"))
				return
			}
		}
		switch ctx.Operation().OperationID {
		case "verifyEmail", "finishLogin", "resetPassword":
			if err := s.RateLimit(ctx.Context(), httpIP(ctx.Context()), "verify"); err != nil {
				_ = httpError(humafiber.Unwrap(ctx), err)
				return
			}
		}
		next(ctx)
	})
	return api
}

func httpRegister[I, O any](api huma.API, op huma.Operation, protected bool, fn func(context.Context, *I) (*O, error)) {
	if op.MaxBodyBytes == 0 {
		op.MaxBodyBytes = httpBodyLimit
	}
	op.Errors = []int{400, 401, 403, 404, 409, 413, 415, 422, 429, 500, 503}
	if protected {
		op.Security = []map[string][]string{{"Session": {}}}
	}
	if httpUnsafe(op.Method) {
		for _, param := range []huma.Param{
			{Name: "Origin", In: "header", Required: true, Schema: &huma.Schema{Type: "string"}, Description: "Exact PUBLIC_ORIGIN; browser supplied."},
			{Name: "X-CSRF-Token", In: "header", Required: true, Schema: &huma.Schema{Type: "string"}, Description: "Signed token from GET /csrf, matching the cookie."},
			{Name: csrfCookie, In: "cookie", Required: true, Schema: &huma.Schema{Type: "string"}, Description: "Browser-managed signed CSRF cookie."},
		} {
			p := param
			op.Parameters = append(op.Parameters, &p)
		}
	}
	huma.Register(api, op, func(ctx context.Context, in *I) (*O, error) {
		out, err := fn(ctx, in)
		if err != nil {
			return nil, httpHumaError(err)
		}
		return out, nil
	})
}

func httpCSRFToken(_ context.Context, s *Service, existing string) (string, []http.Cookie, error) {
	if len(s.Config.HMACSecret) < 32 {
		return "", nil, problem(503, "authentication_unavailable")
	}
	if httpValidCSRF(s.Config.HMACSecret, existing) {
		return existing, nil, nil
	}
	random, err := newToken()
	if err != nil {
		return "", nil, err
	}
	token := random + "." + httpCSRFSignature(s.Config.HMACSecret, random)
	return token, []http.Cookie{httpCookie(csrfCookie, token, 24*time.Hour, false)}, nil
}
