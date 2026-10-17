//go:build testing

package hub_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func swallowSlogTo(t *testing.T, buf *bytes.Buffer) {
	t.Helper()
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(old) })
}

func TestParseFailuresOnTheFanRoutesSaySoBeforeTheyRefuse(t *testing.T) {
	h := newFanHarness(t)
	var buf bytes.Buffer
	swallowSlogTo(t, &buf)

	rec := h.do(t, "admin", http.MethodPost, "/api/plus/fan/level",
		map[string]any{"level": 70, "confirm": true, "not_a_field": 1})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	rec2 := h.do(t, "admin", http.MethodPost, "/api/plus/fan/curve", "][")
	require.Equal(t, http.StatusBadRequest, rec2.Code, rec2.Body.String())

	logs := buf.String()
	assert.Contains(t, logs, "fan request body would not parse",
		"a body the level route cannot read must be logged, not just refused")
	assert.Contains(t, logs, "fan curve request body would not parse",
		"a body the curve route cannot read must be logged as itself, and the refusal code the route already owes")
}
