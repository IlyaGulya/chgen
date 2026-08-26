package main

import (
	"runtime/debug"
	"testing"
)

func TestResolvedVersionUsesLinkedVersionFirst(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Version: "v2.0.0"}}
	if got := resolvedVersion("v1.2.3", info, true); got != "v1.2.3" {
		t.Fatalf("build version = %q, want v1.2.3", got)
	}
}

func TestResolvedVersionUsesModuleVersion(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}}
	if got := resolvedVersion("dev", info, true); got != "v1.2.3" {
		t.Fatalf("build version = %q, want v1.2.3", got)
	}
}

func TestResolvedVersionHasDevelopmentFallback(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}
	if got := resolvedVersion("dev", info, true); got != "dev" {
		t.Fatalf("build version = %q, want dev", got)
	}
}
