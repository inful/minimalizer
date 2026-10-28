package main

import (
	"strings"
	"testing"
)

func TestLoaderEnv(t *testing.T) {
	env := loaderEnv([]string{"LD_LIBRARY_PATH=/opt/lib", "LD_PRELOAD=/x.so", "LD_AUDIT=/y.so", "HOME=/root", "PATH=/bin"})
	for _, e := range env {
		if strings.HasPrefix(e, "LD_") {
			t.Errorf("loaderEnv() kept %q", e)
		}
	}
	if len(env) != 2 {
		t.Errorf("loaderEnv() = %v, want the two other variables", env)
	}
}
