//go:build !windows

package whispercpp

type processStats struct{}

func newProcessStats(int) *processStats             { return nil }
func (*processStats) snapshot() (*uint64, *float64) { return nil, nil }
func (*processStats) close()                        {}
