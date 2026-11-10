package ui

// Nerd Font icons (Symbols Nerd Font Mono) with plain Unicode fallbacks.
// Set REXDASH_ICONS=plain when the terminal has no Nerd Font glyphs.

import (
	"os"
	"strings"
	"sync"
)

var nerdOnce = sync.OnceValue(func() bool { return os.Getenv("REXDASH_ICONS") != "plain" })

// Nerd reports whether Nerd Font glyphs are in use.
func Nerd() bool { return nerdOnce() }

// Ic is an icon with a plain fallback.
type Ic struct{ Glyph, Plain string }

func (i Ic) S() string {
	if Nerd() {
		return i.Glyph
	}
	return i.Plain
}

var (
	// status
	OK      = Ic{"", "✓"}
	FAIL    = Ic{"", "✗"}
	RUNNING = Ic{"", "●"}
	PENDING = Ic{"", "·"}
	WARN    = Ic{"", "!"}
	BUG     = Ic{"", "!"}

	ROCKET = Ic{"", "»"}
	LOGS   = Ic{"", "≡"}

	// git
	BRANCH    = Ic{"", ""}
	COMMIT    = Ic{"", ""}
	DIFF      = Ic{"", ""}
	ADDED     = Ic{"", "A"}
	MODIFIED  = Ic{"", "M"}
	REMOVED   = Ic{"", "D"}
	RENAMED   = Ic{"", "R"}
	UNTRACKED = Ic{"", "?"}
	CONFLICT  = Ic{"", "U"}

	// github
	PR        = Ic{"", ""}
	MERGED    = Ic{"", ""}
	DRAFT     = Ic{"", "draft"}
	CHECKLIST = Ic{"", ""}
	EYE       = Ic{"", ""}
	EXCL      = Ic{"", ""}

	// pipeline
	SOURCE    = Ic{"", ""}
	TEST      = Ic{"", ""}
	BUILD     = Ic{"", ""}
	DEPLOY    = Ic{"", ""}
	STAGE     = Ic{"", ""}
	HOURGLASS = Ic{"", ""}
	CALENDAR  = Ic{"", ""}
	TERMINAL  = Ic{"", "log"}

	// logs
	DOT    = Ic{"", "●"}
	PAUSE  = Ic{"", "⏸"}
	FILTER = Ic{"", "/"}

	// chrome
	REFRESH    = Ic{"", "⟳"}
	CAP_L      = Ic{"", ""}
	CAP_R      = Ic{"", ""}
	ARROW      = Ic{"", " "}
	ARROW_THIN = Ic{"", "›"}
)

// FileIcon is the icon and colour for a file, by extension or name.
func FileIcon(path string) (string, Color) {
	if !Nerd() {
		return "", 0
	}
	name := path[strings.LastIndex(path, "/")+1:]
	ext := ""
	if i := strings.LastIndex(name, "."); i >= 0 {
		ext = name[i+1:]
	}
	switch {
	case in(ext, "ex", "exs", "eex", "heex", "leex"):
		return "", Magenta()
	case in(name, "mix.lock", "Cargo.lock", "bun.lock", "package-lock.json") || ext == "lock":
		return "", Muted()
	case strings.HasPrefix(name, "Dockerfile") || strings.HasPrefix(name, "docker-compose"):
		return "", Blue()
	case in(ext, "js", "mjs", "cjs", "jsx"):
		return "", Yellow()
	case in(ext, "ts", "tsx"):
		return "", Blue()
	case ext == "svelte":
		return "", Red()
	case ext == "json":
		return "", Yellow()
	case in(ext, "md", "markdown"):
		return "", FgC()
	case in(ext, "css", "scss"):
		return "", Blue()
	case ext == "html":
		return "", Red()
	case ext == "rs":
		return "", Yellow()
	case ext == "go":
		return "", Cyan()
	case ext == "py":
		return "", Yellow()
	case in(ext, "sh", "bash", "zsh", "fish"):
		return "", Green()
	case in(ext, "yml", "yaml", "toml"):
		return "", Yellow()
	case in(ext, "tf", "tfvars"):
		return "", Magenta()
	case ext == "sql":
		return "", Yellow()
	}
	return "", Muted()
}

func in(s string, list ...string) bool {
	for _, x := range list {
		if s == x {
			return true
		}
	}
	return false
}

// StageIcon is the icon for a pipeline stage by its usual name.
func StageIcon(name string) Ic {
	switch strings.ToLower(name) {
	case "source":
		return SOURCE
	case "test", "tests":
		return TEST
	case "build":
		return BUILD
	case "deploy", "release":
		return DEPLOY
	}
	return STAGE
}

// KeyIcon is the keycap label for a key name: keyboard icons for special keys.
func KeyIcon(k string) string {
	if !Nerd() {
		return k
	}
	switch k {
	case "↑↓":
		return ""
	case "⏎":
		return "\U000f0311"
	case "tab":
		return "\U000f0312"
	case "esc":
		return "\U000f12b7"
	case "space":
		return "\U000f1050"
	}
	return k
}
