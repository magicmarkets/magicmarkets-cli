package magicmarkets

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Stable error codes returned by the API. The HTTP status conveys the
// category; the code narrows it down.
const (
	CodeValidationError     = "validation_error"
	CodeOrderClosed         = "order_closed"
	CodeAuthError           = "auth_error"
	CodeForbidden           = "forbidden"
	CodeNotFound            = "not_found"
	CodeOrderAlreadyCreated = "order_already_created"
	CodeLimitReached        = "limit_reached"
	CodeThrottled           = "throttled"
	CodeServerError         = "server_error"
)

// APIError is a non-2xx response carrying the API's error envelope.
type APIError struct {
	// HTTPStatus is the response status code.
	HTTPStatus int

	// Code is the stable machine-readable code, e.g. "validation_error".
	// Empty for 503 responses, which have no envelope.
	Code string

	// Detail is a human-readable message extracted from data when available.
	Detail string

	// ValidationErrors maps field name to reasons, for CodeValidationError.
	// Cross-field problems land under "non_field_errors".
	ValidationErrors map[string][]string

	// RetryAfter is the wait in seconds, for CodeThrottled.
	RetryAfter int

	// ExistingOrderID is set for CodeOrderAlreadyCreated: the order the
	// reused request_uuid already produced.
	ExistingOrderID int64

	// SupportToken is the correlation token from a CodeServerError response.
	// Quote it when contacting support.
	SupportToken string

	// Data is the raw data field, for codes not otherwise modelled.
	Data json.RawMessage
}

func (e *APIError) Error() string {
	var b strings.Builder
	if e.Code != "" {
		fmt.Fprintf(&b, "%s (HTTP %d)", e.Code, e.HTTPStatus)
	} else {
		fmt.Fprintf(&b, "HTTP %d", e.HTTPStatus)
	}

	if len(e.ValidationErrors) != 0 {
		fields := make([]string, 0, len(e.ValidationErrors))
		for f := range e.ValidationErrors {
			fields = append(fields, f)
		}
		sort.Strings(fields)
		for _, f := range fields {
			fmt.Fprintf(&b, "\n  %s: %s", f, strings.Join(e.ValidationErrors[f], "; "))
		}
		return b.String()
	}

	if e.Detail != "" {
		fmt.Fprintf(&b, ": %s", e.Detail)
	}
	switch {
	case e.RetryAfter > 0:
		fmt.Fprintf(&b, " (retry after %ds)", e.RetryAfter)
	case e.ExistingOrderID != 0:
		fmt.Fprintf(&b, " (existing order_id %d)", e.ExistingOrderID)
	case e.SupportToken != "":
		fmt.Fprintf(&b, " (support token %s)", e.SupportToken)
	}
	return b.String()
}

// HasCode reports whether err is an APIError with the given code.
func HasCode(err error, code string) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.Code == code
}

// parseAPIError builds an APIError from a response body. The shape of data
// varies by code, so each known code is unpacked individually.
func parseAPIError(status int, retryAfterHeader int, body []byte) *APIError {
	e := &APIError{HTTPStatus: status, RetryAfter: retryAfterHeader}

	var env struct {
		Status string          `json:"status"`
		Code   string          `json:"code"`
		Detail string          `json:"detail"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		// No envelope at all (proxy error page, truncated body, ...).
		e.Detail = strings.TrimSpace(truncate(string(body), 200))
		return e
	}

	e.Code = env.Code
	e.Data = env.Data

	// 503 has no envelope, just {"detail": "Service unavailable"}.
	if e.Code == "" && env.Detail != "" {
		e.Detail = env.Detail
		return e
	}

	switch e.Code {
	case CodeValidationError:
		var d struct {
			ValidationErrors map[string][]string `json:"validation_errors"`
			Detail           string              `json:"detail"`
		}
		if json.Unmarshal(env.Data, &d) == nil {
			e.ValidationErrors = d.ValidationErrors
			e.Detail = d.Detail
		}

	case CodeThrottled:
		var d struct {
			Message    string `json:"message"`
			RetryAfter int    `json:"retry_after"`
		}
		if json.Unmarshal(env.Data, &d) == nil {
			e.Detail = d.Message
			if d.RetryAfter > 0 {
				e.RetryAfter = d.RetryAfter
			}
		}

	case CodeOrderAlreadyCreated:
		var d struct {
			OrderID int64  `json:"order_id"`
			Detail  string `json:"detail"`
		}
		if json.Unmarshal(env.Data, &d) == nil {
			e.ExistingOrderID = d.OrderID
			e.Detail = d.Detail
		}

	case CodeServerError:
		// data is ["An error has occurred, token:", "<token>"].
		var parts []string
		if json.Unmarshal(env.Data, &parts) == nil && len(parts) > 0 {
			e.Detail = parts[0]
			if len(parts) > 1 {
				e.SupportToken = parts[len(parts)-1]
			}
		}

	default:
		e.Detail = detailFromData(env.Data)
	}

	if e.Detail == "" && len(e.ValidationErrors) == 0 {
		e.Detail = detailFromData(env.Data)
	}
	return e
}

// detailFromData pulls a human-readable string out of a variable data field,
// which may be null, a bare string, or an object with a detail/message key.
func detailFromData(data json.RawMessage) string {
	if len(data) == 0 || string(data) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(data, &s) == nil {
		return s
	}
	var obj struct {
		Detail  string `json:"detail"`
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &obj) == nil {
		if obj.Detail != "" {
			return obj.Detail
		}
		if obj.Message != "" {
			return obj.Message
		}
	}
	return truncate(string(data), 200)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
