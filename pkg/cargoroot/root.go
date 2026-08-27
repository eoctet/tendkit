package cargoroot

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Dependencies supplies the read-only process and filesystem inputs used to
// resolve a Cargo install root. Nil functions use their os package defaults.
type Dependencies struct {
	Getwd       func() (string, error)
	ReadFile    func(string) ([]byte, error)
	UserHomeDir func() (string, error)
}

// InstallRoot resolves only public, read-only Cargo configuration. An original
// --root can only be represented by an application's CARGO_INSTALL_ROOT.
func InstallRoot(environment map[string]string, dependencies Dependencies) (string, error) {
	getwd := dependencies.Getwd
	if getwd == nil {
		getwd = os.Getwd
	}
	readFile := dependencies.ReadFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	homeDir := dependencies.UserHomeDir
	if homeDir == nil {
		homeDir = os.UserHomeDir
	}
	cwd, err := getwd()
	if err != nil {
		return "", err
	}
	home, err := homeDir()
	if err != nil {
		return "", err
	}
	if environment != nil {
		if value := strings.TrimSpace(environment["CARGO_INSTALL_ROOT"]); value != "" {
			return resolvePath(value, cwd, home), nil
		}
	}
	if value := strings.TrimSpace(os.Getenv("CARGO_INSTALL_ROOT")); value != "" {
		return resolvePath(value, cwd, home), nil
	}
	cargoHome := ""
	if environment != nil {
		cargoHome = strings.TrimSpace(environment["CARGO_HOME"])
	}
	if cargoHome == "" {
		cargoHome = strings.TrimSpace(os.Getenv("CARGO_HOME"))
	}
	if cargoHome == "" {
		cargoHome = filepath.Join(home, ".cargo")
	} else {
		cargoHome = resolvePath(cargoHome, cwd, home)
	}
	pair := configPair{filepath.Join(cargoHome, "config.toml"), filepath.Join(cargoHome, "config")}
	root, found, err := rootFromConfigPair(pair, readFile, home)
	if err != nil {
		return "", err
	}
	if found {
		return root, nil
	}
	return cargoHome, nil
}

type configPair [2]string

func rootFromConfigPair(pair configPair, readFile func(string) ([]byte, error), home string) (string, bool, error) {
	// Cargo prefers the historical extensionless file when both exist.
	path := pair[1]
	data, err := readFile(path)
	if errors.Is(err, os.ErrNotExist) {
		path = pair[0]
		data, err = readFile(path)
	}
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	value, found, err := parseInstallRoot(string(data))
	if err != nil || !found {
		return "", found, err
	}
	base := filepath.Dir(filepath.Dir(path))
	return resolvePath(value, base, home), true, nil
}

func parseInstallRoot(raw string) (string, bool, error) {
	section := ""
	root := ""
	found := false
	for _, rawLine := range strings.Split(raw, "\n") {
		line := strings.TrimSpace(stripComment(rawLine))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
			continue
		}
		index := strings.IndexByte(line, '=')
		if index < 0 {
			if section == "install" && strings.HasPrefix(line, "root") || strings.HasPrefix(line, "install.root") {
				return "", false, errors.New("Cargo install.root is invalid")
			}
			continue
		}
		key := strings.TrimSpace(line[:index])
		relevant := section == "install" && key == "root" || section == "" && key == "install.root"
		if !relevant {
			continue
		}
		if found {
			return "", false, errors.New("Cargo install.root is duplicated")
		}
		value, err := parseString(strings.TrimSpace(line[index+1:]))
		if err != nil || strings.TrimSpace(value) == "" {
			return "", false, errors.New("Cargo install.root is invalid")
		}
		root, found = value, true
	}
	return root, found, nil
}

func parseString(value string) (string, error) {
	if len(value) < 2 {
		return "", errors.New("not a string")
	}
	if value[0] == '\'' && value[len(value)-1] == '\'' {
		return value[1 : len(value)-1], nil
	}
	if value[0] != '"' || value[len(value)-1] != '"' {
		return "", errors.New("not a string")
	}
	return strconv.Unquote(value)
}

func stripComment(line string) string {
	var quote byte
	escaped := false
	for index := 0; index < len(line); index++ {
		char := line[index]
		if quote == '"' && char == '\\' && !escaped {
			escaped = true
			continue
		}
		if (char == '"' || char == '\'') && !escaped {
			if quote == 0 {
				quote = char
			} else if quote == char {
				quote = 0
			}
		}
		if char == '#' && quote == 0 {
			return line[:index]
		}
		escaped = false
	}
	return line
}

func resolvePath(value, base, home string) string {
	value = strings.TrimSpace(value)
	if value == "~" {
		return filepath.Clean(home)
	}
	if strings.HasPrefix(value, "~/") {
		return filepath.Clean(filepath.Join(home, value[2:]))
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(base, value))
}
