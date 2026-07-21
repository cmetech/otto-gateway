// internal/plugin/compress/testmain_test.go
// Package compress — whitebox test file.
//
// TestMain installs the goleak goroutine-leak gate for the entire compress
// package test suite (mirrors internal/plugin/testmain_test.go).
package compress

import (
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
