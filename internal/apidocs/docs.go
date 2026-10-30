// Package apidocs serves the embedded API contract and same-origin Swagger UI.
package apidocs

import (
	_ "embed"

	"github.com/gofiber/fiber/v3"
)

//go:embed openapi.json
var spec []byte

//go:embed index.html
var index []byte

// Register installs public documentation routes. Call after global middleware
// and before the application's final not-found handler.
func Register(app *fiber.App) {
	app.Get("/openapi.json", func(c fiber.Ctx) error {
		return c.Type("json").Send(spec)
	})
	for _, path := range []string{"/swagger", "/swagger/", "/swagger/index.html"} {
		app.Get(path, func(c fiber.Ctx) error {
			return c.Type("html").Send(index)
		})
	}
}
