//go:build testing

package plus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// One TestMain, so no test in this package has to learn where the machine
// seams are before it can ask whether a duty is allowed. The tests below all
// name an explicit Source; this exists so the calibrated fixture doubles as the
// machine the rest of the package's tests resolve against.
func TestMain(m *testing.M) {
	for key, value := range map[string]string{
		profilesEnv: filepath.Join("testdata", "profile", "profiles.json"),
		dmiRootEnv:  filepath.Join("testdata", "profile", "machine", "dmi"),
		hwmonEnv:    filepath.Join("testdata", "profile", "machine", "hwmon"),
		sysfsEnv:    filepath.Join("testdata", "profile", "machine", "sysfs"),
	} {
		if err := os.Setenv(key, value); err != nil {
			panic(err)
		}
	}
	os.Exit(m.Run())
}

const profileTD = "testdata/profile"

func machineSource() Source {
	return Source{
		Profiles: filepath.Join(profileTD, "profiles.json"),
		DMI:      filepath.Join(profileTD, "machine", "dmi"),
		Hwmon:    filepath.Join(profileTD, "machine", "hwmon"),
		Sysfs:    filepath.Join(profileTD, "machine", "sysfs"),
	}
}

func setEnv(t *testing.T, s Source) {
	t.Helper()
	t.Setenv(profilesEnv, s.Profiles)
	t.Setenv(dmiRootEnv, s.DMI)
	t.Setenv(hwmonEnv, s.Hwmon)
	t.Setenv(sysfsEnv, s.Sysfs)
}

// The readers below stand in for an operator with a shell: they open the same
// files the resolver opens and read them without this package's parsing, so an
// expectation is the machine's own text rather than this fork's opinion of it.

func sensorMaxMilliC(t *testing.T, hwmonRoot, label string) uint64 {
	t.Helper()
	dirs, err := filepath.Glob(filepath.Join(hwmonRoot, "hwmon*"))
	require.NoError(t, err)
	for _, dir := range dirs {
		labels, err := filepath.Glob(filepath.Join(dir, "temp*_label"))
		require.NoError(t, err)
		for _, labelFile := range labels {
			raw, err := os.ReadFile(labelFile)
			require.NoError(t, err)
			if strings.TrimSpace(string(raw)) != label {
				continue
			}
			raw, err = os.ReadFile(strings.TrimSuffix(labelFile, "_label") + "_max")
			require.NoError(t, err)
			n, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
			require.NoError(t, err)
			return n
		}
	}
	t.Fatalf("the fixture publishes no sensor labelled %q under %s", label, hwmonRoot)
	return 0
}

func fpsDuties(t *testing.T, sysfsRoot string) []int {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(sysfsRoot, "bus", "acpi", "devices", "*", "state*"))
	require.NoError(t, err)
	var out []int
	for _, match := range matches {
		raw, err := os.ReadFile(match)
		require.NoError(t, err)
		pct, err := strconv.Atoi(strings.TrimSpace(strings.SplitN(strings.TrimSpace(string(raw)), ":", 2)[0]))
		require.NoError(t, err)
		if pct > 0 {
			out = append(out, pct)
		}
	}
	return out
}

// catalogueDuties reads one entry's static duties with a shape of its own, so
// the expectation does not come from the struct the resolver parses into.
func catalogueDuties(t *testing.T, path string, index int) []int {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var shape struct {
		Profiles []struct {
			Fan struct {
				StaticDuties []int `json:"static_duties"`
			} `json:"fan"`
		} `json:"profiles"`
	}
	require.NoError(t, json.Unmarshal(raw, &shape))
	require.Greater(t, len(shape.Profiles), index)
	return shape.Profiles[index].Fan.StaticDuties
}

