//go:build testing

package hub

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTheAuditTrailCountsTheLinesItDrops(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fan-audit.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(
		`{"op":"set_level","outcome":"applied","level":40,"at":"2026-10-16T00:00:00Z"}`+"\n"+
			`{"op": this line is not a record}`+"\n"), 0o600))

	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(old) })

	trail := newFanAudit(path)
	items := trail.entries()
	require.Len(t, items, 1, "one unreadable line must not cost the good line before it")
	assert.Equal(t, "set_level", items[0]["op"])

	logs := buf.String()
	require.Contains(t, logs, "fan audit line skipped",
		"the drop itself must still say what was skipped: %s", logs)
	assert.Contains(t, logs, "count=1",
		"a dropped line must be counted in the log, not just warned per line: %s", logs)
}
