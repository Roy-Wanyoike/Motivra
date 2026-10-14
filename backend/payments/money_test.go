package payments

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The money tests enforce the integer-ledger discipline (issue #62):
// int64 minor units everywhere, overflow refused, negatives and zeros
// refused, cross-currency arithmetic refused, and no float money anywhere.

func TestNewMoneyAcceptsPositiveKES(t *testing.T) {
	m, err := NewMoney(12345, CurrencyKES)
	require.NoError(t, err)
	assert.Equal(t, int64(12345), m.AmountMinor)
	assert.Equal(t, CurrencyKES, m.Currency)
}

func TestNewMoneyRefusesZeroNegativeAndForeignCurrency(t *testing.T) {
	for _, tc := range []struct {
		name        string
		amountMinor int64
		currency    string
	}{
		{"zero amount", 0, CurrencyKES},
		{"negative amount", -1, CurrencyKES},
		{"min int64", math.MinInt64, CurrencyKES},
		{"unknown currency", 100, "USD"},
		{"empty currency", 100, ""},
		{"lowercase currency", 100, "kes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewMoney(tc.amountMinor, tc.currency)
			require.Error(t, err)
		})
	}
}

func TestAddHappyPath(t *testing.T) {
	a, _ := NewMoney(1_000, CurrencyKES)
	b, _ := NewMoney(250, CurrencyKES)
	sum, err := Add(a, b)
	require.NoError(t, err)
	assert.Equal(t, int64(1_250), sum.AmountMinor)
	assert.Equal(t, CurrencyKES, sum.Currency)
}

func TestAddRefusesOverflow(t *testing.T) {
	max, err := NewMoney(math.MaxInt64, CurrencyKES)
	require.NoError(t, err)

	one, err := NewMoney(1, CurrencyKES)
	require.NoError(t, err)

	// max + 1 must error, never wrap around to a negative.
	_, err = Add(max, one)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "overflows")

	// max + max must error as well.
	_, err = Add(max, max)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "overflows")

	// The boundary itself must succeed: (max - 1) + 1 == max.
	maxMinusOne, err := NewMoney(math.MaxInt64-1, CurrencyKES)
	require.NoError(t, err)
	sum, err := Add(maxMinusOne, one)
	require.NoError(t, err)
	assert.Equal(t, int64(math.MaxInt64), sum.AmountMinor)
}

func TestAddRefusesInvalidAndCrossCurrencyOperands(t *testing.T) {
	valid, err := NewMoney(100, CurrencyKES)
	require.NoError(t, err)

	zero := Money{AmountMinor: 0, Currency: CurrencyKES}
	negative := Money{AmountMinor: -5, Currency: CurrencyKES}
	usd := Money{AmountMinor: 100, Currency: "USD"}

	_, err = Add(zero, valid)
	require.Error(t, err)
	_, err = Add(valid, zero)
	require.Error(t, err)
	_, err = Add(negative, valid)
	require.Error(t, err)
	_, err = Add(valid, negative)
	require.Error(t, err)
	// A foreign currency is refused (by operand validation while KES is
	// the only supported currency; the cross-currency guard inside Add
	// remains as defense in depth for future currencies).
	_, err = Add(valid, usd)
	require.Error(t, err)
}

func TestSubtractHappyPath(t *testing.T) {
	a, _ := NewMoney(1_000, CurrencyKES)
	b, _ := NewMoney(250, CurrencyKES)
	diff, err := Subtract(a, b)
	require.NoError(t, err)
	assert.Equal(t, int64(750), diff.AmountMinor)
}

func TestSubtractRefusesNegativeAndZeroResults(t *testing.T) {
	a, _ := NewMoney(100, CurrencyKES)
	b, _ := NewMoney(250, CurrencyKES)

	// 100 - 250 would go negative.
	_, err := Subtract(a, b)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "negative")

	// 100 - 100 would be zero; the ledger has no zero amounts.
	_, err = Subtract(a, a)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "zero")

	// Invalid operands are refused before the arithmetic.
	_, err = Subtract(a, Money{AmountMinor: 1, Currency: "USD"})
	require.Error(t, err)
}

func TestEqualSameCurrencyIsExact(t *testing.T) {
	a, _ := NewMoney(12_345, CurrencyKES)
	b, _ := NewMoney(12_345, CurrencyKES)
	c, _ := NewMoney(12_346, CurrencyKES)
	assert.True(t, EqualSameCurrency(a, b))
	assert.False(t, EqualSameCurrency(a, c))
	assert.False(t, EqualSameCurrency(a, Money{AmountMinor: 12_345, Currency: "USD"}))
}

func TestMoneyStringRendersMinorUnitsWithoutFloats(t *testing.T) {
	cases := []struct {
		minor int64
		want  string
	}{
		{1, "KES 0.01"},
		{5, "KES 0.05"},
		{99, "KES 0.99"},
		{100, "KES 1.00"},
		{12_345, "KES 123.45"},
		{1_000_000_000, "KES 10000000.00"},
	}
	for _, tc := range cases {
		m, err := NewMoney(tc.minor, CurrencyKES)
		require.NoError(t, err)
		assert.Equal(t, tc.want, m.String())
	}
}
