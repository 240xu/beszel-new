//go:build testing

package hub_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/henrygd/beszel/agent/plus"
	"github.com/henrygd/beszel/internal/fanapi"
	"github.com/henrygd/beszel/internal/hub"
	beszelTests "github.com/henrygd/beszel/internal/tests"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fanRig is the whole fan, faked. Every seam the routes touch is here, so a test
// can assert what reached the device and what did not.
type fanRig struct {
	mu sync.Mutex

	level int
	// setErr fails the write and getErr the read, kept apart because the guard
	// meets them at different points and reports them differently: a read that
	// fails before the write never reaches the device at all.
	setErr error
	getErr error
	// loseTo is what a read-back reports once a write has landed. The device has
	// no way to say "your write lost", so a read-back that disagrees with the
	// request is the only evidence there is.
	loseTo    *int
	temp      int
	tempErr   error
	curve     []byte
	curveErr  error
	pfandUp   bool
	pfandFail error

	setCalls []int
	actions  []string
}

func (r *fanRig) readTempC() (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.temp, r.tempErr
}

func (r *fanRig) readCurve() (*plus.Curve, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.curveErr != nil {
		return nil, r.curveErr
	}
	return plus.ParseCurve(string(r.curve))
}

func (r *fanRig) setLevel(level int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setCalls = append(r.setCalls, level)
	if r.setErr != nil {
		return r.setErr
	}
	r.level = level
	return nil
}

func (r *fanRig) getLevel() (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.getErr != nil {
		return 0, r.getErr
	}
	if r.loseTo != nil && len(r.setCalls) > 0 {
		return *r.loseTo, nil
	}
	return r.level, nil
}

func (r *fanRig) pfand() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pfandUp
}

func (r *fanRig) readCurveBytes() ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.curveErr != nil {
		return nil, r.curveErr
	}
	return append([]byte(nil), r.curve...), nil
}

func (r *fanRig) writeCurveBytes(raw []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.curveErr != nil {
		return r.curveErr
	}
	r.curve = append([]byte(nil), raw...)
	return nil
}

func (r *fanRig) pfandAction(action string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.actions = append(r.actions, action)
	return r.pfandFail
}

// deps is the rig as a Deps builder, so the routes talk to the rig and to
// whatever audit sink the hub hands in.
func (r *fanRig) deps(audit func(fanapi.Event)) fanapi.Deps {
	if audit == nil {
		audit = func(fanapi.Event) {}
	}
	return fanapi.Deps{
		Now:             time.Now,
		ReadTempC:       r.readTempC,
		ReadCurve:       r.readCurve,
		SetLevel:        r.setLevel,
		GetLevel:        r.getLevel,
		PfandUp:         r.pfand,
		ReadCurveBytes:  r.readCurveBytes,
		WriteCurveBytes: r.writeCurveBytes,
		Audit:           audit,
	}
}

func (r *fanRig) writes() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.setCalls...)
}

// TestCurveSource is the curve this host runs, in the shape the wire uses.
const testCurveSource = `IDLE=50
L0=25
T1=56
L1=30
T2=62
L2=40
T3=67
L3=50
T4=72
L4=60
T5=79
L5=70
EMERG=80
E=100
EMERG_REL=75
CAP=70
TRIM=6
GAIN=1
HYST=3
DWELL=30
DEAD=2
IV=10
`

// curveBody is the wire form of the curve above, plus a confirmation.
func curveBody() map[string]any {
	return map[string]any{
		"idle": 50, "l0": 25,
		"t1": 56, "l1": 30,
		"t2": 62, "l2": 40,
		"t3": 67, "l3": 50,
		"t4": 72, "l4": 60,
		"t5": 79, "l5": 70,
		"emerg": 80, "e": 100, "emerg_rel": 75,
		"cap": 70, "trim": 6, "gain": 1,
		"hyst": 3, "dwell": 30, "dead": 2, "iv": 10,
		"confirm": true,
	}
}

// ---- the fan rig and its harness ----

type fanHarness struct {
	hub     *beszelTests.TestHub
	handler http.Handler
	rig     *fanRig
	tokens  map[string]string
}

