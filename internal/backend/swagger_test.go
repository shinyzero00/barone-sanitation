package backend

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v3"
)

func TestSwaggerRoutes(t *testing.T) {
	t.Parallel()
	app := NewApp(&Service{})
	for _, path := range []string{"/openapi.json", "/swagger", "/swagger/", "/swagger/index.html"} {
		t.Run(path, func(t *testing.T) {
			response := httpDo(t, app, httptest.NewRequest("GET", path, nil), 200)
			if response.Header.Get("X-Content-Type-Options") != "nosniff" || response.Header.Get("Cache-Control") != "no-store" {
				t.Fatal("documentation bypassed security middleware")
			}
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if path == "/openapi.json" {
				if !json.Valid(body) || !strings.HasPrefix(response.Header.Get("Content-Type"), "application/openapi+json") {
					t.Fatal("invalid OpenAPI response")
				}
				var doc map[string]any
				if err := json.Unmarshal(body, &doc); err != nil {
					t.Fatal(err)
				}
				if doc["openapi"] != "3.1.0" {
					t.Fatalf("OpenAPI version = %v, want 3.1.0", doc["openapi"])
				}
			} else if !strings.Contains(string(body), "SwaggerUIBundle") || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/html") {
				t.Fatal("invalid Swagger UI response")
			}
		})
	}
}

func TestSwaggerRouteCoverage(t *testing.T) {
	t.Parallel()
	// Construct the real route table, but never execute database-backed handlers.
	app := NewApp(&Service{})
	doc := swaggerDocument(t, app)
	want := map[string]bool{}
	ids := map[string]bool{}
	for path, value := range swaggerObject(t, doc, "paths") {
		item := value.(map[string]any)
		for method := range item {
			if !strings.Contains(" get put post delete options head patch trace ", " "+method+" ") {
				continue
			}
			want[strings.ToUpper(method)+" "+path] = true
			op := swaggerObject(t, doc, "paths", path, method)
			id, _ := op["operationId"].(string)
			if id == "" || ids[id] {
				t.Errorf("%s %s: missing/duplicate operation ID %q", method, path, id)
			}
			ids[id] = true
			parameters := map[string]map[string]any{}
			list, _ := op["parameters"].([]any)
			for _, value := range list {
				param := value.(map[string]any)
				parameters[param["in"].(string)+":"+param["name"].(string)] = param
				if param["schema"] == nil {
					t.Errorf("%s: parameter has no schema: %v", id, param)
				}
			}
			for _, segment := range strings.Split(path, "/") {
				if strings.HasPrefix(segment, "{") {
					name := strings.Trim(segment, "{}")
					if parameters["path:"+name]["required"] != true {
						t.Errorf("%s: missing required path parameter %s", id, name)
					}
				}
			}
			if httpUnsafe(strings.ToUpper(method)) {
				for _, key := range []string{"header:Origin", "header:X-CSRF-Token", "cookie:__Host-csrf"} {
					if parameters[key]["required"] != true {
						t.Errorf("%s: missing required %s", id, key)
					}
				}
			}
			responses := swaggerObject(t, doc, "paths", path, method, "responses")
			hasSuccess := false
			for status := range responses {
				if strings.HasPrefix(status, "2") {
					hasSuccess = true
					if status != "204" {
						swaggerObject(t, doc, "paths", path, method, "responses", status, "content", "application/json", "schema")
					}
				}
			}
			if !hasSuccess {
				t.Errorf("%s: no success response", id)
			}
			if op["requestBody"] != nil {
				swaggerObject(t, doc, "paths", path, method, "requestBody", "content", "application/json", "schema")
			}
			// Public endpoints are the small exception to cookie authentication.
			scheme := "Session"
			switch {
			case path == "/api/v1/auth/login/verify":
				scheme = "LoginChallenge"
			case path == "/healthz", path == "/csrf", strings.HasPrefix(path, "/api/v1/auth/") && path != "/api/v1/auth/logout",
				method == "get" && (path == "/api/v1/consultants" || path == "/api/v1/consultation-types" || path == "/api/v1/offerings" || strings.HasSuffix(path, "/slots")):
				scheme = ""
			}
			security, _ := op["security"].([]any)
			if scheme == "" {
				if len(security) != 0 {
					t.Errorf("%s: public operation has security %v", id, security)
				}
			} else if !reflect.DeepEqual(security, []any{map[string]any{scheme: []any{}}}) {
				t.Errorf("%s: security = %v, want %s", id, security, scheme)
			}
		}
	}
	if len(want) == 0 {
		t.Fatal("generated contract has no operations")
	}
	for _, route := range app.GetRoutes(true) {
		if route.Method == "HEAD" {
			continue // Fiber supplies HEAD for GET automatically.
		}
		if route.Path == "/openapi.json" || route.Path == "/openapi.yaml" || route.Path == "/openapi-3.0.json" || route.Path == "/openapi-3.0.yaml" || route.Path == "/swagger" || strings.HasPrefix(route.Path, "/swagger/") {
			continue // Documentation routes are not domain API operations.
		}
		segments := strings.Split(route.Path, "/")
		for i, segment := range segments {
			if strings.HasPrefix(segment, ":") {
				segments[i] = "{" + strings.TrimPrefix(segment, ":") + "}"
			}
		}
		key := route.Method + " " + strings.Join(segments, "/")
		if !want[key] {
			t.Errorf("undocumented backend operation: %s", key)
		}
		delete(want, key)
	}
	for key := range want {
		t.Errorf("documented operation has no backend route: %s", key)
	}
}

