//go:build linux

package ui

import (
	"syscall"
	"testing"
)

func TestLinuxFDRangeRejectsUpperBound(t *testing.T) {
	upperBound := len((syscall.FdSet{}).Bits) * 64
	if linuxFDInRange(upperBound) {
		t.Fatalf("fd %d was accepted", upperBound)
	}
	if !linuxFDInRange(upperBound - 1) {
		t.Fatalf("fd %d was rejected", upperBound-1)
	}
}
