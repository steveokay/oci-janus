package upstream

import "errors"

// ErrNotFound is returned when the upstream registry does not have the requested resource.
var ErrNotFound = errors.New("upstream resource not found")
