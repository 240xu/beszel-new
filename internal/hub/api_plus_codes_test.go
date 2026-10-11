//go:build testing

package hub_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/henrygd/beszel/internal/fanapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fanapi's closed set in lookup form: a code outside it cannot be read by plus-refusals.ts,
// so it reaches a panel as "this build does not know".
//
// Derived rather than written out: a hand-kept copy was checked only for codes these routes
// happen to emit, so an entry nothing triggered was never read. The mirror test watches it.
var fanWireCodes = codeSet(fanapi.Codes())

func codeSet(codes []string) map[string]bool {
	set := make(map[string]bool, len(codes))
	for _, c := range codes {
		set[c] = true
	}
	return set
}

// postRaw issues a body verbatim, so the paths a JSON encoder cannot reach —
// an oversized body, a truncated document, a form post — can be exercised.
func (h *fanHarness) postRaw(t *testing.T, path, raw string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(raw))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("Authorization", h.tokens["admin"])
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

// refusalCode reads data.code and reports whether one was there at all. "No code"
// and "an empty code" are different answers, and only the second is a bug.
func refusalCode(t *testing.T, rec *httptest.ResponseRecorder) (string, bool) {
	t.Helper()
	body := decodeBody(t, rec)
	data, ok := body["data"].(map[string]any)
	if !ok {
		return "", false
	}
	code, ok := data["code"].(string)
	return code, ok
}

type refusalCase struct {
	name string
	// setup makes the route meet the condition; nil leaves the rig as built.
	setup func(*fanRig)
	// path and raw are the request. An empty raw uses `body`, JSON-encoded.
	path string
	body any
	raw  string
	// wantStatus and wantCode are the whole wire answer. wantCode "" asserts the
	// refusal arrives with no code at all, which is itself the assertion.
	wantStatus int
	wantCode   string
}

// TestFanRefusalsEitherCarryACodeOrAreNamedAsCodeLess walks every refusal the five
// routes can produce and pins each one's code.
//
// The point is the shape of the table, not any one row. A refusal with no code is
// answered by a panel that can only say the server named no reason, which is true;
// a refusal whose code misdescribes it sends the operator after the wrong problem.
// So: every row is either a code from the closed set, or a row named here as
// deliberately code-less — and a new code-less refusal added to the routes without
// a row to match fails this test rather than quietly reaching a panel.
func TestFanRefusalsEitherCarryACodeOrAreNamedAsCodeLess(t *testing.T) {
	cases := []refusalCase{
		// ---- deliberately code-less: a body no published code describes ----
		{"level body is not JSON", nil, "/api/plus/fan/level", nil, `{"level":70,"confirm":true`, http.StatusBadRequest, ""},
		{"level body carries an unknown field", nil, "/api/plus/fan/level", nil,
			`{"level":70,"confirm":true,"levl":70}`, http.StatusBadRequest, ""},
		{"level body has trailing content", nil, "/api/plus/fan/level", nil,
			`{"level":70,"confirm":true}{"level":25}`, http.StatusBadRequest, ""},
		{"level is absent", nil, "/api/plus/fan/level", nil,
			`{"confirm":true}`, http.StatusBadRequest, ""},
		{"mode body is not JSON", nil, "/api/plus/fan/mode", nil, `{"mode":"auto","confirm":true`, http.StatusBadRequest, ""},
		{"mode is not auto or manual", nil, "/api/plus/fan/mode", map[string]any{"mode": "restart", "confirm": true}, "",
			http.StatusBadRequest, ""},

		// ---- the confirmation gate ----
		{"level is unconfirmed", nil, "/api/plus/fan/level", map[string]any{"level": 70}, "",
			http.StatusBadRequest, "confirm_required"},
		{"mode is unconfirmed", nil, "/api/plus/fan/mode", map[string]any{"mode": "auto"}, "",
			http.StatusBadRequest, "confirm_required"},
		{"curve is unconfirmed", nil, "/api/plus/fan/curve", withConfirm(curveBody(), false), "",
			http.StatusBadRequest, "confirm_required"},

		// ---- a curve the route refused to read is a curve it rejected ----
		{"curve body is not JSON", nil, "/api/plus/fan/curve", nil, `{"idle":50,`, http.StatusBadRequest, "curve_rejected"},
		{"curve carries an unknown field", nil, "/api/plus/fan/curve", nil,
			`{"idle":50,"l0":25,"t1":56,"l1":30,"t2":62,"l2":40,"t3":67,"l3":50,"t4":72,"l4":60,"t5":79,"l5":70,"emerg":80,"e":100,"emerg_rel":75,"cap":70,"trim":6,"gain":1,"hyst":3,"dwell":30,"dead":2,"iv":10,"t6":85,"confirm":true}`,
			http.StatusBadRequest, "curve_rejected"},
		{"a curve field is missing", nil, "/api/plus/fan/curve", withoutCurveField("gain"), "",
			http.StatusBadRequest, "curve_rejected"},
		{"the curve the host would not run", func(r *fanRig) {}, "/api/plus/fan/curve",
			withCurveValue("t2", 40), "", http.StatusBadRequest, "curve_rejected"},

		// ---- the guard, which named its refusals already ----
		{"a duty off the whitelist", nil, "/api/plus/fan/level",
			map[string]any{"level": 33, "confirm": true}, "", http.StatusBadRequest, "level_not_allowed"},
		{"the daemon owns the fan", func(r *fanRig) { r.pfandUp = true }, "/api/plus/fan/level",
			map[string]any{"level": 70, "confirm": true}, "", http.StatusConflict, "pfand_active"},
		{"at the emergency edge", func(r *fanRig) { r.temp = 85 }, "/api/plus/fan/level",
			map[string]any{"level": 70, "confirm": true}, "", http.StatusServiceUnavailable, "package_too_hot"},
		{"the sensor cannot be read", func(r *fanRig) { r.tempErr = errors.New("gone") }, "/api/plus/fan/level",
			map[string]any{"level": 70, "confirm": true}, "", http.StatusServiceUnavailable, "temperature_unknown"},
		{"the device refused the program", func(r *fanRig) { r.setErr = errors.New("busy") }, "/api/plus/fan/level",
			map[string]any{"level": 70, "confirm": true}, "", http.StatusInternalServerError, "write_failed"},
		{"the duty could not be read back first", func(r *fanRig) { r.getErr = errors.New("busy") }, "/api/plus/fan/level",
			map[string]any{"level": 70, "confirm": true}, "", http.StatusInternalServerError, "internal"},

		// ---- the daemon restart, the outlet ruling 2 depends on ----
		{"the daemon would not stop", func(r *fanRig) { r.pfandFail = errors.New("unit not loaded") },
			"/api/plus/fan/mode", map[string]any{"mode": "manual", "confirm": true}, "",
			http.StatusInternalServerError, "internal"},
		{"the daemon would not start", func(r *fanRig) { r.pfandFail = errors.New("job failed") },
			"/api/plus/fan/mode", map[string]any{"mode": "auto", "confirm": true}, "",
			http.StatusInternalServerError, "internal"},
	}

	// Named up front, so the assertion at the end is a comparison rather than a
	// count: a route that grows a new code-less refusal makes the two sets differ.
	wantNoCode := map[string]bool{}
	for _, tc := range cases {
		if tc.wantCode == "" {
			wantNoCode[tc.name] = true
		}
	}
	gotNoCode := map[string]bool{}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newFanHarness(t)
			if tc.setup != nil {
				tc.setup(h.rig)
			}
			var rec *httptest.ResponseRecorder
			switch {
			case tc.raw != "":
				rec = h.postRaw(t, tc.path, tc.raw)
			case tc.body != nil:
				rec = h.do(t, "admin", http.MethodPost, tc.path, tc.body)
			default:
				rec = h.do(t, "admin", http.MethodPost, tc.path, nil)
			}
			require.Equal(t, tc.wantStatus, rec.Code, rec.Body.String())

			code, present := refusalCode(t, rec)
			if tc.wantCode == "" {
				assert.False(t, present,
					"this path is named here as code-less; a code added to it is fine, an absent one is not")
				assert.NotEmpty(t, decodeBody(t, rec)["message"],
					"a refusal with no code still has to say what it refused")
				gotNoCode[tc.name] = true
				return
			}
			assert.True(t, present, "refusal carried no code: %s", rec.Body.String())
			assert.Equal(t, tc.wantCode, code)
			assert.True(t, fanWireCodes[code],
				"%q is not in the closed set plus-refusals.ts reads, so a panel cannot branch on it", code)
			assert.False(t, gotNoCode[tc.name], "this path is named as coded and arrived without a code")
		})
	}
	assert.Equal(t, wantNoCode, gotNoCode,
		"the set of refusals that reach a panel with nothing to branch on changed")
}

