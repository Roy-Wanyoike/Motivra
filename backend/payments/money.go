package payments

import (
	"fmt"
	"math"
)

// CurrencyKES is the only currency this domain settles in. The database
// CHECK constraints mirror it.
const CurrencyKES = "KES"

// AllMoney is all money in payments: integer minor units (cents) of KES in
// int64 — never floats. Every arithmetic operation below is overflow- and
// negativity-checked, and the tests in money_test.go enforce the edges
// (issue #62 acceptance criterion: "no float money anywhere in the
// package"; `grep -r 'float64.*[Aa]mount'` finds nothing).

// Money is an amount of money: an int64 count of KES minor units (cents).
// AmountMinor is always strictly positive; Money zero values are invalid
// by construction and Validate refuses them.
type Money struct {
	AmountMinor int64
	Currency    string
}

// NewMoney returns a validated Money of amount minor units in currency.
func NewMoney(amountMinor int64, currency string) (Money, error) {
	m := Money{AmountMinor: amountMinor, Currency: currency}
	if err := m.Validate(); err != nil {
		return Money{}, err
	}
	return m, nil
}

// Validate reports whether m carries a legal payment amount: a strictly
// positive integer minor-unit count in the supported currency. Zero and
// negative amounts are ledger nonsense (there is no zero-value payment),
// and only KES is supported.
func (m Money) Validate() error {
	if m.Currency != CurrencyKES {
		return fmt.Errorf("payments: currency %q is not supported (only %s)", m.Currency, CurrencyKES)
	}
	if m.AmountMinor <= 0 {
		return fmt.Errorf("payments: amount %d minor units is not a positive integer", m.AmountMinor)
	}
	return nil
}

// Add returns a+b with the overflow safety the int64 ledger demands: the
// sum of two legal amounts must itself remain a legal amount, and any
// overflow (or cross-currency addition) is an error, never a wraparound.
func Add(a, b Money) (Money, error) {
	if err := a.Validate(); err != nil {
		return Money{}, fmt.Errorf("payments: add left operand: %w", err)
	}
	if err := b.Validate(); err != nil {
		return Money{}, fmt.Errorf("payments: add right operand: %w", err)
	}
	if a.Currency != b.Currency {
		return Money{}, fmt.Errorf("payments: cannot add %s to %s", a.Currency, b.Currency)
	}
	// Both operands are strictly positive, so only upward overflow is
	// possible; detect it without touching the wraparound.
	if a.AmountMinor > math.MaxInt64-b.AmountMinor {
		return Money{}, fmt.Errorf("payments: %d + %d overflows int64 minor units",
			a.AmountMinor, b.AmountMinor)
	}
	return Money{AmountMinor: a.AmountMinor + b.AmountMinor, Currency: a.Currency}, nil
}

// Subtract returns a-b and refuses any result that is not a strictly
// positive legal amount: the ledger has no negative and no zero balances —
// an over- or under-refund is an error, never a negative row.
func Subtract(a, b Money) (Money, error) {
	if err := a.Validate(); err != nil {
		return Money{}, fmt.Errorf("payments: subtract left operand: %w", err)
	}
	if err := b.Validate(); err != nil {
		return Money{}, fmt.Errorf("payments: subtract right operand: %w", err)
	}
	if a.Currency != b.Currency {
		return Money{}, fmt.Errorf("payments: cannot subtract %s from %s", b.Currency, a.Currency)
	}
	if b.AmountMinor > a.AmountMinor {
		return Money{}, fmt.Errorf("payments: %d - %d would go negative; the ledger has no negative amounts",
			a.AmountMinor, b.AmountMinor)
	}
	if b.AmountMinor == a.AmountMinor {
		return Money{}, fmt.Errorf("payments: %d - %d is zero; the ledger has no zero amounts",
			a.AmountMinor, b.AmountMinor)
	}
	return Money{AmountMinor: a.AmountMinor - b.AmountMinor, Currency: a.Currency}, nil
}

// EqualSameCurrency reports whether a and b carry the exact same amount
// and currency. Provider-attested amounts must equal the ledger amount
// exactly — there is no tolerance and no rounding anywhere in this domain.
func EqualSameCurrency(a, b Money) bool {
	return a.AmountMinor == b.AmountMinor && a.Currency == b.Currency
}

// String renders the amount for logs and error messages, e.g. "KES 123.45".
// Minor units are integer-split, so no float ever touches rendering.
func (m Money) String() string {
	if m.Currency == "" {
		return fmt.Sprintf("%d", m.AmountMinor)
	}
	if m.AmountMinor < 0 {
		// Negative amounts are invalid domain state; render defensively for
		// error paths without legitimizing them.
		return fmt.Sprintf("%s -%d.%02d", m.Currency, -m.AmountMinor/100, -m.AmountMinor%100)
	}
	return fmt.Sprintf("%s %d.%02d", m.Currency, m.AmountMinor/100, m.AmountMinor%100)
}
