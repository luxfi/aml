// Copyright 2024-2026 Lux Industries Inc. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package watch

import (
	"encoding/json"
	"fmt"
	"time"
)

// Span is a length of time that reads and writes as one.
//
// A time.Duration marshals to a count of nanoseconds, so a rung counting over an
// hour arrives on the wire as 3600000000000 and a caller writing 3600 has asked
// for three and a half microseconds. Nothing errors; the rung simply never
// matches. That is the same failure the rule vocabulary refuses for a lookback
// written without a unit (engine parseWindow), and it is refused here the same
// way: the value carries its unit, or it is not a value.
type Span time.Duration

// Duration is the span as the standard library's own type.
func (s Span) Duration() time.Duration { return time.Duration(s) }

// String is the Go duration form: 90m reads back as 1h30m0s.
func (s Span) String() string { return time.Duration(s).String() }

// MarshalJSON writes the duration form.
func (s Span) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(s).String()) }

// UnmarshalJSON reads the duration form and refuses anything else, including a
// bare number: a number has no unit, and a window off by three orders of
// magnitude is a control that does not work while appearing to.
func (s *Span) UnmarshalJSON(b []byte) error {
	var text string
	if err := json.Unmarshal(b, &text); err != nil {
		return fmt.Errorf("watch: a window is written with its unit, as in 1h or 15m, not as %s", b)
	}
	d, err := time.ParseDuration(text)
	if err != nil {
		return fmt.Errorf("watch: window %q: %w", text, err)
	}
	*s = Span(d)
	return nil
}
