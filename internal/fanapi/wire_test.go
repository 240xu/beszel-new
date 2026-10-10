package fanapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/henrygd/beszel/agent/plus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// describeAppliedNeedsNoRestart
func TestDescribeReportsAnAppliedWrite(t *testing.T) {
	d := Describe(Result{Applied: true, Level: 70})
	assert.True(t, d.Applied)
	assert.False(t, d.NeedsPfandRestart)
	assert.Equal(t, "applied", d.Status)
	assert.Empty(t, d.NextAction, "a change that is already in force needs nothing from the operator")
}

// TestDescribeWithholdsAppliedUntilTheDaemonHasReadTheCurve is ruling 1. The
// guard's own Applied says the bytes reached the disk, which is a true and
// necessary fact for the audit trail; the wire response is answering a different
// question, which is whether the change is in force. Answering "applied" while
// NeedsPfandRestart is true is what makes the panel render "saved".
func TestDescribeWithholdsAppliedUntilTheDaemonHasReadTheCurve(t *testing.T) {
	d := Describe(Result{Applied: true, NeedsPfandRestart: true})
	assert.False(t, d.Applied,
		"a curve on disk that no daemon has read is not in force, and reporting it as applied invites a save button that lies")
	assert.True(t, d.NeedsPfandRestart)
	assert.Equal(t, "pending_restart", d.Status)
	assert.Equal(t, "restart_pfand", d.NextAction,
		"ruling 2: a panel that only says pending has no way to finish, so the operator saves again and again")
}

// TestDescribeNeverClaimsASavedCurveToBeSaved is ruling 3. The wording rule is
// about what the panel is shown, so this asserts over the serialised body: a
// field name is as capable of claiming a save as a status string is.
func TestDescribeNeverClaimsASavedCurveToBeSaved(t *testing.T) {
	body, err := json.Marshal(Describe(Result{Applied: true, NeedsPfandRestart: true}))
	require.NoError(t, err)
	for _, claim := range []string{"saved", "in force", "生效", "已保存"} {
		assert.NotContains(t, string(body), claim,
			"a pending change must not be described in words that mean it is already running")
	}
	assert.Contains(t, string(body), StatusPendingRestart)
	assert.Contains(t, string(body), ActionRestartPfand)
}

func TestDescribeCarriesTheRegressDeadlineOnlyWhenThereIsOne(t *testing.T) {
	at := time.Date(2026, 10, 9, 12, 10, 0, 0, time.UTC)
	d := Describe(Result{Applied: true, Level: 25, RegressAt: at})
	require.NotNil(t, d.RegressAt)
	assert.Equal(t, at, *d.RegressAt)

	d = Describe(Result{Applied: true, Level: 25})
	assert.Nil(t, d.RegressAt,
		"an absent deadline must be absent rather than the zero time, or the panel counts down to year one")
}

// classifyAppliedIsNotAnError
func TestClassifyNamesEachRefusalSeparately(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"unconfirmed", fmt.Errorf("duty 70: %w", ErrNotConfirmed), http.StatusBadRequest, "confirm_required"},
		{"level off the whitelist", fmt.Errorf("%s: %w", "invalid fan level 33", plus.ErrFanLevelNotAllowed), http.StatusBadRequest, "level_not_allowed"},
		{"curve rejected", fmt.Errorf("%w: T2 must exceed T1", ErrCurveRejected), http.StatusBadRequest, "curve_rejected"},
		{"pfand owns the fan", fmt.Errorf("duty 70: %w", plus.ErrPfandActive), http.StatusConflict, "pfand_active"},
		{"package at the emergency edge", fmt.Errorf("duty 70 at 81C: %w", plus.ErrPackageTemperature), http.StatusServiceUnavailable, "package_too_hot"},
		{"temperature unreadable", fmt.Errorf("duty 70: %w: open /sys/class/thermal/thermal_zone9/temp: no such file", ErrTemperatureUnknown), http.StatusServiceUnavailable, "temperature_unknown"},
		{"breaker latched", ErrBreakerOpen, http.StatusServiceUnavailable, "breaker_open"},
		{"write not confirmed", fmt.Errorf("asked for 70, device reports 25: %w", ErrWriteUnconfirmed), http.StatusInternalServerError, "write_unconfirmed"},
		{"the device refused the program", fmt.Errorf("program duty 70: %w", ErrWriteFailed), http.StatusInternalServerError, "write_failed"},
		{"restore failed", fmt.Errorf("%w: restoring duty 25", ErrRollbackFailed), http.StatusInternalServerError, "rollback_failed"},
		{"not this platform", plus.ErrFanControlUnsupported, http.StatusNotImplemented, "unsupported"},
		{"anything else", errors.New("the disk went away"), http.StatusInternalServerError, "internal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := Classify(tc.err)
			assert.Equal(t, tc.status, f.Status)
			assert.Equal(t, tc.code, f.Code)
			assert.NotEmpty(t, f.Message)
		})
	}
}