// newFanHarness builds a hub whose fan routes are faked. The seam is replaced
// before the serve event fires, so the routes these tests exercise are the same
// handlers production registers, not a re-implementation.
// seamMu serialises the harness. fanDeps and pfandAction are package-level, so
// two harnesses alive at once would hand each other's requests to each other's
// fake. t.Parallel is not used anywhere in this file for the same reason.
var seamMu sync.Mutex

func newFanHarness(t *testing.T) *fanHarness {
	t.Helper()
	seamMu.Lock()
	t.Cleanup(seamMu.Unlock)
	// GetHubWithUser is the path the hub's own api tests use; it initialises the
	// system manager and runs migrations, which is what makes the users
	// collection addressable below.
	testHub, _ := beszelTests.GetHubWithUser(t)
	t.Cleanup(testHub.Cleanup)

	rig := &fanRig{level: 25, temp: 46, curve: []byte(testCurveSource)}

	admin, err := beszelTests.CreateUserWithRole(testHub, "admin@example.com", "password123", "admin")
	require.NoError(t, err)
	adminToken, err := admin.NewAuthToken()
	require.NoError(t, err)

	plain, err := beszelTests.CreateUser(testHub, "plain@example.com", "password123")
	require.NoError(t, err)
	plainToken, err := plain.NewAuthToken()
	require.NoError(t, err)

	readonly, err := beszelTests.CreateUserWithRole(testHub, "readonly@example.com", "password123", "readonly")
	require.NoError(t, err)
	readonlyToken, err := readonly.NewAuthToken()
	require.NoError(t, err)

	h := &fanHarness{hub: testHub, rig: rig, tokens: map[string]string{
		"admin": adminToken, "plain": plainToken, "readonly": readonlyToken,
	}}

	restore := hub.SetFanSeams(rig.deps, rig.pfandAction)
	t.Cleanup(restore)

	router, err := apis.NewRouter(testHub.TestApp)
	require.NoError(t, err)
	serveEvent := &core.ServeEvent{App: testHub.TestApp, Router: router}
	err = testHub.TestApp.OnServe().Trigger(serveEvent, func(e *core.ServeEvent) error {
		var buildErr error
		h.handler, buildErr = e.Router.BuildMux()
		return buildErr
	})
	require.NoError(t, err)
	require.NotNil(t, h.handler)
	return h
}

// do issues a request as one of the named roles, or anonymously.
func (h *fanHarness) do(t *testing.T, role, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		reader = strings.NewReader(string(raw))
	} else if method == http.MethodPost {
		reader = strings.NewReader("{}")
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("content-type", "application/json")
	if token, ok := h.tokens[role]; ok && role != "" {
		req.Header.Set("Authorization", token)
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

// faultCode reads the refusal code out of a PocketBase error body, where it
// sits under "data" because that is the field ApiError serialises.
func faultCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	data, ok := decodeBody(t, rec)["data"].(map[string]any)
	require.True(t, ok, "a refusal must carry a code, and it lives under data: %s", rec.Body.String())
	code, _ := data["code"].(string)
	return code
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), "body: %s", rec.Body.String())
	return out
}

// ---- the auth matrix, per endpoint ----

// TestPlusFanRoutesRefuseEveryoneButAnAdmin is the matrix this task exists to
// get right. Five routes, four callers: anonymous, a plain user, a readonly
// user and an admin. Every route is read or write; none of them may answer
// anyone but an admin.
func TestPlusFanRoutesRefuseEveryoneButAnAdmin(t *testing.T) {
	for _, ep := range []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"state", http.MethodGet, "/api/plus/fan/state", nil},
		{"audit", http.MethodGet, "/api/plus/fan/audit", nil},
		{"level", http.MethodPost, "/api/plus/fan/level", map[string]any{"level": 70, "confirm": true}},
		{"mode", http.MethodPost, "/api/plus/fan/mode", map[string]any{"mode": "manual", "confirm": true}},
		{"curve", http.MethodPost, "/api/plus/fan/curve", curveBody()},
	} {
		for _, role := range []string{"", "plain", "readonly"} {
			t.Run(fmt.Sprintf("%s as %s", ep.name, orAnonymous(role)), func(t *testing.T) {
				h := newFanHarness(t)
				rec := h.do(t, role, ep.method, ep.path, ep.body)

				want := http.StatusUnauthorized
				if role != "" {
					want = http.StatusForbidden
				}
				assert.Equal(t, want, rec.Code,
					"a caller who is not an admin must not reach this route")
				assert.Empty(t, h.rig.writes(),
					"a refused caller must not reach the device, whatever the route was")
				assert.Empty(t, h.rig.actions,
					"a refused caller must not be able to stop or start the curve daemon")
			})
		}
	}
}

