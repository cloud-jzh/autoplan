package config

import "errors"

// Error contains only a stable code. Paths, environment values, parser input,
// and wrapped errors are intentionally excluded from its public representation.
type Error struct {
	code string
}

func (err *Error) Error() string { return err.code }

func newError(code string) error { return &Error{code: code} }

// ErrorCode converts any configuration failure to a safe, stable code.
func ErrorCode(err error) string {
	var configError *Error
	if errors.As(err, &configError) {
		return configError.code
	}
	return "configuration_invalid"
}
