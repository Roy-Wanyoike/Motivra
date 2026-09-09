package vehicles

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// cursorVersion prefixes the opaque cursor payload so a future format change
// can reject (rather than misread) old cursors.
const cursorVersion = "v1"

// cursorSeparator joins the cursor payload fields. Neither RFC 3339 nor UUID
// representations can contain it.
const cursorSeparator = "|"

// VehicleCursor is a keyset pagination position over the vehicles registry:
// the (created_at, id) pair of the last row already returned. Ordering is
// created_at DESC, id DESC, so the next page is strictly "less than" the
// cursor on that pair.
type VehicleCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// EncodeVehicleCursor renders c as an opaque, URL-safe string handed to
// clients as next_cursor.
func EncodeVehicleCursor(c VehicleCursor) (string, error) {
	if c.ID == uuid.Nil {
		return "", fmt.Errorf("vehicles: encode cursor: cursor id is required")
	}
	payload := strings.Join([]string{
		cursorVersion,
		c.CreatedAt.UTC().Format(time.RFC3339Nano),
		c.ID.String(),
	}, cursorSeparator)
	return base64.RawURLEncoding.EncodeToString([]byte(payload)), nil
}

// ParseVehicleCursor decodes an opaque cursor produced by
// EncodeVehicleCursor. Anything malformed — including cursors from a future
// format version — is a validation failure, never a partial read.
func ParseVehicleCursor(raw string) (VehicleCursor, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return VehicleCursor{}, platform.ErrValidation("cursor is not valid",
			platform.FieldError{Field: "cursor", Issue: "must be the opaque cursor from a previous response"})
	}
	parts := strings.SplitN(string(decoded), cursorSeparator, 3)
	if len(parts) != 3 || parts[0] != cursorVersion {
		return VehicleCursor{}, platform.ErrValidation("cursor is not valid",
			platform.FieldError{Field: "cursor", Issue: "must be the opaque cursor from a previous response"})
	}
	createdAt, err := time.Parse(time.RFC3339Nano, parts[1])
	if err != nil {
		return VehicleCursor{}, platform.ErrValidation("cursor is not valid",
			platform.FieldError{Field: "cursor", Issue: "must be the opaque cursor from a previous response"})
	}
	id, err := uuid.Parse(parts[2])
	if err != nil || id == uuid.Nil {
		return VehicleCursor{}, platform.ErrValidation("cursor is not valid",
			platform.FieldError{Field: "cursor", Issue: "must be the opaque cursor from a previous response"})
	}
	return VehicleCursor{CreatedAt: createdAt.UTC(), ID: id}, nil
}
