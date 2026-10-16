package hub

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/henrygd/beszel/agent/plus"
	"github.com/henrygd/beszel/internal/fanapi"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
)

// The plus fan surface is five routes and one guard. This file owns the HTTP
// shape: what a body may say, what a caller is told, and who may say it at all.
//
// Authorisation is not decided here either. requireAdminRole is the one place in
// the hub that answers it, and duplicating that decision in a second package
// would leave two places to forget. So every route below binds it, including the
// two reads: a curve and a package temperature describe this machine's cooling
// and the limits it runs under.

const (
	// maxFanBody is far above the largest legal body, one curve, and far below
	// anything a panel sends. The limit is what keeps a write endpoint from being
	// a way to make the hub allocate.
	maxFanBody = 8 << 10
	// pfandUnit is the curve daemon the fan is arbitrated against.
	pfandUnit = "pfand.service"
)

// Seams. A route test must not be able to reach the fan, so the seams and the
// daemon are reached through variables a test can replace. Nothing here is a
// mutable setting an operator can turn: they exist so that exercising these
// routes cannot reach a fan.
var (
	fanDeps     = productionDeps
	pfandAction = systemctlPfand
)

// registerPlusFanRoutes binds the plus fan routes under the hub's own router.
func (h *Hub) registerPlusFanRoutes(se *core.ServeEvent) error {
	// In the hub's own data directory, because the record has to outlive the process
	// that wrote it: the floor writes with nobody watching, and the guard holding it is
	// this process, so a restart is exactly when the trail is worth having.
	trail := newFanAudit(filepath.Join(se.App.DataDir(), "fan-audit.jsonl"))
	fc := &fanRoutes{
		deps:  fanDeps(trail.add),
		trail: trail,
		pfand: pfandAction,
	}
	fc.guard = fanapi.New(fc.deps)
	if seenGuard != nil {
		seenGuard(fc.guard)
	}

	// A daemon that is not running has nobody else watching the fan, and the guard that
	// watches it lives in this process and dies with it. The floor is therefore armed
	// from the device rather than from memory: whatever a restart did to the pending
	// state, the daemon being down is visible right now, and that alone is reason to
	// hold a floor.
	if !fc.deps.PfandUp() {
		fc.guard.ArmThermalFloor()
	}

	g := se.Router.Group("/api/plus/fan")
	g.Bind(apis.RequireAuth())
	g.GET("/state", fc.state).BindFunc(requireAdminRole)
	g.GET("/audit", fc.auditList).BindFunc(requireAdminRole)
	g.POST("/level", fc.level).BindFunc(requireAdminRole)
	g.POST("/mode", fc.mode).BindFunc(requireAdminRole)
	g.POST("/curve", fc.curve).BindFunc(requireAdminRole)
	return nil
}

// productionDeps wires the guard to this host's fan, curve and thermal zone.
//
// The zone is read from the platform file rather than written down here. That
// file exists to name this machine's zone, and a literal index in code is the
// one thing that turns a moved sensor into a silently wrong refusal.
func productionDeps(audit func(fanapi.Event)) fanapi.Deps {
	readTemp := readPackageTempC
	return fanapi.Deps{
		Now:       time.Now,
		ReadTempC: readTemp,
		ReadCurve: func() (*plus.Curve, error) { return plus.ReadCurveFile(plus.ProdCurveConf) },
		SetLevel:  plus.SetFanLevel,
		GetLevel:  plus.GetFanLevel,
		PfandUp:   plus.PfandActive,
		ReadCurveBytes: func() ([]byte, error) {
			return os.ReadFile(plus.ProdCurveConf)
		},
		WriteCurveBytes: func(raw []byte) error {
			return plus.WriteCurveBytesFile(plus.ProdCurveConf, raw)
		},
		Audit: audit,
	}
}

// readPackageTempC reads the package temperature from the zone the platform file
// names. sysfs reports millidegrees. A failed read is an error and never zero:
// zero is a temperature, and the guard would rule an unreadable sensor cold.
func readPackageTempC() (int, error) {
	raw, err := os.ReadFile(plus.ProdPlatformConf)
	if err != nil {
		return 0, err
	}
	conf, err := plus.ParsePlatformConf(string(raw))
	if err != nil {
		return 0, err
	}
	b, err := os.ReadFile(conf.ThermalZone)
	if err != nil {
		return 0, err
	}
	milli, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, err
	}
	return milli / 1000, nil
}

