package dispatch

import (
	"hetman/internal/i18n"
)

// textError is an error a person reads: a catalogue message that says
// itself in the reader's language, and in English as Error. It wraps the
// sentinel errors.Is looks for, when there is one.
type textError struct {
	msg  i18n.Message
	base error
}

func newError(base error, key string, args ...any) error {
	return &textError{msg: i18n.M(key, args...), base: base}
}

func (e *textError) Error() string                   { return e.msg.String() }
func (e *textError) Localize(p *i18n.Printer) string { return e.msg.In(p) }
func (e *textError) Unwrap() error                   { return e.base }