func orAnonymous(role string) string {
	if role == "" {
		return "anonymous"
	}
	return role
}

// TestPlusFanWriteRoutesRefuseASuperuser pins that the check is on the admin
// role rather than on being logged in. A superuser is not an admin record, and
// the fan is not part of what a superuser is for.
func TestPlusFanWriteRoutesRefuseASuperuser(t *testing.T) {
	h := newFanHarness(t)
	super, err := beszelTests.CreateSuperuser(h.hub, "su@example.com", "password123")
	require.NoError(t, err)
	token, err := super.NewAuthToken()
	require.NoError(t, err)
	h.tokens["superuser"] = token

	rec := h.do(t, "superuser", http.MethodPost, "/api/plus/fan/level",
		map[string]any{"level": 70, "confirm": true})
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Empty(t, h.rig.writes())
}

// TestPlusFanRoutesRejectAPathThatIsNotOneOfThem stops a typo in a panel from
// being answered by the SPA catch-all with a 200 and an HTML page.
func TestPlusFanRoutesRejectAPathThatIsNotOneOfThem(t *testing.T) {
	h := newFanHarness(t)
	for _, path := range []string{
		"/api/plus/fan", "/api/plus/fan/", "/api/plus/fan/levels",
		"/api/plus/fan/level/70", "/api/plus/fan/regress",
	} {
		rec := h.do(t, "admin", http.MethodGet, path, nil)
		assert.NotEqual(t, http.StatusOK, rec.Code, "%s is not one of the five routes", path)
	}
}

// ---- ruling 1: a curve on disk is not a curve in force ----

// TestCurveWriteReportsPendingWhilePfandHoldsTheFan is ruling 1 on the wire. The
// daemon sources its configuration once at start, so a curve written under a
// running daemon is on disk and nowhere else.
func TestCurveWriteReportsPendingWhilePfandHoldsTheFan(t *testing.T) {
	h := newFanHarness(t)
	h.rig.pfandUp = true

	rec := h.do(t, "admin", http.MethodPost, "/api/plus/fan/curve", curveBody())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	body := decodeBody(t, rec)
	assert.Equal(t, false, body["applied"],
		"a curve no daemon has read is not in force, and applied=true is what renders a save that has not happened")
	assert.Equal(t, true, body["needs_pfand_restart"])
	assert.Equal(t, "pending_restart", body["status"])
	assert.Equal(t, "restart_pfand", body["next_action"],
		"ruling 2: a pending change with no way to finish makes the operator press save repeatedly")
}

// TestCurveWriteReportsAppliedWhenNoDaemonIsRunning is the other half: the same
// write with nothing to reload is genuinely in force, so the panel must be able
// to say so. Folding applied away unconditionally would be its own lie.
func TestCurveWriteReportsAppliedWhenNoDaemonIsRunning(t *testing.T) {
	h := newFanHarness(t)
	h.rig.pfandUp = false

	rec := h.do(t, "admin", http.MethodPost, "/api/plus/fan/curve", curveBody())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	body := decodeBody(t, rec)
	assert.Equal(t, true, body["applied"])
	assert.Equal(t, false, body["needs_pfand_restart"])
	assert.Equal(t, "applied", body["status"])
}

// TestPendingResponseNeverClaimsTheCurveIsSaved is ruling 3, asserted over the
// bytes rather than over a field: a name is as capable of claiming a save as a
// status string is.
func TestPendingResponseNeverClaimsTheCurveIsSaved(t *testing.T) {
	h := newFanHarness(t)
	h.rig.pfandUp = true

	rec := h.do(t, "admin", http.MethodPost, "/api/plus/fan/curve", curveBody())
	require.Equal(t, http.StatusOK, rec.Code)
	for _, claim := range []string{"saved", "in force", "生效", "已保存"} {
		assert.NotContains(t, rec.Body.String(), claim)
	}
}

// ---- the pfand restart outlet ----

