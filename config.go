package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config is the client's optional settings file.
type Config struct {
	Remote string `json:"remote,omitempty"` // base URL of a cuh server
	Secret string `json:"secret,omitempty"` // its shared secret
}

func configPath() string {
	base, err := os.UserConfigDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "cuh", "config.json")
}

// loadConfig reads the file, then lets environment variables override it.
func loadConfig() Config {
	var c Config
	if raw, err := os.ReadFile(configPath()); err == nil {
		json.Unmarshal(raw, &c)
	}
	if v := os.Getenv("CUH_REMOTE"); v != "" {
		c.Remote = v
	}
	if v := os.Getenv("CUH_SECRET"); v != "" {
		c.Secret = v
	}
	return c
}

func saveConfig(c Config) error {
	path := configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(path, append(raw, '\n'), 0o600)
}

// runConfig implements `cuh config ...`.
func runConfig(args []string) {
	if len(args) == 0 {
		args = []string{"show"}
	}
	switch args[0] {
	case "show":
		c := loadConfig()
		fmt.Println("file:  ", configPath())
		fmt.Println("remote:", orNone(c.Remote))
		fmt.Println("secret:", mask(c.Secret))
	case "remote":
		fs := flag.NewFlagSet("config remote", flag.ExitOnError)
		secret := fs.String("secret", "", "shared secret the server expects")
		rest := args[1:]
		url := ""
		if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") { // URL first, then flags
			url, rest = rest[0], rest[1:]
		}
		fs.Parse(rest)
		if url == "" && fs.NArg() == 1 { // flags first, then URL
			url = fs.Arg(0)
		}
		if url == "" {
			fatal(fmt.Errorf("usage: cuh config remote http://host:8787 [--secret S]"))
		}
		c := loadConfig()
		c.Remote = url
		if *secret != "" {
			c.Secret = *secret
		}
		if err := saveConfig(c); err != nil {
			fatal(err)
		}
		fmt.Println("remote set to", c.Remote)
	case "clear":
		if err := saveConfig(Config{}); err != nil {
			fatal(err)
		}
		fmt.Println("config cleared; using direct fetch")
	default:
		fatal(fmt.Errorf("unknown config command %q (show, remote, clear)", args[0]))
	}
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func mask(s string) string {
	if s == "" {
		return "(none)"
	}
	if len(s) <= 4 {
		return "****"
	}
	return s[:2] + "****" + s[len(s)-2:]
}
