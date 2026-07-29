package cases

import "errors"

// ErrNotFound is returned when a case is not found.
var ErrNotFound = errors.New("case not found")

// ErrNoAssessment is returned when a case would be closed without the retained
// decision that closed it.
var ErrNoAssessment = errors.New("case cannot be closed without a retained assessment")
