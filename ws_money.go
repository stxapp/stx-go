package stx

import (
	"bytes"
	"encoding/json"
	"strings"
)

// The markets, market_updates and market:<id> channels send prices as JSON
// numbers in cents and contract counts as JSON numbers. The stream rewrites
// those fields so every amount it hands you is a string in the API's format:
// prices in dollars with at least four decimals, quantities with at least
// two. The book on market:<id> ("ob") is already in dollars and contracts, so
// its levels are only written as strings. Percentages, probabilities and
// timestamps stay numbers. The arithmetic is done on decimal digits.

var (
	centsFields    = []string{"price", "last_traded_price", "max_price"}
	quantityFields = []string{"total_volume", "volume_24h", "open_interest"}
	levelLists     = []string{"bids", "offers", "recent_trades"}
	priceRuleKeys  = []string{"from", "to", "inc"}
)

// maxExponent bounds the exponent of a number the stream rewrites; a larger
// one is passed through as sent rather than expanded digit by digit.
const maxExponent = 1000

// decimalParts splits a JSON number literal into sign, digits and scale:
// value = sign * digits * 10^-scale.
func decimalParts(lit string) (neg bool, digits string, scale int, ok bool) {
	s := lit
	if strings.HasPrefix(s, "-") {
		neg, s = true, s[1:]
	}
	exp := 0
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		e := s[i+1:]
		s = s[:i]
		sign := 1
		switch {
		case strings.HasPrefix(e, "-"):
			sign, e = -1, e[1:]
		case strings.HasPrefix(e, "+"):
			e = e[1:]
		}
		if e == "" {
			return false, "", 0, false
		}
		for _, c := range e {
			if c < '0' || c > '9' {
				return false, "", 0, false
			}
			exp = exp*10 + int(c-'0')
			if exp > maxExponent {
				return false, "", 0, false
			}
		}
		exp *= sign
	}
	intPart, frac, _ := strings.Cut(s, ".")
	digits = intPart + frac
	if digits == "" {
		return false, "", 0, false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return false, "", 0, false
		}
	}
	scale = len(frac) - exp
	if scale < 0 {
		digits += strings.Repeat("0", -scale)
		scale = 0
	}
	digits = strings.TrimLeft(digits, "0")
	if digits == "" {
		digits = "0"
	}
	return neg, digits, scale, true
}

// formatDecimal writes digits*10^-scale with at least minPlaces decimals,
// dropping other trailing zeros.
func formatDecimal(neg bool, digits string, scale, minPlaces int) string {
	for scale > 0 && len(digits) > 1 && strings.HasSuffix(digits, "0") {
		digits = digits[:len(digits)-1]
		scale--
	}
	places := max(minPlaces, scale)
	digits += strings.Repeat("0", places-scale)
	if len(digits) < places+1 {
		digits = strings.Repeat("0", places+1-len(digits)) + digits
	}
	intPart, fracPart := digits[:len(digits)-places], digits[len(digits)-places:]
	zero := strings.Trim(intPart+fracPart, "0") == ""
	out := intPart
	if places > 0 {
		out += "." + fracPart
	}
	if neg && !zero {
		out = "-" + out
	}
	return out
}

// numberText formats a JSON number with an extra scale shift and a minimum
// number of decimals. Anything else passes through.
func numberText(v any, shift, minPlaces int) any {
	n, ok := v.(json.Number)
	if !ok {
		return v
	}
	neg, digits, scale, ok := decimalParts(n.String())
	if !ok {
		return v
	}
	return formatDecimal(neg, digits, scale+shift, minPlaces)
}

func centsToDollars(v any) any { return numberText(v, 2, 4) }
func dollarText(v any) any     { return numberText(v, 0, 4) }
func quantityText(v any) any   { return numberText(v, 0, 2) }

// convertMarket rewrites one market object in place.
func convertMarket(m map[string]any) {
	for _, k := range centsFields {
		if v, ok := m[k]; ok {
			m[k] = centsToDollars(v)
		}
	}
	for _, k := range quantityFields {
		if v, ok := m[k]; ok {
			m[k] = quantityText(v)
		}
	}
	for _, k := range levelLists {
		levels, _ := m[k].([]any)
		for _, l := range levels {
			if level, ok := l.(map[string]any); ok {
				if v, ok := level["price"]; ok {
					level["price"] = centsToDollars(v)
				}
				if v, ok := level["quantity"]; ok {
					level["quantity"] = quantityText(v)
				}
			}
		}
	}
	rules, _ := m["order_price_rules"].([]any)
	for _, r := range rules {
		if rule, ok := r.(map[string]any); ok {
			for _, k := range priceRuleKeys {
				if v, ok := rule[k]; ok {
					rule[k] = centsToDollars(v)
				}
			}
		}
	}
	if ob, ok := m["ob"].(map[string]any); ok {
		for _, side := range []string{"b", "o"} {
			levels, _ := ob[side].([]any)
			for _, l := range levels {
				if level, ok := l.(map[string]any); ok {
					for _, k := range []string{"p", "l", "tl"} {
						if v, ok := level[k]; ok {
							level[k] = dollarText(v)
						}
					}
					for _, k := range []string{"q", "tc"} {
						if v, ok := level[k]; ok {
							level[k] = quantityText(v)
						}
					}
				}
			}
		}
	}
}

func decodeNumbers(raw json.RawMessage) (any, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return nil, false
	}
	return v, true
}

// convertPayload converts a payload on the channels that send cents.
func convertPayload(channel string, raw json.RawMessage) json.RawMessage {
	switch channel {
	case "markets", "market_updates", "market":
	default:
		return raw
	}
	v, ok := decodeNumbers(raw)
	if !ok {
		return raw
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return raw
	}
	if channel == "markets" {
		for _, m := range obj {
			if market, ok := m.(map[string]any); ok {
				convertMarket(market)
			}
		}
	} else {
		convertMarket(obj)
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return out
}

// convertReply converts the join reply of market:<id>, which is the market.
func convertReply(channel string, raw json.RawMessage) json.RawMessage {
	if channel != "market" {
		return raw
	}
	return convertPayload(channel, raw)
}
