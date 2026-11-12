package stx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
)

// Decimal is an exact decimal number carried as its text, such as "0.5600"
// for a price in dollars or "100.00" for a quantity of contracts.
//
// It round-trips through JSON unchanged: it is written as a JSON string, and
// read from a JSON string or a JSON number (keeping the number's digits).
// Arithmetic is left to you; [Decimal.Rat] gives an exact [big.Rat].
type Decimal string

var decimalPattern = regexp.MustCompile(`^-?(\d+(\.\d*)?|\.\d+)([eE][-+]?\d+)?$`)

// ParseDecimal checks that s is a decimal number and returns it unchanged.
func ParseDecimal(s string) (Decimal, error) {
	if !decimalPattern.MatchString(s) {
		return "", fmt.Errorf("stx: %q is not a decimal number", s)
	}
	return Decimal(s), nil
}

// MustDecimal is [ParseDecimal] for constants: it panics when s is not a
// decimal number.
func MustDecimal(s string) Decimal {
	d, err := ParseDecimal(s)
	if err != nil {
		panic(err)
	}
	return d
}

// String returns the decimal text.
func (d Decimal) String() string { return string(d) }

// IsZero reports whether d is empty (absent) or numerically zero.
func (d Decimal) IsZero() bool {
	if d == "" {
		return true
	}
	r, ok := d.Rat()
	return ok && r.Sign() == 0
}

// Valid reports whether d is a well-formed decimal number.
func (d Decimal) Valid() bool { return decimalPattern.MatchString(string(d)) }

// Rat returns d as an exact rational number. ok is false when d is empty or
// not a decimal number.
func (d Decimal) Rat() (r *big.Rat, ok bool) {
	if !d.Valid() {
		return nil, false
	}
	return new(big.Rat).SetString(string(d))
}

// Cmp compares d and other numerically: -1 if d < other, 0 if equal, +1 if
// d > other. An empty or malformed value compares as zero.
func (d Decimal) Cmp(other Decimal) int {
	a, ok := d.Rat()
	if !ok {
		a = new(big.Rat)
	}
	b, ok := other.Rat()
	if !ok {
		b = new(big.Rat)
	}
	return a.Cmp(b)
}

// Float64 returns the nearest float64. It is lossy; use it for display and
// statistics, never to build a price or quantity you send back.
func (d Decimal) Float64() float64 {
	r, ok := d.Rat()
	if !ok {
		return 0
	}
	f, _ := r.Float64()
	return f
}

// MarshalJSON writes d as a JSON string, or null when d is empty.
func (d Decimal) MarshalJSON() ([]byte, error) {
	if d == "" {
		return []byte("null"), nil
	}
	return json.Marshal(string(d))
}

// UnmarshalJSON reads a JSON string, a JSON number (its literal digits are
// kept), or null.
func (d *Decimal) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	switch {
	case bytes.Equal(data, []byte("null")):
		*d = ""
		return nil
	case len(data) > 0 && data[0] == '"':
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*d = Decimal(s)
		return nil
	default:
		if !decimalPattern.Match(data) {
			return fmt.Errorf("stx: cannot read %s as a decimal", data)
		}
		*d = Decimal(data)
		return nil
	}
}