// TestTheRestartOutletIsNotACodeLessRefusal: ruling 2's outlet is the restart
// button, and a restart that fails is the one refusal on this surface that
// happens after the daemon has been asked to act. With no code it is a panel's
// "the server named no reason", which sends the operator looking for a request
// that was never read rather than for a systemd job that failed.
func TestTheRestartOutletIsNotACodeLessRefusal(t *testing.T) {
	h := newFanHarness(t)
	h.rig.pfandFail = errors.New("Job pfand.service/start failed with result 'timeout'")

	rec := h.do(t, "admin", http.MethodPost, "/api/plus/fan/mode",
		map[string]any{"mode": "auto", "confirm": true})
	require.Equal(t, http.StatusInternalServerError, rec.Code)

	code, present := refusalCode(t, rec)
	assert.True(t, present, "the daemon was asked to act, so the refusal must say what happened")
	assert.Equal(t, "internal", code)
	assert.Contains(t, fanWireCodes, code)
	// The sealed fact is the reason the refusal must not describe itself as a
	// request that never arrived: systemctl did run.
	assert.Equal(t, []string{"start"}, h.rig.actions)
}

// TestACodeLessRefusalSaysNothingAboutTheFan is the backend half of the split
// the panel depends on. Every refusal without a code is one the routes raised
// before they classified anything, and none of them may claim a status the wire
// does not support.
func TestACodeLessRefusalSaysNothingAboutTheFan(t *testing.T) {
	h := newFanHarness(t)
	for _, raw := range []string{`{"confirm":true}`, `{"mode":"restart","confirm":true}`, `not json`} {
		path := "/api/plus/fan/level"
		if strings.Contains(raw, "mode") {
			path = "/api/plus/fan/mode"
		}
		rec := h.postRaw(t, path, raw)
		require.Equal(t, http.StatusBadRequest, rec.Code, raw)
		_, present := refusalCode(t, rec)
		assert.False(t, present, "%s", raw)
		body := decodeBody(t, rec)
		assert.NotEmpty(t, body["message"])
		assert.NotContains(t, body, "code")
	}
}

// helpers for the two curve shapes the table needs.
func withoutCurveField(key string) map[string]any {
	body := curveBody()
	delete(body, key)
	return body
}

func withCurveValue(key string, val int) map[string]any {
	body := curveBody()
	body[key] = val
	return body
}
