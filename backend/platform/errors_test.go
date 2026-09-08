package platform

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestErrorStatusCodes(t *testing.T) {
	tests := []struct {
		err    *Error
		status int
		code   string
	}{
		{ErrValidation("bad"), 422, "validation_failed"},
		{ErrNotFound("gone"), 404, "not_found"},
		{ErrUnauthorized("who"), 401, "unauthorized"},
		{ErrForbidden("no"), 403, "forbidden"},
		{ErrConflict("clash"), 409, "conflict"},
		{ErrInternal(), 500, "internal_error"},
	}
	for _, tc := range tests {
		require.Equal(t, tc.status, tc.err.Status)
		require.Equal(t, tc.code, tc.err.Code)
		require.NotEmpty(t, tc.err.Title)
	}
}

func TestWriteErrorProblemJSON(t *testing.T) {
	w := httptest.NewRecorder()
	WriteError(w, ErrValidation("bad input", FieldError{Field: "email", Issue: "required"}))

	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	require.Equal(t, "application/problem+json", w.Header().Get("Content-Type"))

	body := w.Body.String()
	require.Contains(t, body, `"code":"validation_failed"`)
	require.Contains(t, body, `"field":"email"`)
}

func TestWriteErrorWrapsUnknownErrors(t *testing.T) {
	w := httptest.NewRecorder()
	WriteError(w, errors.New("database password leaked secret-value"))
	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.NotContains(t, w.Body.String(), "secret-value")
}

func TestAsError(t *testing.T) {
	require.Nil(t, AsError(nil))
	appErr := ErrConflict("x")
	require.Same(t, appErr, AsError(appErr))
	require.Equal(t, ErrInternal().Status, AsError(errors.New("boom")).Status)
}

func TestDecodeJSON(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}

	t.Run("valid", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"motivra"}`))
		got, err := DecodeJSON[payload](r, 1<<20)
		require.NoError(t, err)
		require.Equal(t, "motivra", got.Name)
	})

	t.Run("invalid json", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{broken`))
		_, err := DecodeJSON[payload](r, 1<<20)
		var appErr *Error
		require.True(t, errors.As(err, &appErr))
		require.Equal(t, http.StatusUnprocessableEntity, appErr.Status)
	})

	t.Run("over limit", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"`+strings.Repeat("x", 64)+`"}`))
		_, err := DecodeJSON[payload](r, 8)
		require.Error(t, err)
	})
}
