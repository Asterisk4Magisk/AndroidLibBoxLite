package environment

import (
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
)

// The Go runtime reads these before package initialization, so restoring os's
// environment alone cannot apply the standalone process's resource settings.
func applyRuntimeLimits() {
	if value, present := os.LookupEnv("GOGC"); present {
		percent := int64(100)
		if value == "off" {
			percent = -1
		} else if parsed, err := strconv.ParseInt(value, 10, 32); err == nil {
			percent = parsed
		}
		debug.SetGCPercent(int(percent))
	}
	if value, present := os.LookupEnv("GOMEMLIMIT"); present {
		limit := int64(math.MaxInt64)
		if value != "" && value != "off" {
			var ok bool
			limit, ok = parseMemoryLimit(value)
			if !ok {
				panic("malformed GOMEMLIMIT; see go doc runtime/debug.SetMemoryLimit")
			}
		}
		debug.SetMemoryLimit(limit)
	}
	if value, err := strconv.ParseInt(os.Getenv("GOMAXPROCS"), 10, 32); err == nil && value > 0 {
		runtime.GOMAXPROCS(int(value))
	}
}

func parseMemoryLimit(value string) (int64, bool) {
	multiplier := int64(1)
	for _, unit := range []struct {
		suffix string
		bytes  int64
	}{
		{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}, {"TiB", 1 << 40}, {"B", 1},
	} {
		if strings.HasSuffix(value, unit.suffix) {
			value = strings.TrimSuffix(value, unit.suffix)
			multiplier = unit.bytes
			break
		}
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 0 || n > math.MaxInt64/multiplier {
		return 0, false
	}
	return n * multiplier, true
}