func systemctlPfand(action string) error {
	out, err := exec.Command("systemctl", action, pfandUnit).CombinedOutput()
	if err != nil {
		// The command's own output names units and carries a job id, so it is
		// logged rather than returned.
		return fmt.Errorf("systemctl %s %s: %w: %s", action, pfandUnit, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// fanRoutes is the handler set plus the seams the state read needs. The guard
// decides writes; the reads here are the same seams the guard was built from, so
// a state read and a write cannot disagree about what the fan is doing.
type fanRoutes struct {
	deps  fanapi.Deps
	guard *fanapi.Guard
	trail *fanAudit
	pfand func(string) error
}

// actor names who made a write. The trail is the only record of who changed a
// machine's cooling, so an unattributed write is not a record.
func actor(e *core.RequestEvent) string {
	if e.Auth == nil {
		return ""
	}
	return e.Auth.Email()
}

// state reports what the fan is doing, with the curve riding along. A panel that
// had to fetch the curve separately could render an editor against a curve it
// read before the write it is about to make.
func (fc *fanRoutes) state(e *core.RequestEvent) error {
	body := map[string]any{"pfand_active": fc.deps.PfandUp()}

	// Each reading is reported with whether it was readable. A zero duty or a
	// zero temperature is a legal value, so an absent reading must not borrow
	// either of them.
	level, err := fc.deps.GetLevel()
	body["level_read"] = err == nil
	if err == nil {
		body["level"] = level
		body["level_label"] = plus.LevelLabel(level)
	} else {
		body["level"] = nil
	}
	temp, terr := fc.deps.ReadTempC()
	body["pkg_temp_read"] = terr == nil
	if terr == nil {
		body["pkg_temp_c"] = temp
	} else {
		body["pkg_temp_c"] = nil
	}

	// A deadline appears only when one exists. A countdown to nothing teaches
	// an operator to wait out a regress that a lost pending no longer owns.
	if at := fc.guard.PendingRegressAt(); !at.IsZero() {
		body["regress_pending"] = true
		body["regress_at"] = at.UTC().Format(time.RFC3339)
	} else {
		body["regress_pending"] = false
	}
	if c, cerr := fc.deps.ReadCurve(); cerr == nil {
		body["curve"] = curveToWire(c)
	} else {
		body["curve"] = nil
	}
	return e.JSON(http.StatusOK, body)
}

func curveToWire(c *plus.Curve) map[string]int {
	return map[string]int{
		"idle": c.Idle, "l0": c.L0,
		"t1": c.T1, "l1": c.L1,
		"t2": c.T2, "l2": c.L2,
		"t3": c.T3, "l3": c.L3,
		"t4": c.T4, "l4": c.L4,
		"t5": c.T5, "l5": c.L5,
		"emerg": c.Emerg, "e": c.E, "emerg_rel": c.EmergRel,
		"cap": c.Cap, "trim": c.Trim, "gain": c.Gain,
		"hyst": c.Hyst, "dwell": c.Dwell, "dead": c.Dead, "iv": c.IV,
	}
}

// level programs a duty.
func (fc *fanRoutes) level(e *core.RequestEvent) error {
	var req struct {
		Level   *int `json:"level"`
		Confirm bool `json:"confirm"`
	}
	if err := decodeBody(e, &req); err != nil {
		return err
	}
	if !req.Confirm {
		return confirmRequired(e)
	}
	// Absent is not zero: zero is the firmware curve and a legal answer, so a
	// body that forgot the field would quietly hand the fan back to the curve.
	if req.Level == nil {
		return e.BadRequestError("level is required", nil)
	}
	res, err := fc.guard.SetManualLevel(*req.Level, fanapi.Request{Confirm: true, Actor: actor(e)})
	if err != nil {
		return fc.fail(e, "set level", err)
	}
	return e.JSON(http.StatusOK, fanapi.Describe(res))
}

// mode hands the fan between the daemon and a hand-set duty. Stopping the daemon
// is the only way to make it leave the fan alone; starting it is the only way a
// curve that reached the disk becomes behaviour, which is why this is the outlet
// a pending change needs.
func (fc *fanRoutes) mode(e *core.RequestEvent) error {
	var req struct {
		Mode    string `json:"mode"`
		Confirm bool   `json:"confirm"`
	}
	if err := decodeBody(e, &req); err != nil {
		return err
	}
	if !req.Confirm {
		return confirmRequired(e)
	}
	action := ""
	switch req.Mode {
	case "manual":
		action = "stop"
	case "auto":
		action = "start"
	default:
		return e.BadRequestError("mode must be auto or manual", nil)
	}
	if err := fc.pfand(action); err != nil {
		slog.Error("fan mode change failed", "mode", req.Mode, "err", err)
		// Coded, because this is the one refusal on this surface that happens after the daemon
		// has been asked to act. A code-less refusal can only say "the server named no reason",
		// and an operator told that about a failed restart is sent looking for a request that
		// was never read rather than for a systemd job that failed. The catch-all is the one
		// the guard itself falls back to, and it is truthful here.
		return coded(e, http.StatusInternalServerError,
			"could not change the curve daemon's state", fanapi.InternalFault.Code)
	}
	// Manual took pfand's control loop away, so the floor takes over watching the
	// fan. Arming follows the daemon actually stopping, so a stop that failed leaves
	// no floor: a floor outliving its own daemon would be a second writer on one fan.
	if req.Mode == "manual" {
		fc.guard.ArmThermalFloor()
	} else {
		fc.guard.DisarmThermalFloor()
	}

	return e.JSON(http.StatusOK, map[string]any{
		"mode":         req.Mode,
		"pfand_active": fc.deps.PfandUp(),
	})
}

// curve replaces the curve. This is the write ruling 1 is about: the bytes land,
// the daemon has not read them, and the response must not call that applied.
func (fc *fanRoutes) curve(e *core.RequestEvent) error {
	req, aerr := decodeCurveRequest(e)
	if aerr != nil {
		return aerr
	}
	if !req.Confirm {
		return confirmRequired(e)
	}
	res, err := fc.guard.WriteCurve(req.curve(), fanapi.Request{Confirm: true, Actor: actor(e)})
	if err != nil {
		return fc.fail(e, "write curve", err)
	}
	return e.JSON(http.StatusOK, fanapi.Describe(res))
}

func (fc *fanRoutes) auditList(e *core.RequestEvent) error {
	return e.JSON(http.StatusOK, map[string]any{"items": fc.trail.entries()})
}

// fail turns a guard error into a wire response. The error is logged and never returned: it
// names the lock, the device and the curve file, none of which belong in a readable body.
func (fc *fanRoutes) fail(e *core.RequestEvent, what string, err error) error {
	fault := fanapi.Classify(err)
	slog.Warn("fan write refused", "op", what, "code", fault.Code, "actor", actor(e), "err", err)
	return coded(e, fault.Status, fault.Message, fault.Code)
}

// confirmRequired refuses a write that arrived unconfirmed. The code comes from the
// guard's own classification, and Data is assigned here for the reason fail gives.
func confirmRequired(e *core.RequestEvent) error {
	return coded(e, http.StatusBadRequest, "confirm is required",
		fanapi.Classify(fanapi.ErrNotConfirmed).Code)
}

// coded is the one way this file puts a refusal code on the wire. Data is assigned after
// the error is built rather than passed as errData: PocketBase rewrites a map errData into
// validation codes, replacing the refusal with "validation_invalid_value".
//
// Every code here is borrowed from fanapi rather than written out. A refusal the guard never
// classified has no member of that closed set to borrow, and a borrowed one that misdescribes
// it is worse than none: a panel told "the curve failed validation" goes looking for a curve.
func coded(e *core.RequestEvent, status int, message, code string) error {
	apiErr := e.Error(status, message, nil)
	apiErr.Data = map[string]any{"code": code}
	return apiErr
}

// decodeBody reads a request body strictly.
//
// Unknown fields are refused. Every field in these bodies is a physical instruction, and
// a name the panel misspells would be dropped in silence while the operator believes it
// was applied. The parser in agent/plus takes the same position about the curve file.
//
// These two refusals carry no code, and that is the deliberate remainder: the closed set
// fanapi publishes has no member meaning "this body could not be read" for a duty or a mode,
// and every member that could be borrowed would misdescribe it. A code-less refusal is
// answered by a panel that says only what it knows, which is true, where "the request was
// refused before it reached the fan" was true by accident. Every such path is listed in
// TestFanRefusalsEitherCarryACodeOrAreOnThisList.
func decodeBody(e *core.RequestEvent, dst any) error {
	body, err := readFanBody(e)
	if err != nil {
		return e.BadRequestError("invalid body", nil)
	}
	if err := strictJSON(body, dst); err != nil {
		return e.BadRequestError("invalid body", nil)
	}
	return nil
}

func readFanBody(e *core.RequestEvent) ([]byte, error) {
	if e.Request.Body == nil {
		return nil, errors.New("no body")
	}
	body, err := io.ReadAll(io.LimitReader(e.Request.Body, maxFanBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxFanBody {
		return nil, errors.New("body too large")
	}
	return body, nil
}

func strictJSON(body []byte, dst any) error {
	if len(bytes.TrimSpace(body)) == 0 {
		return errors.New("empty body")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	// A second document behind the first is a caller sending something other than
	// what it means to send, and taking the first half of it is how a body like
	// {"level":70,...}{"level":25} would program 70.
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing content after the body")
	}
	return nil
}

// curveRequest mirrors plus.Curve by pointer so an absent field is not a zero one. Every
// field is required: zeroing a missing one produces a curve the host will run unasked.
type curveRequest struct {
	Idle     *int `json:"idle"`
	L0       *int `json:"l0"`
	T1       *int `json:"t1"`
	L1       *int `json:"l1"`
	T2       *int `json:"t2"`
	L2       *int `json:"l2"`
	T3       *int `json:"t3"`
	L3       *int `json:"l3"`
	T4       *int `json:"t4"`
	L4       *int `json:"l4"`
	T5       *int `json:"t5"`
	L5       *int `json:"l5"`
	Emerg    *int `json:"emerg"`
	E        *int `json:"e"`
	EmergRel *int `json:"emerg_rel"`
	Cap      *int `json:"cap"`
	Trim     *int `json:"trim"`
	Gain     *int `json:"gain"`
	Hyst     *int `json:"hyst"`
	Dwell    *int `json:"dwell"`
	Dead     *int `json:"dead"`
	IV       *int `json:"iv"`
	Confirm  bool `json:"confirm"`
}

// curveFieldNames is the wire order, kept beside the struct so a rename of a tag
// and a rename of the name that is reported cannot happen to each other.
var curveFieldNames = [...]string{
	"idle", "l0", "t1", "l1", "t2", "l2", "t3", "l3", "t4", "l4", "t5", "l5",
	"emerg", "e", "emerg_rel", "cap", "trim", "gain", "hyst", "dwell", "dead", "iv",
}

func (r *curveRequest) values() [22]*int {
	return [22]*int{r.Idle, r.L0, r.T1, r.L1, r.T2, r.L2, r.T3, r.L3, r.T4, r.L4,
		r.T5, r.L5, r.Emerg, r.E, r.EmergRel, r.Cap, r.Trim, r.Gain, r.Hyst,
		r.Dwell, r.Dead, r.IV}
}

func decodeCurveRequest(e *core.RequestEvent) (*curveRequest, error) {
	var req curveRequest
	// A body this route cannot read is a curve it rejected before anything was written,
	// which is what curve_rejected already means, so it is named rather than guessed at.
	body, err := readFanBody(e)
	if err != nil {
		return nil, coded(e, http.StatusBadRequest, "invalid body", fanapi.Classify(fanapi.ErrCurveRejected).Code)
	}
	if err := strictJSON(body, &req); err != nil {
		return nil, coded(e, http.StatusBadRequest, "invalid body", fanapi.Classify(fanapi.ErrCurveRejected).Code)
	}
	for i, v := range req.values() {
		if v == nil {
			return nil, coded(e, http.StatusBadRequest,
				fmt.Sprintf("curve is missing %s", curveFieldNames[i]), fanapi.Classify(fanapi.ErrCurveRejected).Code)
		}
	}
	return &req, nil
}

func (r *curveRequest) curve() *plus.Curve {
	v := r.values()
	d := func(i int) int { return *v[i] }
	return &plus.Curve{
		Idle: d(0), L0: d(1),
		T1: d(2), L1: d(3),
		T2: d(4), L2: d(5),
		T3: d(6), L3: d(7),
		T4: d(8), L4: d(9),
		T5: d(10), L5: d(11),
		Emerg: d(12), E: d(13), EmergRel: d(14),
		Cap: d(15), Trim: d(16), Gain: d(17),
		Hyst: d(18), Dwell: d(19), Dead: d(20), IV: d(21),
	}
}

// fanAudit keeps the recent attempts, bounded: an unbounded log behind a JSON
// endpoint in a long-lived hub is a memory leak with a read path.
type fanAudit struct {
	mu     sync.Mutex
	events []fanapi.Event
	// path is where the trail is kept between processes, and onDisk how many lines the
	// file holds, so the trim that bounds it does not re-read the file to find out.
	path   string
	onDisk int
}

// auditRecord is what one line of the trail file holds, spelled with the field names the
// route serves rather than the Go ones: an operator handed fan-audit.jsonl can then match
// every line against the endpoint that produced it.
type auditRecord struct {
	Op      string `json:"op"`
	Outcome string `json:"outcome"`
	Level   int    `json:"level"`
	At      string `json:"at"`
	Actor   string `json:"actor,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

func recordOf(e fanapi.Event) auditRecord {
	return auditRecord{
		Op: string(e.Op), Outcome: string(e.Outcome), Level: e.Level,
		At: e.At.UTC().Format(time.RFC3339), Actor: e.Actor,
		Code: e.Code, Message: e.Message,
	}
}

func eventOf(r auditRecord) fanapi.Event {
	at, err := time.Parse(time.RFC3339, r.At)
	if err != nil {
		// A record whose timestamp will not parse is still a record of a write that
		// happened. Dropping it would lose the event to a formatting problem.
		at = time.Time{}
	}
	return fanapi.Event{
		Op: fanapi.Op(r.Op), Outcome: fanapi.Outcome(r.Outcome), Level: r.Level,
		At: at, Actor: r.Actor, Code: r.Code, Message: r.Message,
	}
}

const fanAuditLimit = 200

// newFanAudit opens the trail at path, reading back what the last process left there.
//
// The read is best-effort in both directions, deliberately. A hub that refused to start
// because its audit file was unreadable would trade a fan that runs for a fan nobody can
// account for, and a trail that cannot be written is no reason to stop writing to the
// device: the ring holds the live window either way, and the failure is logged where an
// operator can find it.
func newFanAudit(path string) *fanAudit {
	a := &fanAudit{path: path}
	a.load()
	return a
}

// load seeds the ring from the file, skipping any line it cannot read. A line cut in half
// by a kill during an append is the normal crash artefact, so it is dropped on its own
// rather than taking the whole trail down with it.
func (a *fanAudit) load() {
	raw, err := os.ReadFile(a.path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("fan audit trail unreadable, starting empty", "path", a.path, "err", err)
		}
		return
	}
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if line == "" {
			continue
		}
		var r auditRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			slog.Warn("fan audit line skipped", "path", a.path, "err", err)
			continue
		}
		a.events = append(a.events, eventOf(r))
		a.onDisk++
	}
	a.trimLocked()
}

// appendLocked puts one event on disk. Every error is logged and swallowed: the trail
// records what happened, it never gates whether it may happen.
func (a *fanAudit) appendLocked(e fanapi.Event) {
	line, err := json.Marshal(recordOf(e))
	if err != nil {
		slog.Warn("fan audit event not serialisable", "op", string(e.Op), "err", err)
		return
	}
	f, err := os.OpenFile(a.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		slog.Warn("fan audit trail not writable", "path", a.path, "err", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		slog.Warn("fan audit append failed", "path", a.path, "err", err)
		return
	}
	a.onDisk++
	a.trimLocked()
}

// trimLocked keeps the file to the same window the route serves. Rewriting rather than
// rotating is deliberate: the window is small, the writes are rare, and a second file is
// a second thing to go missing.
func (a *fanAudit) trimLocked() {
	if a.onDisk <= fanAuditLimit {
		return
	}
	keep := a.events[len(a.events)-fanAuditLimit:]
	var body strings.Builder
	for _, e := range keep {
		if line, err := json.Marshal(recordOf(e)); err == nil {
			body.Write(line)
			body.WriteByte(10)
		}
	}
	if err := os.WriteFile(a.path, []byte(body.String()), 0o600); err != nil {
		slog.Warn("fan audit trim failed", "path", a.path, "err", err)
		return
	}
	a.events = keep
	a.onDisk = len(keep)
}

func (a *fanAudit) add(e fanapi.Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
	if len(a.events) > fanAuditLimit {
		a.events = a.events[len(a.events)-fanAuditLimit:]
	}
	a.appendLocked(e)
}

// entries renders the trail. The reason is already a code and its own text, set
// where the error was classified: recovering it from the message would mean
// matching English, which is exactly what the sentinels exist to avoid.
func (a *fanAudit) entries() []map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]map[string]any, 0, len(a.events))
	for _, e := range a.events {
		item := map[string]any{
			"op":      string(e.Op),
			"outcome": string(e.Outcome),
			"level":   e.Level,
			"at":      e.At.UTC().Format(time.RFC3339),
		}
		if e.Actor != "" {
			item["actor"] = e.Actor
		}
		if e.Code != "" {
			item["code"] = e.Code
		}
		if e.Message != "" {
			item["message"] = e.Message
		}
		out = append(out, item)
	}
	return out
}