// TestAutoModeIsTheRestartOutlet is ruling 2's outlet: the only way to make a
// curve that reached the disk become behaviour is to start the daemon again, and
// this is where an operator goes to do it.
func TestAutoModeIsTheRestartOutlet(t *testing.T) {
	h := newFanHarness(t)

	rec := h.do(t, "admin", http.MethodPost, "/api/plus/fan/mode",
		map[string]any{"mode": "auto", "confirm": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []string{"start"}, h.rig.actions)

	rec = h.do(t, "admin", http.MethodPost, "/api/plus/fan/mode",
		map[string]any{"mode": "manual", "confirm": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []string{"start", "stop"}, h.rig.actions)
}

func TestModeRejectsAModeThatIsNotAutoOrManual(t *testing.T) {
	h := newFanHarness(t)
	for _, mode := range []string{"off", "", "AUTO", "restart"} {
		rec := h.do(t, "admin", http.MethodPost, "/api/plus/fan/mode",
			map[string]any{"mode": mode, "confirm": true})
		assert.Equal(t, http.StatusBadRequest, rec.Code, "mode %q", mode)
	}
	assert.Empty(t, h.rig.actions, "an unrecognised mode must not be turned into a systemctl argument")
}

// TestModeReportsAFailedDaemonChangeWithoutItsOutput: systemctl prints unit
// names and a job id, and neither belongs in a response body.
func TestModeReportsAFailedDaemonChangeWithoutItsOutput(t *testing.T) {
	h := newFanHarness(t)
	h.rig.pfandFail = errors.New("Job pfand.service/start failed with result 'timeout'")

	rec := h.do(t, "admin", http.MethodPost, "/api/plus/fan/mode",
		map[string]any{"mode": "auto", "confirm": true})
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), "pfand.service")
	assert.NotContains(t, rec.Body.String(), "timeout")
}

// ---- confirmation ----

// TestEveryWriteRouteInsistsOnConfirm: a write that changes how a machine cools
// must arrive confirmed. The check is here as well as in the guard so that an
// unconfirmed request is answered without anything being attempted.
func TestEveryWriteRouteInsistsOnConfirm(t *testing.T) {
	for _, ep := range []struct {
		name string
		path string
		body map[string]any
	}{
		{"level", "/api/plus/fan/level", map[string]any{"level": 70}},
		{"mode", "/api/plus/fan/mode", map[string]any{"mode": "manual"}},
		{"curve", "/api/plus/fan/curve", withConfirm(curveBody(), false)},
	} {
		t.Run(ep.name, func(t *testing.T) {
			h := newFanHarness(t)
			rec := h.do(t, "admin", http.MethodPost, ep.path, ep.body)
			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Contains(t, strings.ToLower(rec.Body.String()), "confirm is required",
				"PocketBase capitalises the message, so the claim is matched case-insensitively")
			assert.Empty(t, h.rig.writes(), "an unconfirmed request must not reach the device")
			assert.Empty(t, h.rig.actions, "an unconfirmed request must not touch the daemon")
			assert.Equal(t, string(h.rig.curve), testCurveSource,
				"an unconfirmed curve write must not have written anything")
		})
	}
}

func withConfirm(body map[string]any, confirm bool) map[string]any {
	out := map[string]any{}
	for k, v := range body {
		out[k] = v
	}
	out["confirm"] = confirm
	return out
}

// ---- refusals on the wire ----

