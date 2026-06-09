// Package config centralises moraine's typed configuration (Constitution
// Principle II): a single Config struct built once from CLI flags, validated,
// then passed explicitly to the packages that need it. No mutable globals.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sgaunet/moraine/internal/photo"
)

// Config holds all runtime parameters for a moraine session.
type Config struct {
	Source    string        // absolute path of the scanned directory
	DestRoot  string        // absolute path where committed groups are moved (excluded from scan)
	Addr      string        // listen address (loopback by default)
	Model     string        // Ollama vision model
	Gap       time.Duration // max temporal gap within an event
	Home      *photo.LatLng // optional home coordinate for travel heuristic
	Sample    int           // thumbnails sampled per group for the model
	OllamaURL string        // base URL of the local Ollama API
}

// Default values surfaced in the CLI contract.
const (
	DefaultAddr      = "127.0.0.1:8080"
	DefaultModel     = "qwen2.5vl:7b"
	DefaultGap       = 4 * time.Hour
	DefaultSample    = 3
	DefaultOllamaURL = "http://127.0.0.1:11434"
	DefaultDestName  = "_trie"
)

// Parse builds a Config from CLI-style arguments (without the program name).
// It reports usage/argument errors (exit code 2 at the call site). Filesystem
// existence checks are deferred to Validate.
func Parse(args []string) (Config, error) {
	fs := flag.NewFlagSet("moraine", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // caller renders usage; avoid flag's own noise

	var (
		dest   = fs.String("dest", "", "racine de destination (défaut <source>/_trie ; exclue du scan)")
		addr   = fs.String("addr", DefaultAddr, "adresse d'écoute du serveur")
		model  = fs.String("model", DefaultModel, "modèle vision Ollama")
		gap    = fs.Duration("gap", DefaultGap, "écart temporel max au sein d'un événement")
		home   = fs.String("home", "", "coordonnées du domicile \"lat,lng\" (détection voyage)")
		sample = fs.Int("sample", DefaultSample, "photos échantillonnées par groupe pour le modèle")
		ollama = fs.String("ollama-url", DefaultOllamaURL, "URL de base de l'API Ollama locale")
	)

	if err := fs.Parse(args); err != nil {
		return Config{}, fmt.Errorf("arguments invalides : %w", err)
	}

	rest := fs.Args()
	if len(rest) == 0 {
		return Config{}, errors.New("dossier source manquant : usage `moraine [flags] <dossier-source>`")
	}
	if len(rest) > 1 {
		return Config{}, fmt.Errorf("un seul dossier source est attendu, reçu %d (%v)", len(rest), rest)
	}

	if *gap <= 0 {
		return Config{}, fmt.Errorf("-gap doit être strictement positif (reçu %s)", *gap)
	}
	if *sample < 0 {
		return Config{}, fmt.Errorf("-sample doit être positif ou nul (reçu %d)", *sample)
	}

	source, err := filepath.Abs(rest[0])
	if err != nil {
		return Config{}, fmt.Errorf("dossier source illisible %q : %w", rest[0], err)
	}

	destPath := *dest
	if destPath == "" {
		destPath = filepath.Join(source, DefaultDestName)
	}
	destRoot, err := filepath.Abs(destPath)
	if err != nil {
		return Config{}, fmt.Errorf("répertoire de destination illisible %q : %w", destPath, err)
	}

	var homeLL *photo.LatLng
	if strings.TrimSpace(*home) != "" {
		homeLL, err = parseHome(*home)
		if err != nil {
			return Config{}, err
		}
	}

	return Config{
		Source:    source,
		DestRoot:  destRoot,
		Addr:      *addr,
		Model:     *model,
		Gap:       *gap,
		Home:      homeLL,
		Sample:    *sample,
		OllamaURL: *ollama,
	}, nil
}

// Validate performs runtime checks (exit code 1 at the call site): the source
// must exist and be a directory.
func (c Config) Validate() error {
	info, err := os.Stat(c.Source)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("le dossier source %q n'existe pas", c.Source)
		}
		return fmt.Errorf("le dossier source %q n'est pas lisible : %w", c.Source, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("le chemin source %q n'est pas un dossier", c.Source)
	}
	return nil
}

func parseHome(s string) (*photo.LatLng, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 2 {
		return nil, fmt.Errorf("-home doit être au format \"lat,lng\" (reçu %q)", s)
	}
	lat, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return nil, fmt.Errorf("-home : latitude invalide %q : %w", parts[0], err)
	}
	lng, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return nil, fmt.Errorf("-home : longitude invalide %q : %w", parts[1], err)
	}
	return &photo.LatLng{Lat: lat, Lng: lng}, nil
}
