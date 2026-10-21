package benchmark

import "math"

// wilsonZ95 is the normal quantile for a 95% two-sided interval.
const wilsonZ95 = 1.959963984540054

// Wilson returns the 95% Wilson score interval for pass successes in n trials.
// n <= 0 is the full interval (0, 1): there is no evidence yet.
func Wilson(pass, n int) (lo, hi float64) {
	if n <= 0 {
		return 0, 1
	}
	if pass < 0 {
		pass = 0
	}
	if pass > n {
		pass = n
	}
	z2 := wilsonZ95 * wilsonZ95
	nf := float64(n)
	p := float64(pass) / nf
	denom := 1 + z2/nf
	center := (p + z2/(2*nf)) / denom
	margin := wilsonZ95 * math.Sqrt(p*(1-p)/nf+z2/(4*nf*nf)) / denom
	lo = center - margin
	hi = center + margin
	if lo < 0 {
		lo = 0
	}
	if hi > 1 {
		hi = 1
	}
	return lo, hi
}
