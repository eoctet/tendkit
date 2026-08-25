package runtime

import "testing"

func TestExpandPathExpandsEnvironment(t *testing.T) {
	t.Setenv("TENDKIT_PATH_TEST", "expanded")
	if got := ExpandPath("$TENDKIT_PATH_TEST/logs"); got != "expanded/logs" {
		t.Fatalf("ExpandPath() = %q", got)
	}
}
