package plus

import "errors"

// Reject reasons a fan write can carry: named so Task 15 switches on them rather
// than substring-matching English. See the tests for which are reachable where.
var (
	ErrFanLevelNotAllowed    = errors.New("fan duty is not in the firmware whitelist")
	ErrPfandActive           = errors.New("pfand owns the fan curve")
	ErrPackageTemperature    = errors.New("package temperature is at or above the guard limit")
	ErrThermalProfileT4      = errors.New("thermal profile T4 is not modifiable")
	ErrFanControlUnsupported = errors.New("ACPI fan control is only implemented on linux")
)
