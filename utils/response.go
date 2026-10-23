package utils

import "github.com/gofiber/fiber/v3"

type errorBody struct {
	ErrorMessage string `json:"errorMessage"`
	Status       string `json:"status"`
	HasError     bool   `json:"hasError"`
}

// ReportError writes the legacy error JSON body with the given HTTP status.
func ReportError(c fiber.Ctx, err string, statusCode int) error {
	return c.Status(statusCode).JSON(errorBody{
		ErrorMessage: err,
		Status:       "FAIL",
		HasError:     true,
	})
}

// ErrorResult builds the legacy error body for one failed batch item.
func ErrorResult(err string) any {
	return errorBody{
		ErrorMessage: err,
		Status:       "FAIL",
		HasError:     true,
	}
}
