//go:build cgo && linux && (arm64 || riscv64 || loong64)

package gstengine

import "syscall"

// dupOnto duplicates fd onto target, closing whatever target previously
// referred to. syscall.Dup2 is unavailable on this architecture;
// Dup3 with flags 0 is the equivalent.
func dupOnto(fd, target int) error {
	return syscall.Dup3(fd, target, 0)
}
