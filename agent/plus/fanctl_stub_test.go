//go:build !linux && testing

package plus

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The five reasons are one vocabulary across platforms. Task 15 classifies on
// them, so an answer it cannot classify is an answer it has to render as raw
// English -- and this stub used to answer with a bare string, which is how a
// caller ends up substring-matching a message.
//
// Platform support is its own reason rather than a fifth spelling of a refusal.
// Mapping these onto ErrFanLevelNotAllowed would be a false statement, not a
// rough one: nothing refused the duty, this host simply cannot program any.
func TestStubReportsPlatformSupportAsItsOwnReason(t *testing.T) {
	out, err := AcpiEval(`\_SB.PCI0.LPCB.EC0.TFN1._FST 0`, time.Second)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrFanControlUnsupported,
		"a UI written on linux has to be able to classify an answer from any host")
	assert.Empty(t, out, "no value may be invented where the platform has none")

	level, err := GetFanLevel()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrFanControlUnsupported)
	assert.Zero(t, level)

	ec, err := ReadEC76()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrFanControlUnsupported)
	assert.Zero(t, ec)

	assert.ErrorIs(t, SetFanLevel(40), ErrFanControlUnsupported,
		"a whitelisted duty is still unprogrammable here")
	assert.NotErrorIs(t, SetFanLevel(26), ErrFanLevelNotAllowed,
		"the whitelist refused nothing: this host never reached that check")
}

// The reason has to survive the two things that break a switch statement: sharing
// a value with another reason, and being wrapped on the way out to the UI.
func TestStubReasonIsDistinctAndSurvivesWrapping(t *testing.T) {
	for _, reason := range []error{
		ErrFanLevelNotAllowed, ErrPfandActive, ErrPackageTemperature, ErrThermalProfileT4,
	} {
		assert.NotErrorIs(t, ErrFanControlUnsupported, reason,
			"two reasons sharing one value collapse into a single reason")
	}
	wrapped := fmt.Errorf("set fan level: %w", ErrFanControlUnsupported)
	assert.ErrorIs(t, wrapped, ErrFanControlUnsupported,
		"a reason that stops being findable once wrapped is not a reason")
	assert.Equal(t, "ACPI fan control is only implemented on linux", ErrFanControlUnsupported.Error(),
		"the message the stub already showed users is unchanged; only its identity is new")
}
