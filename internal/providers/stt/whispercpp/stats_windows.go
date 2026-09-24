//go:build windows

package whispercpp

import (
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var getMemory = syscall.NewLazyDLL("psapi.dll").NewProc("GetProcessMemoryInfo")

type processMemory struct {
	Size, PageFaults                                                                                                 uint32
	PeakWorkingSet, WorkingSet, QuotaPeakPaged, QuotaPaged, QuotaPeakNonPaged, QuotaNonPaged, Pagefile, PeakPagefile uintptr
}
type processStats struct {
	mu         sync.Mutex
	handle     syscall.Handle
	peak       *uint64
	cpu        *float64
	stop, done chan struct{}
}

func newProcessStats(pid int) *processStats {
	h, e := syscall.OpenProcess(0x0400|0x0010, false, uint32(pid))
	if e != nil {
		return nil
	}
	s := &processStats{handle: h, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.sample()
			case <-s.stop:
				s.sample()
				return
			}
		}
	}()
	return s
}
func (s *processStats) sample() {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := processMemory{}
	m.Size = uint32(unsafe.Sizeof(m))
	ok, _, _ := getMemory.Call(uintptr(s.handle), uintptr(unsafe.Pointer(&m)), uintptr(m.Size))
	if ok != 0 {
		peak := uint64(m.PeakWorkingSet)
		if s.peak == nil || peak > *s.peak {
			s.peak = &peak
		}
	}
	var created, exit, kernel, user syscall.Filetime
	if syscall.GetProcessTimes(s.handle, &created, &exit, &kernel, &user) == nil {
		cpu := float64(uint64(kernel.HighDateTime)<<32|uint64(kernel.LowDateTime))/1e7 + float64(uint64(user.HighDateTime)<<32|uint64(user.LowDateTime))/1e7
		s.cpu = &cpu
	}
}
func (s *processStats) snapshot() (*uint64, *float64) {
	if s == nil {
		return nil, nil
	}
	s.sample()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peak, s.cpu
}
func (s *processStats) close() {
	if s == nil {
		return
	}
	close(s.stop)
	<-s.done
	_ = syscall.CloseHandle(s.handle)
}
