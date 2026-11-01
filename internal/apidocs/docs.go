// Package apidocs serves the same-origin Swagger UI for Huma's generated contract.
package apidocs

import (
	_ "embed"

	"github.com/gofiber/fiber/v3"
)

//go:embed index.html
var index []byte

// Register installs public documentation routes. Call after global middleware
// and before the application's final not-found handler.
func Register(app *fiber.App) {
	for _, path := range []string{"/swagger", "/swagger/", "/swagger/index.html"} {
		app.Get(path, func(c fiber.Ctx) error {
			return c.Type("html").Send(index)
		})
	}
}
