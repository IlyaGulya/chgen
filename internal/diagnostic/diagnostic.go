// Package diagnostic preserves machine-readable refusal reasons through the
// existing error chain. Unknown is deliberately not a synonym for invalid SQL.
package diagnostic

import "errors"

const (
	Confirmed = "confirmed"
	Invalid   = "invalid"
	Unknown   = "unknown"
)

type Detail struct {
	Code, Status, Stage, Message, Hint string
	Package, File, Query               string
	Line                               int
}

type reason struct {
	detail Detail
	cause  error
}

func (e *reason) Error() string      { return e.cause.Error() }
func (e *reason) Unwrap() error      { return e.cause }
func (e *reason) Diagnostic() Detail { return e.detail }

// With adds a reason or source context without changing error text or identity.
func With(err error, detail Detail) error {
	if err == nil {
		return nil
	}
	return &reason{detail: detail, cause: err}
}

// Describe never guesses a classification by matching an error message.
// Refusals not yet classified remain explicitly unknown.
func Describe(err error) Detail {
	if err == nil {
		return Detail{}
	}
	result := Detail{Message: err.Error()}
	for current := err; current != nil; current = errors.Unwrap(current) {
		provider, ok := current.(interface{ Diagnostic() Detail })
		if !ok {
			continue
		}
		d := provider.Diagnostic()
		if result.Code == "" && d.Code != "" {
			result.Code, result.Status, result.Hint = d.Code, d.Status, d.Hint
			if d.Stage != "" {
				result.Stage = d.Stage
			}
		}
		if result.Stage == "" {
			result.Stage = d.Stage
		}
		if result.Package == "" {
			result.Package = d.Package
		}
		if result.File == "" {
			result.File = d.File
		}
		if result.Query == "" {
			result.Query = d.Query
		}
		if result.Line == 0 {
			result.Line = d.Line
		}
	}
	if result.Code == "" {
		result.Code, result.Status = "unclassified", Unknown
		result.Hint = "This refusal is not classified yet. Inspect the message; it does not by itself prove the SQL is invalid."
	}
	return result
}