// TestLevelRefusalsAreDistinguishable walks the sentinels a level write can
// meet and checks that each arrives with its own status and its own code. Two
// refusals sharing a code is the failure mode that makes a panel pick the wrong
// remedy, so the codes are asserted one by one rather than by count.
func TestLevelRefusalsAreDistinguishable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(*fanRig)
		level  int
		status int
		code   string
	}{
		{"duty off the firmware whitelist", func(r *fanRig) {}, 33, http.StatusBadRequest, "level_not_allowed"},
		{"the daemon owns the fan", func(r *fanRig) { r.pfandUp = true }, 70, http.StatusConflict, "pfand_active"},
		{"at the emergency edge", func(r *fanRig) { r.temp = 85 }, 70, http.StatusServiceUnavailable, "package_too_hot"},
		{"the sensor cannot be read", func(r *fanRig) { r.tempErr = errors.New("no such file") }, 70, http.StatusServiceUnavailable, "temperature_unknown"},
		{"the curve cannot be read", func(r *fanRig) { r.curveErr = errors.New("no such file") }, 70, http.StatusServiceUnavailable, "temperature_unknown"},
		{"the device refused the program", func(r *fanRig) { r.setErr = errors.New("device busy") }, 70, http.StatusInternalServerError, "write_failed"},
		{"the duty could not be read before the write", func(r *fanRig) { r.getErr = errors.New("device busy") }, 70, http.StatusInternalServerError, "internal"},
		{"another writer took the single slot", func(r *fanRig) { lost := 25; r.loseTo = &lost }, 70, http.StatusInternalServerError, "write_unconfirmed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newFanHarness(t)
			tc.setup(h.rig)

			rec := h.do(t, "admin", http.MethodPost, "/api/plus/fan/level",
				map[string]any{"level": tc.level, "confirm": true})
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			assert.Equal(t, tc.code, faultCode(t, rec))
		})
	}
}

// TestTheTwoTemperatureFactsAreNotOneMessage is the requirement behind
// ErrTemperatureUnknown. Both are 503, so if the code or the text were shared a
// panel would tell an operator with a broken sensor to cool a machine that was
// never hot.
func TestTheTwoTemperatureFactsAreNotOneMessage(t *testing.T) {
	h := newFanHarness(t)

	h.rig.temp = 85
	hot := levelFault(t, h)

	h.rig.tempErr = errors.New("sensor gone")
	cold := levelFault(t, h)

	assert.Equal(t, "package_too_hot", hot)
	assert.Equal(t, "temperature_unknown", cold)
	assert.NotEqual(t, hot, cold)
}

// levelFault asks for a duty the harness must refuse and returns the code it
// refused with. The harness is taken rather than built: two seams alive at once
// is what seamMu exists to prevent, and a helper that built its own would wait on
// the lock the first one is still holding until the test ends.
func levelFault(t *testing.T, h *fanHarness) string {
	t.Helper()
	rec := h.do(t, "admin", http.MethodPost, "/api/plus/fan/level",
		map[string]any{"level": 70, "confirm": true})
	require.NotEqual(t, http.StatusOK, rec.Code, rec.Body.String())
	return faultCode(t, rec)
}

// TestARefusedWriteDoesNotTripTheBreakerIntoAFalseLockout: three refusals leave
// the next write answerable. If a refusal counted as a device fault, an operator
// clicking save four times would be told the fan control is broken when nothing
// has failed.
func TestARefusedWriteDoesNotTripTheBreakerIntoAFalseLockout(t *testing.T) {
	h := newFanHarness(t)
	h.rig.pfandUp = true
	for range 4 {
		rec := h.do(t, "admin", http.MethodPost, "/api/plus/fan/level",
			map[string]any{"level": 70, "confirm": true})
		assert.Equal(t, "pfand_active", faultCode(t, rec))
	}
	h.rig.pfandUp = false
	rec := h.do(t, "admin", http.MethodPost, "/api/plus/fan/level",
		map[string]any{"level": 70, "confirm": true})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// TestTheBreakerIsReportedAfterTheGuardThatStoodInItsWay pins the precedence
// Task 14 fixed, on the wire. Told "the daemon owns the fan", an operator stops
// the daemon; being told "latched open" first would send them to wait out a
// cooldown and find the same answer again.
func TestTheBreakerIsReportedAfterTheGuardThatStoodInItsWay(t *testing.T) {
	h := newFanHarness(t)
	h.rig.setErr = errors.New("device busy")
	for range 3 {
		h.do(t, "admin", http.MethodPost, "/api/plus/fan/level",
			map[string]any{"level": 70, "confirm": true})
	}

	h.rig.pfandUp = true
	rec := h.do(t, "admin", http.MethodPost, "/api/plus/fan/level",
		map[string]any{"level": 70, "confirm": true})
	assert.Equal(t, "pfand_active", faultCode(t, rec),
		"the guard in the way is the thing the operator has to act on")

	h.rig.pfandUp = false
	rec = h.do(t, "admin", http.MethodPost, "/api/plus/fan/level",
		map[string]any{"level": 70, "confirm": true})
	assert.Equal(t, "breaker_open", faultCode(t, rec),
		"and the latch is still there once the guard is out of the way")
}

// ---- the request body ----

// TestUnknownFieldsInAWriteAreRefused. Every field in these bodies is a physical
// instruction. A name the panel misspells would be dropped in silence while the
// operator believed it had been applied, which is the failure the parser in
// agent/plus already refuses for the same reason on the curve file.
func TestUnknownFieldsInAWriteAreRefused(t *testing.T) {
	for _, ep := range []struct {
		name string
		path string
		body map[string]any
	}{
		{"level", "/api/plus/fan/level", map[string]any{"level": 70, "confirm": true, "levl": 70}},
		{"mode", "/api/plus/fan/mode", map[string]any{"mode": "auto", "confirm": true, "restart": true}},
		{"curve", "/api/plus/fan/curve", withExtra(curveBody(), "t6", 85)},
	} {
		t.Run(ep.name, func(t *testing.T) {
			h := newFanHarness(t)
			rec := h.do(t, "admin", http.MethodPost, ep.path, ep.body)
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Empty(t, h.rig.writes())
			assert.Empty(t, h.rig.actions)
			assert.Equal(t, testCurveSource, string(h.rig.curve))
		})
	}
}

func withExtra(body map[string]any, key string, val any) map[string]any {
	out := map[string]any{}
	for k, v := range body {
		out[k] = v
	}
	out[key] = val
	return out
}

// TestAbsentLevelIsNotZero: zero is the firmware curve and a legal duty, so a
// body that left the field out would otherwise hand the fan back silently.
func TestAbsentLevelIsNotZero(t *testing.T) {
	h := newFanHarness(t)
	rec := h.do(t, "admin", http.MethodPost, "/api/plus/fan/level",
		map[string]any{"confirm": true})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, strings.ToLower(rec.Body.String()), "level is required")
	assert.Empty(t, h.rig.writes())
}

