package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindDotEnv(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "cmd", "tropa")
	os.MkdirAll(deep, 0o755)
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o644)

	if got := findDotEnv(deep); got != "" {
		t.Fatalf("без .env нашли %q", got)
	}
	env := filepath.Join(root, ".env")
	os.WriteFile(env, nil, 0o644)
	if got := findDotEnv(deep); got != env {
		t.Fatalf("из подпапки: %q, ждали %q", got, env)
	}
}

func TestLoadDotEnv(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(p, []byte(`# комментарий
TROPA_T_A=postgres://u:p@h/db?sslmode=disable
export TROPA_T_B = "в кавычках"
TROPA_T_C='одинарные'
TROPA_T_KEEP=из файла
`), 0o644)
	t.Setenv("TROPA_T_KEEP", "из окружения")
	for _, k := range []string{"TROPA_T_A", "TROPA_T_B", "TROPA_T_C"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}

	if err := loadDotEnv(p); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"TROPA_T_A":    "postgres://u:p@h/db?sslmode=disable",
		"TROPA_T_B":    "в кавычках",
		"TROPA_T_C":    "одинарные",
		"TROPA_T_KEEP": "из окружения",
	} {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s = %q, ждали %q", k, got, want)
		}
	}

	os.WriteFile(p, []byte("без знака равно\n"), 0o644)
	if err := loadDotEnv(p); err == nil {
		t.Fatal("кривая строка должна давать ошибку")
	}
}
