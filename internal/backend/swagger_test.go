package backend

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/shinyzero00/barone-sanitation/internal/apidocs"
)

func TestSwaggerRoutes(t *testing.T) {
	t.Parallel()
	app := NewApp(&Service{})
	for _, path := range []string{"/openapi.json", "/swagger", "/swagger/", "/swagger/index.html"} {
		t.Run(path, func(t *testing.T) {
			response, err := app.Test(httptest.NewRequest("GET", path, nil))
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != 200 {
				t.Fatalf("status = %d", response.StatusCode)
			}
			if response.Header.Get("X-Content-Type-Options") != "nosniff" || response.Header.Get("Cache-Control") != "no-store" {
				t.Fatal("documentation bypassed security middleware")
			}
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if path == "/openapi.json" {
				if !json.Valid(body) || !strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
					t.Fatal("invalid OpenAPI response")
				}
			} else if !strings.Contains(string(body), "SwaggerUIBundle") || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/html") {
				t.Fatal("invalid Swagger UI response")
			}
		})
	}
}

func TestSwaggerRouteCoverage(t *testing.T) {
	t.Parallel()
	// Read through the public docs handler; no filesystem or database is needed.
	docs := fiber.New()
	apidocs.Register(docs)
	response, err := docs.Test(httptest.NewRequest("GET", "/openapi.json", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.NewDecoder(response.Body).Decode(&spec); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for path, item := range spec.Paths {
		for method := range item {
			if method == "parameters" {
				continue
			}
			want[strings.ToUpper(method)+" "+path] = true
		}
	}
	// Constructing the route table does not access DB; never execute API handlers.
	app := NewApp(&Service{})
	for _, route := range app.GetRoutes(true) {
		if route.Method == "HEAD" {
			continue // Fiber supplies HEAD for GET automatically.
		}
		if route.Path == "/openapi.json" || route.Path == "/swagger" || strings.HasPrefix(route.Path, "/swagger/") {
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
