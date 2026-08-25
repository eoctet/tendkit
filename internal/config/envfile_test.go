package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadParsesSupportedDotenvSyntax(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	content := "\ufeff# comment\n" +
		"ENVFILE_TEST_PLAIN=value\n" +
		"export ENVFILE_TEST_EXPORTED = 'quoted value'\n" +
		"ENVFILE_TEST_DOUBLE=\"value # retained\"\n" +
		"ENVFILE_TEST_COMMENT=value # removed\n" +
		"ENVFILE_TEST_EMPTY=\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	keys := []string{"ENVFILE_TEST_PLAIN", "ENVFILE_TEST_EXPORTED", "ENVFILE_TEST_DOUBLE", "ENVFILE_TEST_COMMENT", "ENVFILE_TEST_EMPTY"}
	for _, key := range keys {
		_ = os.Unsetenv(key)
		t.Cleanup(func() { _ = os.Unsetenv(key) })
	}

	result, err := LoadEnvFile(path, true, ".env")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Exists || result.Loaded != len(keys) {
		t.Fatalf("unexpected result: %+v", result)
	}
	want := map[string]string{
		"ENVFILE_TEST_PLAIN": "value", "ENVFILE_TEST_EXPORTED": "quoted value",
		"ENVFILE_TEST_DOUBLE": "value # retained", "ENVFILE_TEST_COMMENT": "value", "ENVFILE_TEST_EMPTY": "",
	}
	for key, expected := range want {
		if actual, exists := os.LookupEnv(key); !exists || actual != expected {
			t.Errorf("%s=%q, exists=%v; want %q", key, actual, exists, expected)
		}
	}
}

func TestLoadDoesNotOverrideProcessEnvironment(t *testing.T) {
	const key = "ENVFILE_TEST_PRECEDENCE"
	t.Setenv(key, "process-value")
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(key+"='file-value'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := LoadEnvFile(path, true, ".env")
	if err != nil {
		t.Fatal(err)
	}
	if result.Loaded != 0 || os.Getenv(key) != "process-value" {
		t.Fatalf("process environment was overridden: result=%+v value=%q", result, os.Getenv(key))
	}
}

func TestLoadMissingFileRequiredPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.env")
	result, err := LoadEnvFile(path, false, ".env")
	if err != nil || result.Exists || result.Loaded != 0 {
		t.Fatalf("optional missing file: result=%+v err=%v", result, err)
	}
	if _, err := LoadEnvFile(path, true, ".env"); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("expected required-file error, got %v", err)
	}
}

func TestLoadRejectsInvalidContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("INVALID LINE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadEnvFile(path, true, ".env"); err == nil || !strings.Contains(err.Error(), "缺少 '='") {
		t.Fatalf("expected syntax error, got %v", err)
	}
}
