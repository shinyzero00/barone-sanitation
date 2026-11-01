package apidocs

import (
	"bytes"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
)

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
			if path == "/openapi.json" || path == "/swagger/missing" || path == "/api/v1" || path == "/" {
				if res.StatusCode != 404 {
					t.Fatalf("status = %d, want 404", res.StatusCode)
				}
				return
			}
			if res.StatusCode != 200 {
				t.Fatalf("status = %d, want 200", res.StatusCode)
			}
			body, err := io.ReadAll(res.Body)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(body, index) || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/html") {
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
		`parameter.name === "X-CSRF-Token"`, "delete request.headers[name]",
		`["origin", "cookie", "x-csrf-token"].includes(name.toLowerCase())`,
		`request.redirect = "error"`, "withCredentials: true",
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
