//go:build !unix

package signingkey

import "errors"

// errPermissionCheckUnsupported reports ADR-025 decision 4's ownership
// intent honestly on a platform outside the "unix" build tag set (notably
// Windows, which has no POSIX file mode or uid concept to check): rather
// than silently skip the check and let a warning-free log read as if it
// passed, [checkFilePermissions] always reports this condition so an
// operator on such a platform knows the guarantee has not been verified.
// ShowMesh targets linux and darwin for the coordinator (ADR-012); this
// exists so the build itself still succeeds elsewhere, matching
// pkg/multisync/sockopts_other.go's identical precedent.
var errPermissionCheckUnsupported = errors.New("signingkey: key file permission/ownership check is not supported on this platform")

func checkFilePermissions(_ string) error {
	return errPermissionCheckUnsupported
}