// TestAPartialCurveIsRefusedRatherThanZeroFilled. A curve is a whole control
// law; zeroing the field a panel forgot produces a curve the host will run and
// the operator did not write.
func TestAPartialCurveIsRefusedRatherThanZeroFilled(t *testing.T) {
	for _, key := range []string{"trim", "gain", "emerg_rel", "l5", "iv"} {
		t.Run(key, func(t *testing.T) {
			h := newFanHarness(t)
			body := curveBody()
			delete(body, key)
			rec := h.do(t, "admin", http.MethodPost, "/api/plus/fan/curve", body)
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), key,
				"the response must name the field that was missing, so an operator can see which one their panel forgot")
			assert.Equal(t, testCurveSource, string(h.rig.curve))
		})
	}
}

// TestACurveTheHostWouldNotRunIsRefusedBeforeItIsWritten: the daemon's answer to
// a curve it dislikes is to run the fan at full speed, so the check has to
// happen before the write rather than after it.
func TestACurveTheHostWouldNotRunIsRefusedBeforeItIsWritten(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
		val  int
	}{
		{"an edge out of order", "t2", 40},
		{"a band duty outside the trim table", "l3", 80},
		{"an emergency release above the edge", "emerg_rel", 90},
		{"the old v1 value", "t4", 84},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newFanHarness(t)
			body := curveBody()
			body[tc.key] = tc.val
			rec := h.do(t, "admin", http.MethodPost, "/api/plus/fan/curve", body)
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Equal(t, "curve_rejected", faultCode(t, rec))
			assert.Equal(t, testCurveSource, string(h.rig.curve))
		})
	}
}

// TestAnOversizedBodyIsRefused: a write endpoint that reads a body of any size
// is a way to make the hub allocate.
func TestAnOversizedBodyIsRefused(t *testing.T) {
	h := newFanHarness(t)
	req := httptest.NewRequest(http.MethodPost, "/api/plus/fan/level",
		strings.NewReader(`{"level":70,"confirm":true,"pad":"`+strings.Repeat("x", 64<<10)+`"}`))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("Authorization", h.tokens["admin"])
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Empty(t, h.rig.writes())
}

func TestAnEmptyBodyIsRefused(t *testing.T) {
	h := newFanHarness(t)
	for _, path := range []string{"/api/plus/fan/level", "/api/plus/fan/mode", "/api/plus/fan/curve"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(""))
		req.Header.Set("content-type", "application/json")
		req.Header.Set("Authorization", h.tokens["admin"])
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code, "%s accepted an empty body", path)
	}
}

