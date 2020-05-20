// +build windows

package rlimit

// On unix, we need to raise the limit on the number of open files.
// Unsure if anything is required on windows, or if bazel-remote even
// works on windows. But let's not intentionally prevent compiling
// for windows.
func Raise() {
}
