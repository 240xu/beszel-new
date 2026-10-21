//go:build testing

package fanapi

import (
	"errors"
	"testing"
	"time"

	"github.com/henrygd/beszel/agent/plus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeBackend struct {
	temp     int
	tempErr  error
	level    int
	levelErr error
	pfand    bool
	duties   []int
	curve    *plus.Curve
	raw      []byte
	written  []byte
}

func (f *fakeBackend) ReadTempC() (int, error) { return f.temp, f.tempErr }
func (f *fakeBackend) SetLevel(int) error      { return nil }
func (f *fakeBackend) GetLevel() (int, error)  { return f.level, f.levelErr }
func (f *fakeBackend) PfandUp() bool           { return f.pfand }
func (f *fakeBackend) ResolveDutySet() (plus.DutySet, error) {
	return plus.DutySetFrom(f.duties), nil
}
func (f *fakeBackend) ReadCurve() (*plus.Curve, error) { return f.curve, nil }
func (f *fakeBackend) ReadCurveBytes() ([]byte, error) { return f.raw, nil }
func (f *fakeBackend) WriteCurveBytes(b []byte) error  { f.written = b; return nil }

func TestDepsFromBackendBindsEverySeam(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	f := &fakeBackend{temp: 46, level: 70, pfand: true, duties: []int{0, 50, 70, 100}}
	d := DepsFromBackend(f, func() time.Time { return now }, nil, nil)

	gotTemp, err := d.ReadTempC()
	require.NoError(t, err)
	assert.Equal(t, 46, gotTemp)

	gotLevel, err := d.GetLevel()
	require.NoError(t, err)
	assert.Equal(t, 70, gotLevel)

	assert.True(t, d.PfandUp())
	assert.False(t, d.Now().IsZero())

	set, err := d.ResolveDutySet()
	require.NoError(t, err)
	assert.True(t, set.Has(70))
	assert.False(t, set.Has(80), "a duty outside the resolved set must stay outside")

	// The audit/after seams must never be nil, or the guard panics on a write.
	assert.NotNil(t, d.Audit)
	assert.NotNil(t, d.AfterFunc)
}

func TestDepsFromBackendPropagatesReadErrors(t *testing.T) {
	boom := errors.New("sensor gone")
	f := &fakeBackend{tempErr: boom, levelErr: boom}
	d := DepsFromBackend(f, nil, nil, nil)

	_, err := d.ReadTempC()
	assert.ErrorIs(t, err, boom)
	_, err = d.GetLevel()
	assert.ErrorIs(t, err, boom)
}
