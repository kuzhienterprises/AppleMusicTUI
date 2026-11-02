package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvFile is the dotenv file read at startup, beside the settings file.
//
// It exists so a key can live in one place without being exported by hand in
// every shell, and without going into config.json, where everything else that
// is secret already sits behind a mask. A real environment variable always
// wins over a line in this file, so a one-off `OPENAI_API_KEY=... am recommend`
// still overrides what is on disk.
var EnvFile = filepath.Join(Dir, ".env")

// EnvFileTemplate is what `am config env` writes. It is a comment and an
// example rather than a value, because a file full of keys copied from the
// documentation is how keys end up in the wrong hands.
const EnvFileTemplate = `# Environment for every am command.
#
# One KEY=VALUE per line. A line that starts with # is a comment, so a key
# pasted after one is ignored. A variable already set in the environment wins
# over the line here.
#
# Put a key on its own line, without the #:
#
#   OPENAI_API_KEY=sk-...
`

// LoadEnv reads KEY=VALUE lines from path into the process environment,
// without overwriting anything already set.
//
// It returns how many variables were applied and what it could not make sense
// of. A missing file is not an error: most machines will not have one.
func LoadEnv(path string) (applied int, problems []string, err error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil, nil
		}
		return 0, nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		// `export KEY=VALUE` is what a shell user types, and pasting it into
		// the file is the obvious mistake to make.
		text = strings.TrimPrefix(text, "export ")

		name, value, ok := strings.Cut(text, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			problems = append(problems, fmt.Sprintf("line %d is not KEY=VALUE", line))
			continue
		}
		if _, exists := os.LookupEnv(name); exists {
			continue
		}
		if err := os.Setenv(name, unquote(strings.TrimSpace(value))); err != nil {
			problems = append(problems, fmt.Sprintf("line %d: %v", line, err))
			continue
		}
		applied++
	}
	if err := scanner.Err(); err != nil {
		return applied, problems, err
	}
	return applied, problems, nil
}

// unquote removes one pair of matching quotes, so a key pasted with the quotes
// from a document still works.
func unquote(value string) string {
	if len(value) >= 2 {
		first, last := value[0], value[len(value)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return value[1 : len(value)-1]
		}
	}
	return value
}

// WriteEnvTemplate creates the env file if it does not exist yet, and reports
// the path either way. It never overwrites: a file that is already there may
// hold a key.
func WriteEnvTemplate() (string, bool, error) {
	if _, err := os.Stat(EnvFile); err == nil {
		return EnvFile, false, nil
	}
	if err := os.MkdirAll(Dir, 0o700); err != nil {
		return EnvFile, false, err
	}
	if err := os.WriteFile(EnvFile, []byte(EnvFileTemplate), 0o600); err != nil {
		return EnvFile, false, err
	}
	_ = os.Chmod(EnvFile, 0o600)
	return EnvFile, true, nil
}

// Commented reports whether name appears in the env file only as a comment.
//
// It exists because the file the tool writes holds the example in a comment,
// and pasting a real key after the "#" is the obvious thing to do. It looks
// right, it is ignored, and without this the failure reads as "no key" with a
// file that plainly has one in it.
func Commented(name string) bool {
	file, err := os.Open(EnvFile)
	if err != nil {
		return false
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		text := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(text, "#") {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(strings.TrimLeft(text, "#")), name+"=") {
			return true
		}
	}
	return false
}

// EnvFileStatus describes the env file for `am config` and `am doctor`,
// without printing what is in it.
func EnvFileStatus() string {
	info, err := os.Stat(EnvFile)
	if err != nil {
		return "missing"
	}
	if info.IsDir() {
		return "is a directory"
	}
	return info.ModTime().Format("2006-01-02")
}
