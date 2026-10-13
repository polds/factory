package harness

import (
	"fmt"
	"strconv"
)

const defaultMaxTurns = 8

func maxTurnsFrom(env []string) (int, error) {
	raw := lookupEnv(env, "FACTORY_MAX_TURNS")
	if raw == "" {
		return defaultMaxTurns, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 200 {
		return 0, fmt.Errorf("must be an integer in 1..200, got %q", raw)
	}
	return n, nil
}