func writeCatalogue(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profiles.json")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}

func intersect(a, b []int) []int {
	inB := make(map[int]bool, len(b))
	for _, v := range b {
		inB[v] = true
	}
	var out []int
	for _, v := range a {
		if inB[v] {
			out = append(out, v)
		}
	}
	return out
}

func goldenCurve(t *testing.T) *Curve {
	t.Helper()
	c, err := ParseCurve(golden(t, "pfand.conf"))
	require.NoError(t, err)
	return c
}

func everyPublishedDuty() []int {
	return []int{25, 30, 40, 50, 60, 70, 80, 85, 90, 95, 100}
}

// --- DMI matching --------------------------------------------------------

// Every dimension a profile names has to agree, and a dimension it leaves out
// is not a condition. The two entries in the fixture name different numbers of
// them, and which one answers is visible in the duties that come back.
func TestANamedDimensionMustAgreeAndAnOmittedOneIsNotACondition(t *testing.T) {
	id := readIdentity(filepath.Join(profileTD, "machine", "dmi"))

	ok, why := Profile{Match: map[string]string{
		dmiSysVendor:   "Panasonic Corporation",
		dmiProductName: "CFQV8-1",
		dmiProductVer:  "001",
	}}.matches(id)
	assert.True(t, ok, "a profile naming what the machine publishes must match it")
	assert.Empty(t, why)

	ok, why = Profile{Match: map[string]string{dmiProductName: "CFQV8-1"}}.matches(id)
	assert.True(t, ok, "the dimensions a profile omits are not conditions")

	ok, why = Profile{Match: map[string]string{
		dmiProductName: "CFQV8-1",
		dmiProductVer:  "002",
	}}.matches(id)
	assert.False(t, ok, "one dimension disagreeing is a miss, not a near miss")
	assert.Contains(t, why, "001", "the refusal has to name the number that disagreed")
	assert.Contains(t, why, "002")
}

func TestAMatchOnAFieldTheMachineDoesNotPublishIsRefused(t *testing.T) {
	partial := Identity{dmiProductName: "CFQV8-1"}

	ok, why := Profile{Match: map[string]string{
		dmiProductName: "CFQV8-1",
		dmiProductVer:  "001",
	}}.matches(partial)
	assert.False(t, ok, "an absent dimension and a disagreeing one are different facts")
	assert.Contains(t, why, "does not publish")

	ok, why = Profile{Match: map[string]string{"product": "CFQV8-1"}}.matches(partial)
	assert.False(t, ok, "a misspelled key compares nothing and would claim every machine")
	assert.Contains(t, why, "not an identity field")

	ok, _ = Profile{Match: map[string]string{dmiProductName: "  "}}.matches(partial)
	assert.False(t, ok, "an empty value is a condition nothing satisfies, not an omitted one")

	ok, _ = Profile{Match: map[string]string{}}.matches(partial)
	assert.False(t, ok, "a profile naming nothing would claim every machine")
}

// The catalogue is ordered and the first entry that agrees answers, so a
// catalogue that puts the broad entry first answers differently from one that
// does not. That is the only thing the order is for.
func TestOrderDecidesWhichProfileAnswers(t *testing.T) {
	specific, err := resolveDutySet(machineSource())
	require.NoError(t, err)

	broad, err := resolveDutySet(Source{
		Profiles: writeCatalogue(t, `{"profiles":[
			{"match":{"product_name":"CFQV8-1"},
			 "fan":{"static_duties":[25,30],"min_duty":25,"max_duty":100},
			 "thermal":{"max_temp_c":100,"max_temp_label":"Package id 0"}},
			{"match":{"sys_vendor":"Panasonic Corporation","product_name":"CFQV8-1",
			          "product_version":"001"},
			 "fan":{"static_duties":[25,30],"min_duty":25,"max_duty":100},
			 "thermal":{"max_temp_c":100,"max_temp_label":"Package id 0"}}]}`),
		DMI:   machineSource().DMI,
		Hwmon: machineSource().Hwmon,
		Sysfs: machineSource().Sysfs,
	})
	require.NoError(t, err)

	assert.True(t, broad.duties[0] < specific.duties[len(specific.duties)-1],
		"the broad entry answers when it comes first, so its shorter ladder is the one resolved")
	assert.NotEqual(t, len(specific.duties), len(broad.duties))
}

// --- the catalogue is a file, and it is strict ---------------------------

func TestACatalogueIsRefusedRatherThanPartlyRead(t *testing.T) {
	for name, body := range map[string]string{
		"misspelled duty key": `{"profiles":[{"match":{"product_name":"X"},
			"fan":{"static_duts":[25],"min_duty":25,"max_duty":100},
			"thermal":{"max_temp_c":100,"max_temp_label":"P"}}]}`,
		"misspelled ceiling": `{"profiles":[{"match":{"product_name":"X"},
			"fan":{"static_duties":[25],"min_duty":25,"max_duty":100},
			"thermal":{"max_temp_c":100,"max_temp_lable":"P"}}]}`,
		"not an object": `[{"match":{"product_name":"X"}}]`,
		"no duty list": `{"profiles":[{"match":{"product_name":"X"},
			"fan":{"min_duty":25,"max_duty":100}}]}`,
		"duty outside the range it declares": `{"profiles":[{"match":{"product_name":"X"},
			"fan":{"static_duties":[20],"min_duty":25,"max_duty":100}}]}`,
		"duty listed twice": `{"profiles":[{"match":{"product_name":"X"},
			"fan":{"static_duties":[25,25],"min_duty":25,"max_duty":100}}]}`,
		"empty duty range": `{"profiles":[{"match":{"product_name":"X"},
			"fan":{"static_duties":[25],"min_duty":100,"max_duty":25}}]}`,
		"no identity field": `{"profiles":[{"match":{},
			"fan":{"static_duties":[25],"min_duty":25,"max_duty":100}}]}`,
		"misspelled identity key": `{"profiles":[{"match":{"product":"X"},
			"fan":{"static_duties":[25],"min_duty":25,"max_duty":100}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadProfiles(writeCatalogue(t, body))
			require.Error(t, err, "a catalogue this fork cannot read whole is not a catalogue")
			assert.ErrorIs(t, err, ErrPlatformUnknown)
		})
	}

	_, err := loadProfiles(filepath.Join(profileTD, "no-such-catalogue.json"))
	assert.ErrorIs(t, err, ErrPlatformUnknown)
}

// --- the duty whitelist is an intersection -------------------------------

func TestTheWhitelistIsWhatBothSourcesAgreeOn(t *testing.T) {
	set, err := resolveDutySet(machineSource())
	require.NoError(t, err)

	want := intersect(
		catalogueDuties(t, filepath.Join(profileTD, "profiles.json"), 0),
		fpsDuties(t, filepath.Join(profileTD, "machine", "sysfs")))
	require.NotEmpty(t, want)
	for _, duty := range want {
		assert.True(t, set.Has(duty), "%d is named by the profile and published by the firmware", duty)
		assert.NoError(t, ValidateDuty(duty, set))
	}
	assert.Len(t, set.duties, len(want),
		"a duty only one source names is still a duty the write path must refuse")
}

// The short table is the discriminating case: the profile would admit ten
// duties and the firmware publishes five, so a resolver that took either half
// alone would be visibly wrong.
func TestAShorterFirmwareTableShrinksTheWhitelistToWhatItPublishes(t *testing.T) {
	s := machineSource()
	s.Sysfs = filepath.Join(profileTD, "shorttable", "sysfs")
	set, err := resolveDutySet(s)
	require.NoError(t, err)

	for _, duty := range fpsDuties(t, s.Sysfs) {
		assert.True(t, set.Has(duty), "%d is the only half of the intersection that can widen it", duty)
	}
	assert.False(t, set.Has(85),
		"the profile offers 85 and this table does not publish it, so the intersection may not")
	assert.Less(t, len(set.duties), len(everyPublishedDuty()))
}

// --- the refusals --------------------------------------------------------

// Row one: no profile claims this machine, so the whitelist is empty and every
// write is refused. The refusal is the behaviour; the message is a courtesy.
func TestAMachineNoProfileClaimsIsRefusedEveryWrite(t *testing.T) {
	s := machineSource()
	s.DMI = filepath.Join(profileTD, "elsewhere", "dmi")

	set, err := resolveDutySet(s)
	require.ErrorIs(t, err, ErrPlatformUnknown)
	assert.Empty(t, set.duties)
	assert.Contains(t, err.Error(), "Lenovo",
		"the refusal has to name what the machine says, or the operator is left guessing which file to edit")

	for _, duty := range everyPublishedDuty() {
		assert.Error(t, ValidateDuty(duty, set), "%d is a duty everywhere but here", duty)
	}
}

// Row two: the profile is here and the firmware table is not, so the
// intersection has one known half. Half a whitelist is not a short whitelist.
func TestAMachineWithNoFirmwareTableIsRefusedEveryWrite(t *testing.T) {
	s := machineSource()
	s.Sysfs = filepath.Join(profileTD, "nowhere", "sysfs")

	set, err := resolveDutySet(s)
	require.ErrorIs(t, err, ErrDutySetUnknown)
	assert.Contains(t, err.Error(), "firmware half", "the refusal has to say which half is missing")
	for _, duty := range everyPublishedDuty() {
		assert.Error(t, ValidateDuty(duty, set))
	}
}

// Two sources that share nothing resolve to an empty set, and an empty set
// admits nothing: the profile half alone would have admitted all of them.
func TestDisjointHalvesResolveToNothingRatherThanToEither(t *testing.T) {
	s := machineSource()
	s.Profiles = writeCatalogue(t, `{"profiles":[{"match":{"product_name":"CFQV8-1"},
		"fan":{"static_duties":[33,66],"min_duty":25,"max_duty":100},
		"thermal":{"max_temp_c":100,"max_temp_label":"Package id 0"}}]}`)

	set, err := resolveDutySet(s)
	require.NoError(t, err, "both halves were present; they simply agree on nothing")
	assert.Empty(t, set.duties)
	for _, duty := range []int{25, 33, 66, 100} {
		assert.Error(t, ValidateDuty(duty, set),
			"nothing about an empty intersection makes a duty admissible")
	}
}

// ValidateLevel is the name the write path calls, so it has to carry the same
// refusals rather than a whitelist of its own.
func TestTheCallersGateRefusesWheneverASourceIsMissing(t *testing.T) {
	cases := map[string]func(*Source){
		"no profile":   func(s *Source) { s.DMI = filepath.Join(profileTD, "elsewhere", "dmi") },
		"no fan table": func(s *Source) { s.Sysfs = filepath.Join(profileTD, "nowhere", "sysfs") },
		"halves disjoint": func(s *Source) {
			s.Profiles = writeCatalogue(t, `{"profiles":[{"match":{"product_name":"CFQV8-1"},
				"fan":{"static_duties":[33,66],"min_duty":25,"max_duty":100}}]}`)
		},
	}
	for name, break_ := range cases {
		t.Run(name, func(t *testing.T) {
			s := machineSource()
			break_(&s)
			setEnv(t, s)
			for _, duty := range everyPublishedDuty() {
				assert.Error(t, ValidateLevel(duty))
			}
		})
	}

	t.Run("calibrated", func(t *testing.T) {
		setEnv(t, machineSource())
		assert.NoError(t, ValidateLevel(50))
		assert.Error(t, ValidateLevel(51))
	})
}

// --- the thermal ceiling is selected by label ----------------------------

// The fixture puts the sensor with the low maximum first and the sensor with
// the real ceiling second, so the two orders disagree. Directory order is not a
// ranking: on this machine hwmon1 is a disk controller whose alarm sits well
// below the package's own limit, and a resolver that read whichever sensor it
// met first would refuse a curve the machine is running.
func TestTheCeilingIsSelectedByLabelNotBySensorOrder(t *testing.T) {
	root := filepath.Join(profileTD, "machine", "hwmon")
	named := sensorMaxMilliC(t, root, "Package id 0")
	first := sensorMaxMilliC(t, root, "Composite")
	require.NotEqual(t, named, first,
		"the fixture only proves anything while the two candidates disagree")

	ceiling, err := machineSource().ThermalCeiling()
	require.NoError(t, err)
	assert.Equal(t, int(named/1000), ceiling,
		"the ceiling is the sensor the profile names, read off the machine")
	assert.NotEqual(t, int(first/1000), ceiling,
		"and emphatically not the sensor the enumeration reached first")
}

// A sensor that publishes a fraction of a degree floors rather than rounds: one
// tripping at 82.85C is a ceiling of 82, because rounding would admit an 83 the
// machine has already passed.
func TestAFractionalCeilingIsFloored(t *testing.T) {
	s := machineSource()
	s.Hwmon = filepath.Join(profileTD, "machine", "hwmon")
	s.Profiles = writeCatalogue(t, `{"profiles":[{"match":{"product_name":"CFQV8-1"},
		"fan":{"static_duties":[25],"min_duty":25,"max_duty":100},
		"thermal":{"max_temp_label":"Composite"}}]}`)

	raw := sensorMaxMilliC(t, s.Hwmon, "Composite")
	require.NotZero(t, raw%1000, "the fixture only proves flooring with a fractional sensor")

	ceiling, err := s.ThermalCeiling()
	require.NoError(t, err)
	assert.Equal(t, int(raw/1000), ceiling)
	assert.Less(t, ceiling, int((raw+999)/1000), "rounding up would name a temperature the sensor has passed")
}

// Row four: the profile names a sensor and this machine has none carrying that
// label, so the curve has no ceiling and the write is refused rather than
// bounded by a default.
func TestACurveIsRefusedWhenTheNamedLabelIsNotOnAnySensor(t *testing.T) {
	s := machineSource()
	s.Hwmon = filepath.Join(profileTD, "unlabelled", "hwmon")

	_, err := s.ThermalCeiling()
	assert.ErrorIs(t, err, ErrThermalCeilingUnknown)
	assert.Contains(t, err.Error(), "Package id 0", "the refusal names the label that was looked for")

	setEnv(t, s)
	c := goldenCurve(t)
	assert.Error(t, c.Validate(), "an unbounded curve is not one this machine may be handed")
	assert.ErrorIs(t, ValidateBytes([]byte(c.Render())), ErrThermalCeilingUnknown,
		"the byte gate is the same gate, so a render cannot slip past a refusal")
}

func TestAProfileWithNoSensorLabelResolvesNoCeiling(t *testing.T) {
	s := machineSource()
	s.Profiles = writeCatalogue(t, `{"profiles":[{"match":{"product_name":"CFQV8-1"},
		"fan":{"static_duties":[25],"min_duty":25,"max_duty":100},
		"thermal":{"max_temp_c":100}}]}`)

	_, err := s.ThermalCeiling()
	assert.ErrorIs(t, err, ErrThermalCeilingUnknown)
	assert.Contains(t, err.Error(), "names no sensor")
}

// Two sockets publish one label and two ceilings. Choosing the friendlier one
// is a guess about which core the operator meant.
func TestTwoSensorsCarryingOneLabelAreRefusedNotPicked(t *testing.T) {
	s := machineSource()
	s.Hwmon = filepath.Join(profileTD, "dual", "hwmon")

	_, err := s.ThermalCeiling()
	assert.ErrorIs(t, err, ErrThermalCeilingUnknown)
	assert.Contains(t, err.Error(), "no single ceiling")
}

// A profile left behind by a board swap must not bound a curve by the number it
// remembers: the sensor and the file have to agree or neither is believed.
func TestAProfileThatDisagreesWithItsSensorIsRefused(t *testing.T) {
	s := machineSource()
	s.Hwmon = filepath.Join(profileTD, "shifted", "hwmon")

	_, err := s.ThermalCeiling()
	assert.ErrorIs(t, err, ErrThermalCeilingUnknown)
	assert.Contains(t, err.Error(), "states 100C")
}

// The ceiling is read rather than assumed: the same curve is admitted against a
// machine whose sensor says 100 and refused against one that says 85.
func TestTheEmergencyEdgeFollowsTheSensorRatherThanAFixedCeiling(t *testing.T) {
	c := goldenCurve(t)
	c.Emerg = 90

	setEnv(t, machineSource())
	assert.NoError(t, c.Validate(), "a sensor at 100C admits an emergency edge at 90")

	s := machineSource()
	s.Hwmon = filepath.Join(profileTD, "tight", "hwmon")
	s.Profiles = writeCatalogue(t, `{"profiles":[{"match":{"product_name":"CFQV8-1"},
		"fan":{"static_duties":[25],"min_duty":25,"max_duty":100},
		"thermal":{"max_temp_c":85,"max_temp_label":"Package id 0"}}]}`)
	setEnv(t, s)
	assert.Error(t, c.Validate(),
		"the same edge past a ceiling the machine actually published is unreachable there")
}

// --- the literal the panel mirror is parsed out of -----------------------

// That literal is no longer the gate, so nothing would catch it drifting from
// the machine except this. It is the third link in one chain: the fixture, the
// Go literal, and the TypeScript mirror that reads the literal out of the
// source text.
func TestThePanelLiteralStillDescribesTheCalibratedMachine(t *testing.T) {
	set, err := resolveDutySet(machineSource())
	require.NoError(t, err)

	for duty := range validLevels {
		assert.True(t, set.Has(duty),
			"%d is in the literal the panel is built from but not in this machine's whitelist", duty)
	}
	assert.Len(t, set.duties, len(validLevels),
		"the calibrated machine resolves exactly the ladder the panel offers")
}

// A catalogue written before schema_version existed keeps working: the field is
// additive and unstated means version 0.
func TestProfilesWithoutSchemaVersionStillLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profiles.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"profiles":[{"match":{"product_name":"Test"},
		"fan":{"static_duties":[40],"min_duty":20,"max_duty":100},
		"thermal":{"max_temp_c":90,"max_temp_label":"Tctl"}}]}`), 0o600))

	set, err := loadProfiles(path)
	require.NoError(t, err)
	assert.Zero(t, set.SchemaVersion)
	require.Len(t, set.Profiles, 1)
}

// A file from a future build states a version this one cannot parse, and must be
// refused rather than silently read as the current shape.
func TestProfilesWithAnUnknownSchemaVersionAreRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profiles.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"schema_version":99,"profiles":[]}`), 0o600))

	_, err := loadProfiles(path)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPlatformUnknown)
	assert.Contains(t, err.Error(), "schema_version 99")
}

// The declared version is accepted.
func TestProfilesWithTheCurrentSchemaVersionLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profiles.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"schema_version":1,"profiles":[{"match":{"product_name":"T"},
		"fan":{"static_duties":[40],"min_duty":20,"max_duty":100},
		"thermal":{"max_temp_c":90,"max_temp_label":"Tctl"}}]}`), 0o600))

	set, err := loadProfiles(path)
	require.NoError(t, err)
	assert.Equal(t, 1, set.SchemaVersion)
}
