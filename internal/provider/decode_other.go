//go:build !windows

package provider

// toUTF8 is a no-op off Windows: a child process's output is already UTF-8 on
// Linux/macOS, so no code-page transcoding is needed.
func toUTF8(b []byte) string { return string(b) }
