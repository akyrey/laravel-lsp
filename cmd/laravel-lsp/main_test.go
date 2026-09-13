package main

import "testing"

func TestResolveVersion_PrefersLinkTimeStamp(t *testing.T) {
	// Arrange
	orig := version
	t.Cleanup(func() { version = orig })
	version = "v1.2.3"

	// Act
	got := resolveVersion()

	// Assert
	if got != "v1.2.3" {
		t.Fatalf("resolveVersion() = %q, want %q", got, "v1.2.3")
	}
}

func TestResolveVersion_FallsBackWhenUnstamped(t *testing.T) {
	// Arrange
	orig := version
	t.Cleanup(func() { version = orig })
	version = ""

	// Act
	got := resolveVersion()

	// Assert: the exact value depends on how the test binary was built, but it
	// must never be empty and must never leak the toolchain's "(devel)" marker.
	if got == "" {
		t.Fatal("resolveVersion() returned an empty string")
	}
	if got == "(devel)" {
		t.Fatalf("resolveVersion() leaked the toolchain placeholder %q", got)
	}
}
