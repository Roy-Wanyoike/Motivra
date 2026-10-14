package platform

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedactFreeTextMasksEmails(t *testing.T) {
	t.Parallel()
	out := RedactFreeText("Roy's iPhone (roy.wanyoike@example.com)")
	require.Contains(t, out, "***@redacted@example.com")
	require.NotContains(t, out, "roy.wanyoike")
}

func TestRedactFreeTextMasksPhoneNumbers(t *testing.T) {
	t.Parallel()
	out := RedactFreeText("device +254 700 123 456 registered")
	require.Contains(t, out, "***redacted-phone***")
	require.NotContains(t, out, "254 700 123 456")
}

func TestRedactFreeTextStripsControlCharsAndTruncates(t *testing.T) {
	t.Parallel()
	out := RedactFreeText("dev\x00\x1fname\n\t  with   spaces")
	require.Equal(t, "dev name with spaces", out)

	long := strings.Repeat("x", 500)
	require.Len(t, RedactFreeText(long), MaxRedactedFieldLen)
}

func TestRedactFreeTextKeepsBenignDeviceNames(t *testing.T) {
	t.Parallel()
	require.Equal(t, "pixel-8", RedactFreeText("pixel-8"))
	require.Equal(t, "Tecno Spark 20", RedactFreeText("Tecno Spark 20"))
}

func TestRedactEmailMasksLocalPart(t *testing.T) {
	t.Parallel()
	require.Equal(t, "ro***@redacted@example.com", RedactEmail("roy.wanyoike@example.com"))
	require.Equal(t, "***@redacted", RedactEmail("not-an-email"))
	require.Equal(t, "***@redacted@x.co", RedactEmail("ab@x.co")) // 2-char local part masks fully
}