// TestABodyThatIsNotJSONIsRefused covers a form post and a truncated document,
// both of which a lenient reader would take apart and half-apply.
func TestABodyThatIsNotJSONIsRefused(t *testing.T) {
	h := newFanHarness(t)
	for _, raw := range []string{
		"level=70&confirm=true",
		`{"level":70,"confirm":true`,
		`{"level":70,"confirm":true}{"level":25}`,
		`[70, true]`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/plus/fan/level", strings.NewReader(raw))
		req.Header.Set("content-type", "application/json")
		req.Header.Set("Authorization", h.tokens["admin"])
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code, "accepted %q", raw)
	}
	assert.Empty(t, h.rig.writes())
}

// ---- what a response may and may not contain ----

// TestNoFanResponseCarriesAHostPathOrAToken. The errors these writes wrap name
// the lock, the device, the curve file and whatever systemd printed; a response
// body is read by any admin account and by whatever log pipeline sits behind it.
func TestNoFanResponseCarriesAHostPathOrAToken(t *testing.T) {
	const secret = "/run/pfanel.lock /etc/pfand.conf /dev/pfanctl PFAN_DEV=deadbeef TOKEN=abc123"
	for _, tc := range []struct {
		name  string
		setup func(*fanRig)
		ep    struct {
			name   string
			method string
			path   string
			body   any
		}
	}{
		{"refused level", func(r *fanRig) { r.tempErr = errors.New(secret) },
			struct {
				name   string
				method string
				path   string
				body   any
			}{"level", http.MethodPost, "/api/plus/fan/level", map[string]any{"level": 70, "confirm": true}}},
		{"failed write", func(r *fanRig) { r.setErr = errors.New(secret) },
			struct {
				name   string
				method string
				path   string
				body   any
			}{"level", http.MethodPost, "/api/plus/fan/level", map[string]any{"level": 70, "confirm": true}}},
		{"state with an unreadable sensor", func(r *fanRig) { r.tempErr = errors.New(secret) },
			struct {
				name   string
				method string
				path   string
				body   any
			}{"state", http.MethodGet, "/api/plus/fan/state", nil}},
		{"state with an unreadable curve", func(r *fanRig) { r.curveErr = errors.New(secret) },
			struct {
				name   string
				method string
				path   string
				body   any
			}{"state", http.MethodGet, "/api/plus/fan/state", nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newFanHarness(t)
			tc.setup(h.rig)
			rec := h.do(t, "admin", tc.ep.method, tc.ep.path, tc.ep.body)
			for _, leak := range []string{"pfanel.lock", "pfand.conf", "pfanctl", "PFAN_DEV", "TOKEN=", "abc123"} {
				assert.NotContains(t, rec.Body.String(), leak,
					"%s answered with %s", tc.name, leak)
			}
		})
	}
}

// TestTheAuditTrailNamesWhoAndWhyWithoutThePathsUnderneath: the trail is the
// record an incident review reads, so it must carry the code and the actor, and
// it must not carry the text the code stands for.
func TestTheAuditTrailNamesWhoAndWhyWithoutThePathsUnderneath(t *testing.T) {
	h := newFanHarness(t)
	h.rig.tempErr = errors.New("/run/pfanel.lock is not readable")

	rec := h.do(t, "admin", http.MethodPost, "/api/plus/fan/level",
		map[string]any{"level": 70, "confirm": true})
	require.NotEqual(t, http.StatusOK, rec.Code)

	rec = h.do(t, "admin", http.MethodGet, "/api/plus/fan/audit", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), "pfanel.lock")

	items, _ := decodeBody(t, rec)["items"].([]any)
	require.NotEmpty(t, items, "a refused write is what an incident review needs to see")
	last := items[len(items)-1].(map[string]any)
	assert.Equal(t, "set_level", last["op"])
	assert.Equal(t, "refused", last["outcome"])
	assert.Equal(t, "temperature_unknown", last["code"],
		"the trail must name the refusal, not paraphrase it")
	assert.Contains(t, last, "actor")
	assert.NotEmpty(t, last["actor"], "an unattributed write is not a record")
}

// ---- the state read ----

