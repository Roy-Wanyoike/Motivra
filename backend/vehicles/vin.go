// Package vehicles implements the Motivra Vehicle Registry domain: vehicle
// identity (ISO 3779 VIN normalization and validation), the append-only
// service history and the read-only Vehicle Passport (docs/GLOSSARY.md).
package vehicles

import (
	"fmt"
	"strings"
)

// vinLength is the ISO 3779 VIN length in characters.
const vinLength = 17

// vinForbidden are the letters excluded from VINs because they are visually
// confusable with digits (I/1, O/0, Q/0).
const vinForbidden = "IOQ"

// vinCheckDigitChars maps the check-digit remainder (0-10) to its character.
// A remainder of 10 is written as X.
const vinCheckDigitChars = "0123456789X"

// vinCharValues transliterates every allowed VIN character to its ISO 3779
// numeric value. I, O and Q are absent on purpose.
var vinCharValues = map[byte]int{
	'0': 0, '1': 1, '2': 2, '3': 3, '4': 4, '5': 5, '6': 6, '7': 7, '8': 8, '9': 9,
	'A': 1, 'B': 2, 'C': 3, 'D': 4, 'E': 5, 'F': 6, 'G': 7, 'H': 8,
	'J': 1, 'K': 2, 'L': 3, 'M': 4, 'N': 5, 'P': 7, 'R': 9,
	'S': 2, 'T': 3, 'U': 4, 'V': 5, 'W': 6, 'X': 7, 'Y': 8, 'Z': 9,
}

// vinPositionWeights is the ISO 3779 weight applied to each of the 17 VIN
// positions.
var vinPositionWeights = [vinLength]int{8, 7, 6, 5, 4, 3, 2, 10, 0, 9, 8, 7, 6, 5, 4, 3, 2}

// NormalizeVIN canonicalizes raw user input into the canonical stored form:
// uppercase, with every space and hyphen removed. It rejects input whose
// length is not exactly 17 characters after stripping, characters outside
// A-Z and 0-9, and the forbidden letters I, O and Q.
func NormalizeVIN(raw string) (string, error) {
	stripped := strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' {
			return -1 // drop
		}
		return r
	}, raw)
	vin := strings.ToUpper(stripped)

	if len(vin) != vinLength {
		return "", fmt.Errorf("vin must be exactly %d characters after normalization, got %d", vinLength, len(vin))
	}
	for i := 0; i < len(vin); i++ {
		c := vin[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		default:
			return "", fmt.Errorf("vin contains invalid character %q at position %d", c, i+1)
		}
		if strings.IndexByte(vinForbidden, c) >= 0 {
			return "", fmt.Errorf("vin contains forbidden character %q at position %d", c, i+1)
		}
	}
	return vin, nil
}

// ValidateVIN normalizes raw input and then verifies the ISO 3779 check
// digit at position 9. It returns the normalized VIN so callers can replace
// the raw input with the canonical form.
func ValidateVIN(raw string) (string, error) {
	vin, err := NormalizeVIN(raw)
	if err != nil {
		return "", err
	}
	want, err := vinCheckDigit(vin)
	if err != nil {
		return "", err
	}
	if vin[8] != want {
		return "", fmt.Errorf("vin check digit mismatch at position 9: expected %q, got %q", want, vin[8])
	}
	return vin, nil
}

// vinCheckDigit computes the ISO 3779 check-digit character for vin (all 17
// positions; position 9 is ignored through its zero weight).
func vinCheckDigit(vin string) (byte, error) {
	if len(vin) != vinLength {
		return 0, fmt.Errorf("vin must be exactly %d characters, got %d", vinLength, len(vin))
	}
	sum := 0
	for i := 0; i < vinLength; i++ {
		v, ok := vinCharValues[vin[i]]
		if !ok {
			return 0, fmt.Errorf("vin contains invalid character %q at position %d", vin[i], i+1)
		}
		sum += v * vinPositionWeights[i]
	}
	return vinCheckDigitChars[sum%11], nil
}
