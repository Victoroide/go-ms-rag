package pipeline

import "errors"

// ErrSynthesisEmpty indicates that answer synthesis returned no text.
var ErrSynthesisEmpty = errors.New("synthesis returned no text")
