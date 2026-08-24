//go:build cgo && !(linux && (arm64 || riscv64 || loong64))

package gstengine

import "syscall"

// dupOnto duplicates fd onto target, closing whatever target previously
// referred to. syscall.Dup2 does not exist on some 64-bit linux
// architectures (arm64, riscv64, loong64); see teardownrace_dup3_test.go
// for the Dup3-based equivalent used there.
func dupOnto(fd, target int) error {
	return syscall.Dup2(fd, target)
}