func swaggerDocument(t *testing.T, app *fiber.App) map[string]any {
	t.Helper()
	response := httpDo(t, app, httptest.NewRequest("GET", "/openapi.json", nil), 200)
	var doc map[string]any
	if err := json.NewDecoder(response.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if doc["openapi"] != "3.1.0" {
		t.Fatalf("OpenAPI version = %v, want 3.1.0", doc["openapi"])
	}
	return doc
}

// Resolve generated schema references while navigating the served contract.
func swaggerObject(t *testing.T, doc map[string]any, path ...string) map[string]any {
	t.Helper()
	object := doc
	for _, key := range path {
		value, ok := object[key].(map[string]any)
		if !ok {
			t.Fatalf("missing object %q in %v", key, path)
		}
		object = value
		if ref, ok := object["$ref"].(string); ok {
			if !strings.HasPrefix(ref, "#/") {
				t.Fatalf("nonlocal reference %q", ref)
			}
			parts := strings.Split(strings.TrimPrefix(ref, "#/"), "/")
			for i, part := range parts {
				parts[i] = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
			}
			object = swaggerObject(t, doc, parts...)
		}
	}
	return object
}

func TestSwaggerGeneratedSchemas(t *testing.T) {
	t.Parallel()
	doc := swaggerDocument(t, NewApp(&Service{}))
	var walk func(any)
	walk = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			if ref, ok := value["$ref"].(string); ok {
				if !strings.HasPrefix(ref, "#/") {
					t.Fatalf("nonlocal reference %q", ref)
				}
				parts := strings.Split(strings.TrimPrefix(ref, "#/"), "/")
				for i, part := range parts {
					parts[i] = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
				}
				swaggerObject(t, doc, parts...)
			}
			for _, child := range value {
				walk(child)
			}
		case []any:
			for _, child := range value {
				walk(child)
			}
		}
	}
	walk(doc)
	for scheme, cookie := range map[string]string{"Session": sessionCookie, "LoginChallenge": challengeCookie} {
		value := swaggerObject(t, doc, "components", "securitySchemes", scheme)
		if value["type"] != "apiKey" || value["in"] != "cookie" || value["name"] != cookie {
			t.Errorf("invalid cookie security scheme %s: %v", scheme, value)
		}
	}
	for _, path := range []string{"/api/v1/offerings/{id}/slots", "/api/v1/bookings"} {
		op := swaggerObject(t, doc, "paths", path, "get")
		parameters, _ := op["parameters"].([]any)
		for _, name := range []string{"from", "to"} {
			found := false
			for _, value := range parameters {
				param := value.(map[string]any)
				if param["name"] == name && param["in"] == "query" {
					found = param["required"] == true && swaggerObject(t, param, "schema")["format"] == "date-time"
				}
			}
			if !found {
				t.Errorf("%s: missing required date-time query parameter %s", path, name)
			}
		}
	}
	for _, path := range []string{"/api/v1/auth/login", "/api/v1/auth/login/verify", "/api/v1/auth/logout"} {
		swaggerObject(t, doc, "paths", path, "post", "responses", "204", "headers", "Set-Cookie", "schema")
	}
	booking := swaggerObject(t, doc, "paths", "/api/v1/bookings/{id}", "get", "responses", "200", "content", "application/json", "schema")
	if swaggerObject(t, booking, "properties", "starts_at")["format"] != "date-time" {
		t.Error("Booking.starts_at must be a date-time")
	}
	if swaggerObject(t, booking, "properties", "price")["type"] != "string" {
		t.Error("Booking.price must be a decimal string")
	}
	profile := swaggerObject(t, doc, "paths", "/api/v1/me", "patch", "requestBody", "content", "application/json", "schema")
	if required, _ := profile["required"].([]any); len(required) != 0 {
		t.Errorf("ProfileUpdate fields must be optional: %v", required)
	}
	code := swaggerObject(t, doc, "paths", "/api/v1/auth/verify-email", "post", "requestBody", "content", "application/json", "schema", "properties")
	for _, name := range []string{"purpose", "user_id"} {
		if code[name] != nil {
			t.Errorf("CodeInput exposes internal field %s", name)
		}
	}
	typ := swaggerObject(t, doc, "paths", "/api/v1/admin/consultation-types", "post", "requestBody", "content", "application/json", "schema", "properties")
	if duration := swaggerObject(t, typ, "duration_minutes"); duration["minimum"] != float64(1) || duration["maximum"] != float64(1440) {
		t.Error("consultation duration constraints missing from typed contract")
	}
	if swaggerObject(t, typ, "price")["pattern"] != domainPrice.String() {
		t.Error("typed price constraint differs from service validation")
	}
	for path, field := range map[string]string{"/api/v1/auth/codes": "purpose", "/api/v1/bookings/{id}/status": "status"} {
		property := swaggerObject(t, doc, "paths", path, "post", "requestBody", "content", "application/json", "schema", "properties", field)
		if len(property["enum"].([]any)) == 0 {
			t.Errorf("%s: no generated enum for %s", path, field)
		}
	}
	if swaggerObject(t, doc, "paths", "/api/v1/auth/logout", "post", "requestBody")["required"] == true {
		t.Error("empty logout body must be optional")
	}
	errorBody := swaggerObject(t, doc, "paths", "/api/v1/auth/register", "post", "responses", "422", "content", "application/json", "schema", "properties")
	if len(errorBody) != 1 || swaggerObject(t, errorBody, "error")["type"] != "string" {
		t.Error("generated validation error schema does not match sanitized envelope")
	}
}