// TestClassifyTellsTheTwoTemperatureFactsApart is the requirement behind
// ErrTemperatureUnknown existing at all. Both are 503, so the machine code and
// the message are the only thing left to tell them apart; a panel that renders
// one message for both sends an operator with a broken sensor to cool a machine
// that is already fine.
func TestClassifyTellsTheTwoTemperatureFactsApart(t *testing.T) {
	hot := Classify(fmt.Errorf("duty 70 at 81C: %w", plus.ErrPackageTemperature))
	unknown := Classify(fmt.Errorf("duty 70: %w: open %s: no such file", ErrTemperatureUnknown, "/run/pfanel.lock"))
	assert.NotEqual(t, hot.Code, unknown.Code)
	assert.NotEqual(t, hot.Message, unknown.Message)
	assert.NotContains(t, unknown.Message, "hot",
		"the unreadable case must not read as an over-temperature case")
	assert.Contains(t, unknown.Message, "temperature could not be read")
}

// TestClassifyPrefersTheRestoreFailure pins the precedence when both facts are
// wrapped into one error: the fan is now holding a duty nobody asked for, which
// is the part an operator has to act on tonight.
func TestClassifyPrefersTheRestoreFailure(t *testing.T) {
	both := fmt.Errorf("%w, and %w: restoring the previous curve", ErrWriteUnconfirmed, ErrRollbackFailed)
	assert.Equal(t, "rollback_failed", Classify(both).Code)
	assert.Equal(t, "write_unconfirmed", Classify(ErrWriteUnconfirmed).Code)
}

// TestClassifyNeverEchoesTheWrappedError is the leak check. The errors this
// package wraps carry the device, the lock and the curve file; none of them
// belong in an HTTP body that any authenticated admin, and any log pipeline
// downstream of it, can read.
func TestClassifyNeverEchoesTheWrappedError(t *testing.T) {
	secret := "TOKEN=deadbeef /run/pfanel.lock PFAN_DEV=/dev/pfanctl /etc/pfand.conf"
	for _, err := range []error{
		fmt.Errorf("duty 70: %w: %s", ErrTemperatureUnknown, secret),
		fmt.Errorf("duty 70: %w: %s", plus.ErrPackageTemperature, secret),
		fmt.Errorf("read the duty to restore: %w", errors.New(secret)),
		fmt.Errorf("%w: %s", ErrCurveRejected, secret),
		errors.New(secret),
	} {
		f := Classify(err)
		assert.NotContains(t, f.Message, "deadbeef")
		assert.NotContains(t, f.Message, "pfanel.lock")
		assert.NotContains(t, f.Message, "PFAN_DEV")
		assert.NotContains(t, f.Message, "pfand.conf")
	}
}

// TestClassifyFollowsTheGuardPrecedence pins that the breaker is not hoisted
// above the guard that stood in its way. Task 14 made the guard the answer on
// purpose; a classifier that reported breaker_open for that error would send an
// operator to wait out a two minute cooldown when the real cause is in front of
// them.
func TestClassifyFollowsTheGuardPrecedence(t *testing.T) {
	guarded := fmt.Errorf("duty 70: %w", plus.ErrPfandActive)
	assert.Equal(t, "pfand_active", Classify(guarded).Code)
	assert.Equal(t, "breaker_open", Classify(ErrBreakerOpen).Code)
}

// TestEveryDeclineIsCoded is the test the guard.go comment names. Every gate a
// regress can decline at must carry a code and its own text, so an audit entry
// never leaves a panel matching English prose: a phrase reworded for clarity
// would otherwise silently become a code nothing branches on.
func TestEveryDeclineIsCoded(t *testing.T) {
	for code, message := range declineMessages {
		assert.NotEmpty(t, code)
		assert.NotEmpty(t, message,
			"a code with no text is a code the trail reports and no one can read")
	}
	for _, code := range []string{
		DeclineNone, DeclineDaemon, DeclineUnreadable, DeclineAtEmergency,
	} {
		assert.Contains(t, declineMessages, code,
			"a gate regressLocked can name must have text, or the trail says only that it declined")
	}
}

// TestTheDeclineTextsAreDistinct keeps a reworded gate from quietly becoming the
// same sentence as another one, which would put two codes on one sentence and
// make the trail's claim ambiguous.
func TestTheDeclineTextsAreDistinct(t *testing.T) {
	seen := map[string]string{}
	for code, message := range declineMessages {
		if other, dup := seen[message]; dup {
			t.Errorf("%s and %s share the text %q", code, other, message)
		}
		seen[message] = code
	}
}

// TestEveryClassificationCarriesBothHalves: a code with no status would answer
// 200 with a refusal body, and a status with no code would leave the panel with
// nothing to branch on.
func TestEveryClassificationCarriesBothHalves(t *testing.T) {
	for _, c := range classifications {
		assert.NotEmpty(t, c.Code, "%v", c.sentinel)
		assert.NotEmpty(t, c.Message, "%v", c.sentinel)
		assert.True(t, c.Status >= 400, "%s answered %d, which reads as success", c.Code, c.Status)
	}
}

// TestEveryCodeIsReachable pins that each classification is named by an error the
// guard can actually produce. A row naming a sentinel nothing returns is a code
// no panel will ever see, and it reads as coverage.
func TestEveryCodeIsReachable(t *testing.T) {
	for _, c := range classifications {
		t.Run(c.Code, func(t *testing.T) {
			wrapped := fmt.Errorf("a real attempt: %w", c.sentinel)
			f := Classify(wrapped)
			assert.Equal(t, c.Code, f.Code)
			assert.Equal(t, c.Status, f.Status)
			assert.Equal(t, c.Message, f.Message)
			assert.Equal(t, c.Code, codeOf(wrapped),
				"the audit event and the response must name the refusal identically")
		})
	}
}
