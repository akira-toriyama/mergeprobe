// Package core holds mergeprobe's dependency-free domain contract: no I/O, no
// globals, deterministic. The process exit-code contract lives here so its
// meanings are defined in exactly one place.
package core

import (
	"errors"
	"fmt"
)

// Code is mergeprobe's process exit-code contract. The CLI maps a returned error to
// one of these on the way out. Keep the meanings stable — scripts and AI
// agents branch on them.
type Code int

const (
	CodeOK         Code = 0 // success
	CodeNotFound   Code = 1 // requested resource / result set was empty — a soft miss, not a crash
	CodeValidation Code = 2 // bad usage or invalid input — fix the args, do not retry
	CodeInternal   Code = 3 // internal / IO failure — not the caller's fault; retrying may help, otherwise report a bug
)

// Error is mergeprobe's structured error. On a non-zero exit the CLI prints it to
// stderr as {"error":{"code","message"[,"id","details"]}} so callers get a
// machine-readable failure. Plain (non-*Error) errors are treated as
// CodeInternal.
type Error struct {
	Code Code
	ID   string // stable error slug (e.g. "diff-too-large"), or ""
	Msg  string
	// Details is optional machine-actionable payload for errors where the
	// message alone is not enough to act on.
	Details any
}

func (e *Error) Error() string { return e.Msg }

// NotFoundf builds a CodeNotFound error (soft miss).
func NotFoundf(id, format string, a ...any) *Error {
	return &Error{Code: CodeNotFound, ID: id, Msg: fmt.Sprintf(format, a...)}
}

// Validationf builds a CodeValidation error (bad input — fix the args).
func Validationf(id, format string, a ...any) *Error {
	return &Error{Code: CodeValidation, ID: id, Msg: fmt.Sprintf(format, a...)}
}

// Internalf builds a CodeInternal error (unexpected failure).
func Internalf(id, format string, a ...any) *Error {
	return &Error{Code: CodeInternal, ID: id, Msg: fmt.Sprintf(format, a...)}
}

// AsError unwraps err to this package's *Error, or nil when err carries none.
func AsError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return nil
}

// ExitCode resolves any error to a process exit code: nil -> 0, an *Error ->
// its Code, anything else -> CodeInternal. An unclassified failure is internal
// by definition — it never falls back to usage.
func ExitCode(err error) int {
	if err == nil {
		return int(CodeOK)
	}
	if e := AsError(err); e != nil {
		return int(e.Code)
	}
	return int(CodeInternal)
}