func TestSwaggerTypedOperationSourceOfTruth(t *testing.T) {
	t.Parallel()
	type contractInput struct {
		Body struct {
			Label string `json:"label" minLength:"3"`
		}
	}
	type contractOutput struct {
		Body struct {
			Accepted bool `json:"accepted"`
		}
	}
	app := fiber.New()
	api := newHTTPAPI(app, &Service{})
	httpRegister(api, huma.Operation{Method: "POST", Path: "/contract-probe", OperationID: "contractProbe", DefaultStatus: 201}, false,
		func(context.Context, *contractInput) (*contractOutput, error) {
			return &contractOutput{}, nil
		})
	doc := swaggerDocument(t, app)
	if len(swaggerObject(t, doc, "paths")) != 1 {
		t.Fatal("isolated API should document only the locally registered operation")
	}
	op := swaggerObject(t, doc, "paths", "/contract-probe", "post")
	if op["operationId"] != "contractProbe" {
		t.Fatal("typed operation missing from served contract")
	}
	input := swaggerObject(t, doc, "paths", "/contract-probe", "post", "requestBody", "content", "application/json", "schema")
	label := swaggerObject(t, input, "properties", "label")
	if label["type"] != "string" || label["minLength"] != float64(3) || !reflect.DeepEqual(input["required"], []any{"label"}) {
		t.Fatalf("typed input schema not generated: %v", input)
	}
	accepted := swaggerObject(t, doc, "paths", "/contract-probe", "post", "responses", "201", "content", "application/json", "schema", "properties", "accepted")
	if accepted["type"] != "boolean" {
		t.Fatalf("typed output schema not generated: %v", accepted)
	}
}
