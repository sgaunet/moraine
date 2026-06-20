// Package config centralises moraine's typed configuration (Constitution
// Principle II): a single Config struct built once from CLI flags, validated,
// then passed explicitly to the packages that need it. No mutable globals.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Config holds all runtime parameters for a moraine organize run.
type Config struct {
	Source        string        // absolute path of the source (a directory → batch, a file → single photo)
	SourceIsDir   bool          // resolved by Validate: directory (batch) vs regular file (single)
	DestRoot      string        // absolute path of the copy destination root (excluded from scan)
	Model         string        // Ollama vision model
	Gap           time.Duration // max temporal gap within an event
	Sample        int           // photos sampled per large group for the model (0 disables the model stage)
	OllamaURL     string        // base URL of the local Ollama API
	Themes        []string      // configured theme slugs (folder names)
	FallbackTheme string        // theme slug used when none is confidently chosen
	LogLevel      slog.Level    // logging verbosity
}

// Default values surfaced in the CLI contract.
const (
	DefaultModel     = "qwen2.5vl:7b"
	DefaultGap       = time.Hour
	DefaultSample    = 3
	DefaultOllamaURL = "http://127.0.0.1:11434"
	DefaultThemes    = "family,mountain,special-events,nature"
	DefaultFallback  = "other"
	DefaultLogLevel  = "info"
	DefaultDestName  = "_trie"
)

// slugPattern constrains theme slugs to filesystem-safe lowercase tokens.
var slugPattern = regexp.MustCompile(`^[a-z0-9-]+$`)

// Parse builds a Config from CLI-style arguments (without the program name).
// It reports usage/argument errors (exit code 2 at the call site). Filesystem
// existence checks and destination-default resolution are deferred to Validate.
func Parse(args []string) (Config, error) {
	fs := flag.NewFlagSet("moraine", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // caller renders usage; avoid flag's own noise

	var (
		dest     = fs.String("dest", "", "racine de destination (défaut <source>/_trie ; exclue du scan)")
		model    = fs.String("model", DefaultModel, "modèle vision Ollama")
		gap      = fs.Duration("gap", DefaultGap, "écart temporel max au sein d'un événement")
		sample   = fs.Int("sample", DefaultSample, "photos échantillonnées par grand groupe (0 désactive le modèle)")
		ollama   = fs.String("ollama-url", DefaultOllamaURL, "URL de base de l'API Ollama locale")
		themes   = fs.String("themes", DefaultThemes, "thèmes (slugs séparés par des virgules)")
		fallback = fs.String("fallback-theme", DefaultFallback, "thème de repli quand aucun n'est déterminé")
		logLevel = fs.String("log-level", DefaultLogLevel, "verbosité des logs : debug|info|warn|error")
	)

	if err := fs.Parse(args); err != nil {
		return Config{}, fmt.Errorf("arguments invalides : %w", err)
	}

	rest := fs.Args()
	if len(rest) == 0 {
		return Config{}, errors.New("source manquante : usage `moraine [flags] <dossier-ou-fichier>`")
	}
	if len(rest) > 1 {
		return Config{}, fmt.Errorf("une seule source est attendue, reçu %d (%v)", len(rest), rest)
	}

	if *gap <= 0 {
		return Config{}, fmt.Errorf("-gap doit être strictement positif (reçu %s)", *gap)
	}
	if *sample < 0 {
		return Config{}, fmt.Errorf("-sample doit être positif ou nul (reçu %d)", *sample)
	}

	level, err := parseLevel(*logLevel)
	if err != nil {
		return Config{}, err
	}

	themeList, err := parseThemes(*themes, *fallback)
	if err != nil {
		return Config{}, err
	}

	source, err := filepath.Abs(rest[0])
	if err != nil {
		return Config{}, fmt.Errorf("source illisible %q : %w", rest[0], err)
	}

	destRoot := ""
	if strings.TrimSpace(*dest) != "" {
		destRoot, err = filepath.Abs(*dest)
		if err != nil {
			return Config{}, fmt.Errorf("répertoire de destination illisible %q : %w", *dest, err)
		}
	}

	return Config{
		Source:        source,
		DestRoot:      destRoot,
		Model:         *model,
		Gap:           *gap,
		Sample:        *sample,
		OllamaURL:     *ollama,
		Themes:        themeList,
		FallbackTheme: strings.TrimSpace(*fallback),
		LogLevel:      level,
	}, nil
}

// Validate performs runtime checks (exit code 1 at the call site): the source
// must exist (file or directory) and the destination default is resolved.
func (c *Config) Validate() error {
	info, err := os.Stat(c.Source)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("la source %q n'existe pas", c.Source)
		}
		return fmt.Errorf("la source %q n'est pas lisible : %w", c.Source, err)
	}
	c.SourceIsDir = info.IsDir()

	if c.DestRoot == "" {
		base := c.Source
		if !c.SourceIsDir {
			base = filepath.Dir(c.Source)
		}
		c.DestRoot = filepath.Join(base, DefaultDestName)
	}
	return nil
}

// parseThemes splits a comma-separated slug list, validating each slug and the
// fallback, and rejecting empties, duplicates, and a fallback that collides
// with a theme.
func parseThemes(list, fallback string) ([]string, error) {
	fallback = strings.TrimSpace(fallback)
	if !slugPattern.MatchString(fallback) {
		return nil, fmt.Errorf("-fallback-theme invalide %q : attendu [a-z0-9-]", fallback)
	}
	seen := make(map[string]struct{})
	var themes []string
	for raw := range strings.SplitSeq(list, ",") {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		if !slugPattern.MatchString(s) {
			return nil, fmt.Errorf("thème invalide %q : attendu [a-z0-9-]", s)
		}
		if _, dup := seen[s]; dup {
			return nil, fmt.Errorf("thème en double %q", s)
		}
		if s == fallback {
			return nil, fmt.Errorf("le thème %q ne peut pas être identique au thème de repli", s)
		}
		seen[s] = struct{}{}
		themes = append(themes, s)
	}
	if len(themes) == 0 {
		return nil, errors.New("-themes ne doit pas être vide")
	}
	return themes, nil
}

// parseLevel maps a textual level to slog.Level.
func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("-log-level invalide %q : attendu debug|info|warn|error", s)
	}
}
