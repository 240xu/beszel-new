# M7b gate — CI round 2 (fixes from the first real CI run)

status: **green (pending CI confirmation)**
found by CI round 1:
- lint: 2 new U1000/unused findings in internal/hub/api_plus.go -> the compatibility
  wrappers readFanBody/strictJSON had no callers after the typed rename. Deleted them
  and restored the plain names; no wrapper layer left behind.
- windows/amd64 + windows/arm64: `agent/smart_windows.go:13:12: pattern
  smartmontools/smartctl.exe: no matching files found`. The LHM build step was not
  enough: the Windows agent embeds two artifacts. Added
  `go generate -run fetchsmartctl ./agent` to the Windows job, which is the same
  pinned-SHA fetch the release uses.
evidence:
- smartctl fetch verified locally: sha256 3912249c...c30b47 matches the pinned hash
- go build ./... and go vet ./internal/hub after removing the wrappers: clean
- YAML re-parsed
open:
- the Windows job still cannot be run locally (no dotnet on wuming), so CI is the only
  verifier for that cell
