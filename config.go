package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Config struct {
	SSH struct {
		Host          string `json:"host"`
		Port          int    `json:"port"`
		User          string `json:"user"`
		PrivateKey    string `json:"private_key"`
		KnownHosts    string `json:"known_hosts"`
		PassphraseEnv string `json:"passphrase_env,omitempty"`
	} `json:"ssh"`
	Display string `json:"display"`
	Window  struct {
		Mode       string `json:"mode,omitempty"`
		Process    string `json:"process"`
		Class      string `json:"class"`
		TitleRegex string `json:"title_regex,omitempty"`
	} `json:"window"`
	MaxImageBytes int    `json:"max_image_bytes"`
	MaxPixels     int    `json:"max_pixels"`
	TimeoutMS     int    `json:"timeout_ms"`
	LogLevel      string `json:"log_level"`
	title         *regexp.Regexp
}

func loadConfig(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil {
		return c, err
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return c, errors.New("config must contain one JSON object")
	}
	if c.SSH.Port == 0 {
		c.SSH.Port = 22
	}
	if c.Display == "" {
		c.Display = ":99"
	}
	if c.MaxImageBytes == 0 {
		c.MaxImageBytes = 20 << 20
	}
	if c.MaxPixels == 0 {
		c.MaxPixels = 16_000_000
	}
	if c.TimeoutMS == 0 {
		c.TimeoutMS = 12000
	}
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
	if c.SSH.Host == "" || strings.HasPrefix(c.SSH.Host, "-") || c.SSH.User == "" || c.SSH.PrivateKey == "" || c.SSH.KnownHosts == "" {
		return c, errors.New("SSH host, user, private_key and known_hosts are required")
	}
	if c.SSH.Port < 1 || c.SSH.Port > 65535 || c.MaxImageBytes < 1024 || c.MaxImageBytes > 256<<20 || c.MaxPixels < 1 || c.MaxPixels > 64_000_000 || c.TimeoutMS < 1000 || c.TimeoutMS > 120000 {
		return c, errors.New("invalid port, image limits or timeout_ms")
	}
	if c.Window.Mode == "" {
		if c.Window.TitleRegex != "" {
			c.Window.Mode = "title"
		} else {
			c.Window.Mode = "codex_tui"
		}
	}
	if c.Window.Mode != "title" && c.Window.Mode != "codex_tui" {
		return c, errors.New("window.mode must be codex_tui or title")
	}
	if c.Window.Process == "" || c.Window.Class == "" {
		return c, errors.New("window process and class are required")
	}
	if c.Window.Mode == "title" {
		if c.Window.TitleRegex == "" {
			return c, errors.New("title mode requires title_regex")
		}
		if c.title, err = regexp.Compile(c.Window.TitleRegex); err != nil {
			return c, fmt.Errorf("title_regex: %w", err)
		}
		if c.title.MatchString("") {
			return c, errors.New("title_regex must not match empty titles")
		}
	} else if c.Window.TitleRegex != "" {
		return c, errors.New("remove title_regex when using codex_tui mode")
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return c, errors.New("invalid log_level")
	}
	for _, p := range []*string{&c.SSH.PrivateKey, &c.SSH.KnownHosts} {
		*p = os.ExpandEnv(*p)
		if strings.HasPrefix(*p, "~/") || strings.HasPrefix(*p, "~\\") {
			h, e := os.UserHomeDir()
			if e != nil {
				return c, e
			}
			*p = filepath.Join(h, (*p)[2:])
		}
		if !filepath.IsAbs(*p) {
			*p = filepath.Join(filepath.Dir(path), *p)
		}
	}
	return c, nil
}
