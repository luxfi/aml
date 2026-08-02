// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package dictionary

import (
	"encoding/base64"
	"math"
)

// A sketch counts how many DISTINCT values a field has taken, without keeping any
// of them.
//
// That constraint is the whole design. A field catalog is a diagnostic plane, and
// the ordinary way to count distinct values — keep the values, or keep their
// hashes — puts a pseudonymised set of customer identifiers, addresses and email
// addresses into a table whose purpose is to describe shapes. The retained record
// plane is where identifiers belong, sealed and purpose-gated (pkg/token,
// pkg/retention); a statistics table is not entitled to a second copy of them
// under a weaker regime.
//
// So: linear counting over a fixed bitmap. Each value sets one bit chosen by a
// hash; the estimate is derived from how many bits are still clear. Two
// properties follow, and both matter here. It is MERGEABLE — a bitwise OR — so an
// accumulator flushed to a row and picked up again after a restart continues the
// same count rather than starting one. And it SATURATES rather than drifting: as
// the bitmap fills the estimate loses accuracy in a way that is computable from
// the bitmap itself, so the answer is "at least this many, and the count is no
// longer reliable" instead of a smaller number that reads as fact.
//
// Whitang, Vitter & others' linear counting: n̂ = -m·ln(z/m), for m buckets of
// which z are empty. At the bucket count below it is within about 1% up to a few
// thousand distinct values, which is the range where the number is interesting —
// a field with more distinct values than that is an identifier, and knowing it is
// an identifier is the whole of what the catalog needs to say.

// buckets is the bitmap's width in bits. 4096 bits is 512 bytes per field per
// tenant, and roughly 1% accurate to a few thousand distinct values.
const buckets = 4096

// full is the share of set buckets past which the estimate is no longer reported
// as a count. At 95% occupancy linear counting is extrapolating from 5% of the
// bitmap and the error grows without bound.
const full = 0.95

type sketch [buckets / 8]byte

// add records one value against a tenant-and-field-specific hash.
//
// The org is part of the hash so the same value in two tenants sets different
// buckets. Without that, two tenants' bitmaps could be compared to learn whether
// they share a customer — a cross-tenant inference computed from a diagnostics
// table, which is exactly the shape of leak the tenant key exists to make
// impossible.
func (s *sketch) add(org, field, value string) {
	h := hash(org, field, value)
	i := h % buckets
	s[i/8] |= 1 << (i % 8)
}

// merge folds another bitmap in. Union, because a distinct count is a set union
// and the same value seen in both halves must not count twice.
func (s *sketch) merge(other sketch) {
	for i := range s {
		s[i] |= other[i]
	}
}

// set counts the bits that are on.
func (s sketch) set() int {
	n := 0
	for _, b := range s {
		for ; b != 0; b &= b - 1 {
			n++
		}
	}
	return n
}

// estimate is how many distinct values the bitmap has seen, and whether the
// answer is still a count.
//
// saturated true means the bitmap is too full for the estimate to mean anything
// beyond "at least this many". It is returned rather than folded into the number,
// because a saturated bitmap reports FEWER distinct values than there are, and a
// cardinality that silently stops rising reads as a field that stopped varying.
func (s sketch) estimate() (n int64, saturated bool) {
	on := s.set()
	if on == 0 {
		return 0, false
	}
	z := buckets - on
	if z == 0 {
		return int64(math.Round(-float64(buckets) * math.Log(1/float64(buckets)))), true
	}
	est := -float64(buckets) * math.Log(float64(z)/float64(buckets))
	return int64(math.Round(est)), float64(on)/float64(buckets) >= full
}

// encode renders a bitmap for a row. Base64 of the raw bits: fixed width, no
// interpretation, and nothing in it derived from a value that could be read back.
func (s sketch) encode() string { return base64.StdEncoding.EncodeToString(s[:]) }

// decode reads a bitmap back. An unreadable or wrong-width one yields an empty
// bitmap rather than an error: the count then restarts, which understates, and
// understating a diagnostic is preferable to refusing to serve the catalog. The
// caller reports the width mismatch through the saturation flag never being set
// on a bitmap that was thrown away, and the row is rewritten on the next flush.
func decode(s string) sketch {
	var out sketch
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(raw) != len(out) {
		return out
	}
	copy(out[:], raw)
	return out
}

// hash is FNV-1a over the tenant, the field and the value.
func hash(org, field, value string) uint64 {
	var h uint64 = 14695981039346656037
	mix := func(s string) {
		for i := 0; i < len(s); i++ {
			h ^= uint64(s[i])
			h *= 1099511628211
		}
		h ^= 0
		h *= 1099511628211
	}
	mix(org)
	mix(field)
	mix(value)
	return h
}