// TestStateReportsTheCurveSoThePanelDoesNotFetchItSeparately: an editor
// rendered against a curve fetched before the write it is about to make is an
// editor that overwrites the change.
func TestStateReportsTheCurveSoThePanelDoesNotFetchItSeparately(t *testing.T) {
	h := newFanHarness(t)
	rec := h.do(t, "admin", http.MethodGet, "/api/plus/fan/state", nil)
	require.Equal(t, http.StatusOK, rec.Code)

	body := decodeBody(t, rec)
	curve, ok := body["curve"].(map[string]any)
	require.True(t, ok, "state must carry the curve: %s", rec.Body.String())
	for key, want := range map[string]float64{
		"idle": 50, "l0": 25, "t1": 56, "l1": 30, "t2": 62, "l2": 40,
		"t3": 67, "l3": 50, "t4": 72, "l4": 60, "t5": 79, "l5": 70,
		"emerg": 80, "e": 100, "emerg_rel": 75, "cap": 70, "trim": 6, "gain": 1,
		"hyst": 3, "dwell": 30, "dead": 2, "iv": 10,
	} {
		assert.Equal(t, want, curve[key], "curve field %s", key)
	}
}

// TestStateMarksEachReadingRatherThanBorrowingAZero: zero is a legal duty and a
// legal temperature, so an unreadable reading must not answer with either.
func TestStateMarksEachReadingRatherThanBorrowingAZero(t *testing.T) {
	h := newFanHarness(t)
	h.rig.getErr = errors.New("gone")
	h.rig.tempErr = errors.New("gone")
	h.rig.curveErr = errors.New("gone")

	rec := h.do(t, "admin", http.MethodGet, "/api/plus/fan/state", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	body := decodeBody(t, rec)

	assert.Equal(t, false, body["level_read"])
	assert.Nil(t, body["level"])
	assert.Equal(t, false, body["pkg_temp_read"])
	assert.Nil(t, body["pkg_temp_c"])
	assert.Nil(t, body["curve"])
}

// TestStateReportsARegressDeadlineOnlyWhenThereIsOne. An operator shown a
// countdown to nothing will wait out a regress that a pending lost to an agent
// restart no longer owns, which is how a manual duty outlives its own term.
func TestStateReportsARegressDeadlineOnlyWhenThereIsOne(t *testing.T) {
	h := newFanHarness(t)

	rec := h.do(t, "admin", http.MethodGet, "/api/plus/fan/state", nil)
	body := decodeBody(t, rec)
	assert.Equal(t, false, body["regress_pending"],
		"with nothing pending the panel must be told there is nothing pending")
	assert.NotContains(t, rec.Body.String(), "regress_at")

	rec = h.do(t, "admin", http.MethodPost, "/api/plus/fan/level",
		map[string]any{"level": 70, "confirm": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotEmpty(t, decodeBody(t, rec)["regress_at"],
		"the write itself must carry the deadline, so the panel need not ask twice")

	rec = h.do(t, "admin", http.MethodGet, "/api/plus/fan/state", nil)
	body = decodeBody(t, rec)
	assert.Equal(t, true, body["regress_pending"])
	at, ok := body["regress_at"].(string)
	require.True(t, ok)
	parsed, err := time.Parse(time.RFC3339, at)
	require.NoError(t, err)
	assert.True(t, parsed.After(time.Now()), "the deadline is in the future, not year one")
}

// TestAZeroDutyIsNeverLabelledOff: level 0 hands the fan back to the firmware
// curve. It does not stop the fan.
func TestAZeroDutyIsNeverLabelledOff(t *testing.T) {
	h := newFanHarness(t)
	h.rig.level = 0
	rec := h.do(t, "admin", http.MethodGet, "/api/plus/fan/state", nil)
	assert.Equal(t, "auto", decodeBody(t, rec)["level_label"])
}

// ---- compile-time guards on the harness itself ----

func TestFanHarnessReportsWritesItSaw(t *testing.T) {
	h := newFanHarness(t)
	assert.Empty(t, h.rig.writes())
	rec := h.do(t, "admin", http.MethodPost, "/api/plus/fan/level",
		map[string]any{"level": 70, "confirm": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []int{70}, h.rig.writes(),
		"if the harness cannot see a write then the refusals above prove nothing")
}
