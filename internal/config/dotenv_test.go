package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeEnv puts a file in a temporary directory and returns its path.
func writeEnv(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	return path
}

func TestLoadEnvReadsLines(t *testing.T) {
	// Names are unique to this test so a machine's real environment cannot
	// make them look already set.
	t.Setenv("AM_TEST_KEY", "")
	os.Unsetenv("AM_TEST_KEY")

	path := writeEnv(t, strings.Join([]string{
		"# a comment",
		"",
		"AM_TEST_KEY=sk-abc",
		"export AM_TEST_MODEL = gpt-6-luna ",
		`AM_TEST_QUOTED="quoted value"`,
		"AM_TEST_SINGLE='single'",
		"not a pair",
		"=no name",
		"AM_TEST_EQUALS=a=b=c",
	}, "\n"))

	applied, problems, err := LoadEnv(path)
	if err != nil {
		t.Fatalf("LoadEnv: %v", err)
	}
	if applied != 5 {
		t.Errorf("applied %d variables, want 5", applied)
	}
	if len(problems) != 2 {
		t.Errorf("problems = %v, want two lines reported", problems)
	}

	cases := map[string]string{
		"AM_TEST_KEY":    "sk-abc",
		"AM_TEST_MODEL":  "gpt-6-luna",
		"AM_TEST_QUOTED": "quoted value",
		"AM_TEST_SINGLE": "single",
		"AM_TEST_EQUALS": "a=b=c",
	}
	for name, want := range cases {
		if got := os.Getenv(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

// A variable already in the environment is the one that counts: a one-off
// `OPENAI_API_KEY=... am recommend` must not be overridden by a file.
func TestLoadEnvDoesNotOverrideTheEnvironment(t *testing.T) {
	t.Setenv("AM_TEST_EXISTING", "from-the-environment")

	path := writeEnv(t, "AM_TEST_EXISTING=from-the-file\n")

	if _, _, err := LoadEnv(path); err != nil {
		t.Fatalf("LoadEnv: %v", err)
	}
	if got := os.Getenv("AM_TEST_EXISTING"); got != "from-the-environment" {
		t.Errorf("%s = %q, want the environment's value", "AM_TEST_EXISTING", got)
	}
}

func TestLoadEnvWithoutAFileIsNotAnError(t *testing.T) {
	applied, problems, err := LoadEnv(filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatalf("a missing file was an error: %v", err)
	}
	if applied != 0 || len(problems) != 0 {
		t.Errorf("applied %d, problems %v", applied, problems)
	}
}

func TestWriteEnvTemplateDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	original := Dir
	Dir = dir
	defer func() { Dir = original }()
	EnvFile = filepath.Join(dir, ".env")
	defer func() { EnvFile = filepath.Join(original, ".env") }()

	path, created, err := WriteEnvTemplate()
	if err != nil {
		t.Fatalf("WriteEnvTemplate: %v", err)
	}
	if !created {
		t.Fatal("the file was not created")
	}

	// A key written by hand has to survive a second run.
	if err := os.WriteFile(path, []byte("OPENAI_API_KEY=sk-keep\n"), 0o600); err != nil {
		t.Fatalf("writing the key: %v", err)
	}
	if _, created, err = WriteEnvTemplate(); err != nil {
		t.Fatalf("WriteEnvTemplate: %v", err)
	}
	if created {
		t.Error("a second run overwrote the file")
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading it back: %v", err)
	}
	if string(body) != "OPENAI_API_KEY=sk-keep\n" {
		t.Errorf("body = %q, want the key that was written", body)
	}
}
