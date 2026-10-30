package domain

import "errors"

// ErrDocumentExists indicates that a document identity is already stored.
var ErrDocumentExists = errors.New("document already exists")
