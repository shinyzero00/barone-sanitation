package apidocs

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
)

func document(t *testing.T) map[string]any {
	t.Helper()
	if !json.Valid(spec) {
		t.Fatal("embedded specification is invalid JSON")
	}
	var doc map[string]any
	if err := json.Unmarshal(spec, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func resolve(t *testing.T, doc map[string]any, ref string) map[string]any {
	t.Helper()
	if !strings.HasPrefix(ref, "#/") {
		t.Fatalf("nonlocal reference: %s", ref)
	}
	var value any = doc
	for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		object, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("reference traverses non-object: %s", ref)
		}
		value, ok = object[part]
		if !ok {
			t.Fatalf("unresolved reference: %s", ref)
		}
	}
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("reference target is not object: %s", ref)
	}
	return object
}

func TestSpecificationReferences(t *testing.T) {
	t.Parallel()
	doc := document(t)
	if doc["openapi"] != "3.0.3" {
		t.Fatal("expected OpenAPI 3.0.3")
	}
	var walk func(any)
	walk = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			if ref, ok := value["$ref"].(string); ok {
				resolve(t, doc, ref)
				if len(value) != 1 {
					t.Errorf("OpenAPI 3.0 reference has ignored siblings: %s", ref)
				}
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
}

func TestOperationContract(t *testing.T) {
	t.Parallel()
	doc := document(t)
	// method:success:cookie scheme; '-' means no authentication cookie.
	expected := map[string]string{
		"/healthz":                                           "get:200:-",
		"/csrf":                                              "get:200:-",
		"/api/v1/auth/csrf":                                  "get:200:-",
		"/api/v1/auth/register":                              "post:201:-",
		"/api/v1/auth/codes":                                 "post:204:-",
		"/api/v1/auth/verify-email":                          "post:204:-",
		"/api/v1/auth/login":                                 "post:204:-",
		"/api/v1/auth/login/verify":                          "post:204:LoginChallenge",
		"/api/v1/auth/reset-password":                        "post:204:-",
		"/api/v1/auth/logout":                                "post:204:Session",
		"/api/v1/me":                                         "get:200:Session patch:200:Session",
		"/api/v1/addresses":                                  "get:200:Session post:201:Session",
		"/api/v1/addresses/{id}":                             "get:200:Session put:200:Session delete:204:Session",
		"/api/v1/consultants":                                "get:200:-",
		"/api/v1/consultation-types":                         "get:200:-",
		"/api/v1/offerings":                                  "get:200:-",
		"/api/v1/admin/consultants":                          "post:201:Session",
		"/api/v1/admin/consultants/{id}":                     "patch:204:Session",
		"/api/v1/admin/consultation-types":                   "post:201:Session",
		"/api/v1/admin/consultation-types/{id}":              "put:201:Session",
		"/api/v1/admin/offerings":                            "put:200:Session",
		"/api/v1/consultants/{consultant_id}/work-intervals": "get:200:Session post:201:Session",
		"/api/v1/consultants/{consultant_id}/work-intervals/{id}": "get:200:Session put:200:Session delete:204:Session",
		"/api/v1/offerings/{id}/slots":                            "get:200:-",
		"/api/v1/bookings":                                        "get:200:Session post:201:Session",
		"/api/v1/bookings/{id}":                                   "get:200:Session",
		"/api/v1/bookings/{id}/events":                            "get:200:Session",
		"/api/v1/bookings/{id}/status":                            "post:200:Session",
	}
	paths := doc["paths"].(map[string]any)
	if len(paths) != len(expected) {
		t.Fatalf("documented paths = %d, want %d", len(paths), len(expected))
	}
	ids := map[string]bool{}
	for path, contracts := range expected {
		t.Run(path, func(t *testing.T) {
			item, ok := paths[path].(map[string]any)
			if !ok {
				t.Fatal("missing path")
			}
			operations := strings.Fields(contracts)
			count := len(item)
			if _, ok := item["parameters"]; ok {
				count--
			}
			if count != len(operations) {
				t.Errorf("operation count = %d, want %d", count, len(operations))
			}
			for _, contract := range operations {
				parts := strings.Split(contract, ":")
				method, success, scheme := parts[0], parts[1], parts[2]
				op, ok := item[method].(map[string]any)
				if !ok {
					t.Fatalf("missing %s", method)
				}
				id, _ := op["operationId"].(string)
				if id == "" || ids[id] {
					t.Errorf("missing/duplicate operationId: %q", id)
				}
				ids[id] = true
				wantSecurity := []any{}
				if scheme != "-" {
					wantSecurity = []any{map[string]any{scheme: []any{}}}
				}
				if !reflect.DeepEqual(op["security"], wantSecurity) {
					t.Errorf("%s security = %v, want %v", method, op["security"], wantSecurity)
				}
				responses := op["responses"].(map[string]any)
				if responses[success] == nil {
					t.Errorf("%s missing success %s", method, success)
				}
				for status, value := range responses {
					if strings.HasPrefix(status, "2") && status != success {
						t.Errorf("%s unexpected success code %s", method, status)
					}
					if status == "204" && value.(map[string]any)["content"] != nil {
						t.Error("204 response must have no body")
					}
				}
				if strings.HasPrefix(path, "/api/v1/") {
					for _, code := range []string{"429", "503", "500"} {
						if responses[code] == nil {
							t.Errorf("%s missing common response %s", method, code)
						}
					}
				}
				parameters := map[string]map[string]any{}
				for _, source := range []map[string]any{item, op} {
					list, _ := source["parameters"].([]any)
					for _, value := range list {
						p := value.(map[string]any)
						if ref, ok := p["$ref"].(string); ok {
							p = resolve(t, doc, ref)
						}
						parameters[p["name"].(string)] = p
					}
				}
				for _, segment := range strings.Split(path, "/") {
					if strings.HasPrefix(segment, "{") {
						name := strings.Trim(segment, "{}")
						if p := parameters[name]; p == nil || p["in"] != "path" || p["required"] != true {
							t.Errorf("%s missing required path parameter %s", method, name)
						}
					}
				}
				if method != "get" {
					for _, name := range []string{"Origin", "X-CSRF-Token", "__Host-csrf"} {
						if p := parameters[name]; p == nil || p["required"] != true {
							t.Errorf("%s missing required %s", method, name)
						}
					}
					for _, code := range []string{"400", "403", "413", "415"} {
						if responses[code] == nil {
							t.Errorf("%s missing mutation response %s", method, code)
						}
					}
				}
			}
		})
	}
}

func TestSchemaContract(t *testing.T) {
	t.Parallel()
	doc := document(t)
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	for _, name := range []string{"Profile", "Address", "Consultant", "ConsultationType", "Offering", "WorkInterval", "Slot", "Booking", "BookingEvent"} {
		schema := schemas[name].(map[string]any)
		properties := schema["properties"].(map[string]any)
		required := schema["required"].([]any)
		if len(required) != len(properties) {
			t.Errorf("%s: every response field must be required, even when nullable", name)
		}
		for _, field := range required {
			if properties[field.(string)] == nil {
				t.Errorf("%s: unknown required field %s", name, field)
			}
		}
	}
	for _, name := range []string{"ID", "NullableID"} {
		schema := schemas[name].(map[string]any)
		if schema["type"] != "integer" || schema["format"] != "int64" {
			t.Errorf("%s must be an int64", name)
		}
	}
	price := schemas["Price"].(map[string]any)
	if price["type"] != "string" {
		t.Fatal("price must be a decimal string")
	}
	pattern := regexp.MustCompile(price["pattern"].(string))
	for _, value := range []string{"0", "0.00", "125.0", "9999999999.99"} {
		if !pattern.MatchString(value) {
			t.Errorf("valid price rejected: %s", value)
		}
	}
	for _, value := range []string{"-1", "01", "1e2", "1.234", "10000000000", ".50"} {
		if pattern.MatchString(value) {
			t.Errorf("invalid price accepted: %s", value)
		}
	}
	for scheme, cookie := range map[string]string{"Session": "__Host-session", "LoginChallenge": "__Host-login-challenge"} {
		value := resolve(t, doc, "#/components/securitySchemes/"+scheme)
		if value["type"] != "apiKey" || value["in"] != "cookie" || value["name"] != cookie {
			t.Errorf("invalid cookie security scheme %s", scheme)
		}
	}
	rate := resolve(t, doc, "#/components/responses/RateLimited")
	if rate["headers"].(map[string]any)["Retry-After"] == nil {
		t.Error("429 must document Retry-After")
	}
}

func TestRegister(t *testing.T) {
	t.Parallel()
	app := fiber.New(fiber.Config{StrictRouting: true})
	app.Use(func(c fiber.Ctx) error {
		c.Set("Cache-Control", "no-store")
		c.Set("X-Content-Type-Options", "nosniff")
		return c.Next()
	})
	Register(app)
	app.Use(func(c fiber.Ctx) error { return c.SendStatus(404) })
	for _, path := range []string{"/openapi.json", "/swagger", "/swagger/", "/swagger/index.html", "/swagger/missing", "/api/v1", "/"} {
		t.Run(path, func(t *testing.T) {
			res, err := app.Test(httptest.NewRequest("GET", "https://example.com"+path, nil))
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if path == "/swagger/missing" || path == "/api/v1" || path == "/" {
				if res.StatusCode != 404 {
					t.Fatalf("status = %d, want 404", res.StatusCode)
				}
				return
			}
			if res.StatusCode != 200 {
				t.Fatalf("status = %d, want 200", res.StatusCode)
			}
			want, contentType := index, "text/html"
			if path == "/openapi.json" {
				want, contentType = spec, "application/json"
			}
			body, err := io.ReadAll(res.Body)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(body, want) || !strings.HasPrefix(res.Header.Get("Content-Type"), contentType) {
				t.Error("incorrect embedded asset or MIME type")
			}
			if res.Header.Get("Cache-Control") != "no-store" || res.Header.Get("X-Content-Type-Options") != "nosniff" {
				t.Error("global middleware headers not preserved")
			}
		})
	}
}

func TestUIPrivacySettings(t *testing.T) {
	t.Parallel()
	html := string(index)
	for _, required := range []string{
		"swagger-ui-dist@5.30.2/swagger-ui.css", "swagger-ui-dist@5.30.2/swagger-ui-bundle.js",
		`fetch("/openapi.json"`, "validatorUrl: null", "persistAuthorization: false",
		"spec.components.parameters.CSRFHeader.schema.default", "delete request.headers[name]",
		"url.origin !== window.location.origin", `request.credentials = "include"`,
		`fetch("/csrf", {credentials: "include", cache: "no-store", redirect: "error"})`,
		`request.headers["X-CSRF-Token"] = token`, "__Host-csrf=",
	} {
		if !strings.Contains(html, required) {
			t.Errorf("UI missing security setting %q", required)
		}
	}
	for _, forbidden := range []string{"localStorage", "sessionStorage", "console.log", `headers["Origin"]`, "@latest"} {
		if strings.Contains(html, forbidden) {
			t.Errorf("UI contains forbidden behavior %q", forbidden)
		}
	}
}
