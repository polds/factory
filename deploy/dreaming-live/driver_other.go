//go:build !linux

package dreaminglive

import "errors"

func ReexecCleanDriver() error {
	return errors.New("driver re-exec requires linux")
}

func driverAllowEnv(src []string) []string {
	return nil
}
