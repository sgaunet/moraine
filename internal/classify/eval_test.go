package classify_test

// Accuracy eval harness.
//
// Prompt, threshold and sampling changes are otherwise judged by eye on a handful of
// photos, which is guesswork: this measures them. It is a test rather than a
// subcommand because a labeled corpus of real photos cannot be committed (size,
// licensing, privacy) and a real vision model cannot run in CI, so there is nothing
// here for a shipped binary to do — while `go test` already knows how to skip.
//
// Point it at a corpus laid out as
//
//	<corpus>/<expected-theme>/<event-directory>/*.jpg
//
// where each leaf directory is one event and its parent names the theme that event
// should be filed under. The theme set is read from the corpus itself, so adding a
// theme means adding a directory. Then:
//
//	MORAINE_EVAL_CORPUS=~/eval task eval
//
// Without MORAINE_EVAL_CORPUS the test skips, so it costs a normal `task test`
// nothing. The report ends with the mean confidence of right versus wrong answers,
// which is the number to pick a --min-confidence from.
//
// With MORAINE_EVAL_DECIDER=laya|jev the same corpus is also run through
// describe-then-decide (the key comes from LAYA_API_KEY or TYPESAFE_API_KEY, as for
// sort), and the two methods are reported one after the other, then side by side
// with their wall time. An event directory whose name starts with "mixed-" holds
// photos of more than one theme: it is left out of accuracy and reported on its own,
// so its confidence can be set against the homogeneous events' — a threshold is only
// useful if it separates the two.

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/classify"
	"github.com/sgaunet/moraine/internal/config"
	"github.com/sgaunet/moraine/internal/decide"
	"github.com/sgaunet/moraine/internal/exifmeta"
	"github.com/sgaunet/moraine/internal/heicpreview"
	"github.com/sgaunet/moraine/internal/photo"
	"github.com/sgaunet/moraine/internal/rawpreview"
)

// Environment variables the harness reads. Only the corpus is required; the rest
// mirror the `sort` flags they are named after.
const (
	evalCorpusEnv        = "MORAINE_EVAL_CORPUS"
	evalOllamaURLEnv     = "MORAINE_EVAL_OLLAMA_URL"
	evalModelEnv         = "MORAINE_EVAL_MODEL"
	evalSampleEnv        = "MORAINE_EVAL_SAMPLE"
	evalFallbackEnv      = "MORAINE_EVAL_FALLBACK"
	evalVoteEnv          = "MORAINE_EVAL_VOTE"
	evalMinConfidenceEnv = "MORAINE_EVAL_MIN_CONFIDENCE"
	evalMinAccuracyEnv   = "MORAINE_EVAL_MIN_ACCURACY"

	evalDeciderEnv              = "MORAINE_EVAL_DECIDER"
	evalDeciderURLEnv           = "MORAINE_EVAL_DECIDER_URL"
	evalDeciderModelEnv         = "MORAINE_EVAL_DECIDER_MODEL"
	evalDeciderMinConfidenceEnv = "MORAINE_EVAL_DECIDER_MIN_CONFIDENCE"
)

// mixedPrefix marks an event directory holding more than one theme.
const mixedPrefix = "mixed-"

// evalTimeouts bound the external programs the harness may need. They match what
// app.Organize uses, so the eval measures the same pipeline a real run does.
const (
	evalRawTimeout  = 30 * time.Second
	evalHEICTimeout = 60 * time.Second
)

// evalSettings is the harness configuration, read from the environment.
type evalSettings struct {
	corpus        string
	ollamaURL     string
	model         string
	fallback      string
	sample        int
	vote          bool
	minConfidence float64
	minAccuracy   float64

	decider              config.Decider // off: single-call only
	deciderURL           string
	deciderModel         string
	deciderMinConfidence float64
}

// evalEvent is one labeled event: the photos, and the theme the corpus says they
// belong to.
type evalEvent struct {
	dir      string
	expected string
	cluster  photo.Cluster
}

// evalOutcome is what the pipeline decided about one event.
type evalOutcome struct {
	event      evalEvent
	got        string
	method     classify.Method
	confidence float64
}

func (o evalOutcome) correct() bool { return o.got == o.event.expected }

// mixed reports whether the event deliberately spans several themes.
func (e evalEvent) mixed() bool { return strings.HasPrefix(filepath.Base(e.dir), mixedPrefix) }

func TestClassifyAccuracy(t *testing.T) {
	set := evalSettingsFromEnv(t)
	events, themes := loadCorpus(t, set.corpus)
	if len(events) == 0 {
		t.Fatalf("no events found under %s (expected <corpus>/<theme>/<event>/*.jpg)", set.corpus)
	}
	for _, th := range themes {
		if th == set.fallback {
			t.Fatalf("corpus theme %q collides with the fallback theme; set %s", th, evalFallbackEnv)
		}
	}
	t.Logf("corpus %s: %d events, themes %v", set.corpus, len(events), themes)

	single, singleTook := runEval(t, "single-call", evalClassifier(t, set, themes), set, set.minConfidence, events, themes)
	if set.decider == config.DeciderOff {
		checkAccuracyFloor(t, set, single)
		return
	}
	described, describedTook := runEval(t, "described", evalDescriber(t, set, themes), set,
		set.minConfidence, events, themes)
	t.Logf("single-call accuracy=%s described accuracy=%s time ratio=%.2f (single-call %s, described %s)",
		accuracyOf(single), accuracyOf(described), float64(describedTook)/float64(max(singleTook, 1)),
		singleTook.Round(time.Second), describedTook.Round(time.Second))
	checkAccuracyFloor(t, set, described)
}

// runEval labels every event with one classifier, reports the outcome under a
// heading, and returns it with the wall time the labelling took.
func runEval(
	t *testing.T, name string, oc *classify.OllamaClassifier, set evalSettings, minConfidence float64,
	events []evalEvent, themes []string,
) ([]evalOutcome, time.Duration) {
	t.Helper()
	rec := &recordingClassifier{inner: oc}
	opts := classify.Options{
		Themes:               themes,
		Fallback:             set.fallback,
		Classifier:           rec,
		MountainAltitudeM:    1500,
		MinConfidence:        minConfidence,
		DeciderMinConfidence: set.deciderMinConfidence,
	}
	start := time.Now()
	outcomes := make([]evalOutcome, 0, len(events))
	for _, ev := range events {
		theme, method := classify.Label(context.Background(), ev.cluster, opts)
		outcomes = append(outcomes, evalOutcome{
			event: ev, got: theme, method: method, confidence: rec.last.Confidence,
		})
	}
	took := time.Since(start)
	t.Logf("== %s (%s) ==", name, took.Round(time.Second))
	homogeneous, mixed := splitMixed(outcomes)
	if len(homogeneous) > 0 {
		reportEval(t, homogeneous, themes)
	}
	reportMixed(t, homogeneous, mixed)
	return outcomes, took
}

// checkAccuracyFloor fails the run when the measured method falls below the floor.
func checkAccuracyFloor(t *testing.T, set evalSettings, outcomes []evalOutcome) {
	t.Helper()
	homogeneous, _ := splitMixed(outcomes)
	if len(homogeneous) == 0 {
		return
	}
	correct, _ := tallyOutcomes(homogeneous)
	if accuracy := float64(correct) / float64(len(homogeneous)); accuracy < set.minAccuracy {
		t.Errorf("accuracy %.1f%% is below the %s floor of %.1f%%",
			100*accuracy, evalMinAccuracyEnv, 100*set.minAccuracy)
	}
}

// splitMixed separates the homogeneous events, which have a right answer, from the
// mixed ones, which only have a confidence worth looking at.
func splitMixed(outcomes []evalOutcome) (homogeneous, mixed []evalOutcome) {
	for _, o := range outcomes {
		if o.event.mixed() {
			mixed = append(mixed, o)
		} else {
			homogeneous = append(homogeneous, o)
		}
	}
	return homogeneous, mixed
}

// accuracyOf renders the homogeneous events' accuracy.
func accuracyOf(outcomes []evalOutcome) string {
	homogeneous, _ := splitMixed(outcomes)
	if len(homogeneous) == 0 {
		return "n/a"
	}
	correct, _ := tallyOutcomes(homogeneous)
	return fmt.Sprintf("%.1f%%", 100*float64(correct)/float64(len(homogeneous)))
}

// reportMixed prints every mixed event's answer and confidence, then the mean
// confidence of mixed against homogeneous events (SC-002).
func reportMixed(t *testing.T, homogeneous, mixed []evalOutcome) {
	t.Helper()
	if len(mixed) == 0 {
		return
	}
	var mixedSum, homoSum float64
	var mixedN, homoN int
	for _, o := range mixed {
		t.Logf("  mixed: %s -> %s (method %s, confidence %.2f)", o.event.dir, o.got, o.method, o.confidence)
		if o.confidence > 0 {
			mixedSum, mixedN = mixedSum+o.confidence, mixedN+1
		}
	}
	for _, o := range homogeneous {
		if o.confidence > 0 {
			homoSum, homoN = homoSum+o.confidence, homoN+1
		}
	}
	t.Logf("confidence: mixed %s, homogeneous %s", meanOf(mixedSum, mixedN), meanOf(homoSum, homoN))
}

// evalSettingsFromEnv reads the harness configuration, skipping the whole test when
// no corpus is configured.
func evalSettingsFromEnv(t *testing.T) evalSettings {
	t.Helper()
	corpus := os.Getenv(evalCorpusEnv)
	if corpus == "" {
		t.Skipf("set %s=<dir> to measure classification accuracy (see eval_test.go)", evalCorpusEnv)
	}
	return evalSettings{
		corpus:        corpus,
		ollamaURL:     envOr(evalOllamaURLEnv, "http://127.0.0.1:11434"),
		model:         envOr(evalModelEnv, "qwen3-vl:8b"),
		fallback:      envOr(evalFallbackEnv, "other"),
		sample:        envInt(t, evalSampleEnv, 3),
		vote:          os.Getenv(evalVoteEnv) == "true",
		minConfidence: envFloat(t, evalMinConfidenceEnv, 0),
		minAccuracy:   envFloat(t, evalMinAccuracyEnv, 0),

		decider:              evalDecider(t),
		deciderURL:           os.Getenv(evalDeciderURLEnv),
		deciderModel:         os.Getenv(evalDeciderModelEnv),
		deciderMinConfidence: envFloat(t, evalDeciderMinConfidenceEnv, 0),
	}
}

// evalDecider reads MORAINE_EVAL_DECIDER through the parser sort uses.
func evalDecider(t *testing.T) config.Decider {
	t.Helper()
	d, err := config.ParseDecider(os.Getenv(evalDeciderEnv))
	if err != nil {
		t.Fatalf("%s: %v", evalDeciderEnv, err)
	}
	return d
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(t *testing.T, key string, def int) int {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		t.Fatalf("%s=%q is not a number: %v", key, v, err)
	}
	return n
}

func envFloat(t *testing.T, key string, def float64) float64 {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		t.Fatalf("%s=%q is not a number: %v", key, v, err)
	}
	return f
}

// evalClassifier builds the real Ollama classifier, wired exactly as a run wires it,
// and fails the test if the model is not there: an eval against a missing model would
// only measure the fallback theme.
func evalClassifier(t *testing.T, set evalSettings, themes []string) *classify.OllamaClassifier {
	t.Helper()
	oc := classify.NewOllama(set.ollamaURL, set.model, set.sample, themes)
	oc.Logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	oc.Vote = set.vote
	oc.RawPreview = rawpreview.NewExtractor("exiftool", evalRawTimeout)
	if conv, ok := heicpreview.Detect(evalHEICTimeout); ok {
		oc.HEICPreview = conv
	}
	if status := oc.Preflight(context.Background()); status != classify.StatusReady {
		t.Fatalf("ollama at %s is not ready for model %q (status %v)", set.ollamaURL, set.model, status)
	}
	t.Logf("model %s at %s (sample %d, vote %v, min-confidence %g)",
		set.model, set.ollamaURL, set.sample, set.vote, set.minConfidence)
	return oc
}

// evalDescriber builds a second classifier that describes and then decides, and fails
// the test if the decision service is not ready: an eval against an unusable service
// would only measure the single-call fallback a second time. That costs one readiness
// request more than a run makes (the classifier still checks on its own), which is the
// price of failing loudly instead of degrading quietly.
func evalDescriber(t *testing.T, set evalSettings, themes []string) *classify.OllamaClassifier {
	t.Helper()
	oc := evalClassifier(t, set, themes)
	// The same resolution sort applies, so the defaults cannot differ from a real run's.
	cfg, err := config.New(config.Options{
		Source: set.corpus, Gap: time.Hour, MountainAltitude: 1500, Themes: strings.Join(themes, ","),
		Fallback: set.fallback, LogLevel: "warn",
		Decider: string(set.decider), DeciderURL: set.deciderURL, DeciderModel: set.deciderModel,
		DeciderAPIKey: os.Getenv(set.decider.APIKeyEnv()),
	})
	if err != nil {
		t.Fatalf("decider settings: %v", err)
	}
	dc, err := decide.New(decide.Config{
		Backend: decide.Backend(cfg.Decider), URL: cfg.DeciderURL, Model: cfg.DeciderModel, APIKey: cfg.DeciderAPIKey,
	})
	if err != nil {
		t.Fatalf("decision client: %v", err)
	}
	dc.Logger = oc.Logger
	options := classify.EffectiveDescriptions(themes, set.fallback, nil)
	if err := dc.Ready(context.Background(), classify.Decision{Options: options, Fallback: set.fallback}); err != nil {
		t.Fatalf("decision service %s at %s is not ready: %v", cfg.Decider, cfg.DeciderURL, err)
	}
	oc.Decider = dc
	oc.Fallback = set.fallback
	oc.DeciderAttrs = []any{"backend", string(cfg.Decider), "url", cfg.DeciderURL, "model", cfg.DeciderModel}
	t.Logf("decider %s at %s (model %s, min-confidence %g)",
		cfg.Decider, cfg.DeciderURL, cfg.DeciderModel, set.deciderMinConfidence)
	return oc
}

// recordingClassifier keeps the last verdict on its way through, so the report can
// show the confidence behind a label without paying for a second model call.
type recordingClassifier struct {
	inner classify.Classifier
	last  classify.Verdict
}

func (r *recordingClassifier) Classify(ctx context.Context, c photo.Cluster) (classify.Verdict, error) {
	v, err := r.inner.Classify(ctx, c)
	r.last = v
	return v, err
}

// loadCorpus reads every labeled event under root, and returns the theme set the
// corpus's own directory names define.
func loadCorpus(t *testing.T, root string) ([]evalEvent, []string) {
	t.Helper()
	themeDirs, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("reading corpus %q: %v", root, err)
	}
	var (
		events []evalEvent
		themes []string
	)
	for _, td := range themeDirs {
		if !td.IsDir() {
			continue
		}
		themes = append(themes, td.Name())
		themeDir := filepath.Join(root, td.Name())
		eventDirs, err := os.ReadDir(themeDir)
		if err != nil {
			t.Fatalf("reading theme directory %q: %v", themeDir, err)
		}
		for _, ed := range eventDirs {
			if !ed.IsDir() {
				continue
			}
			dir := filepath.Join(themeDir, ed.Name())
			if c, ok := loadEvent(t, dir); ok {
				events = append(events, evalEvent{dir: dir, expected: td.Name(), cluster: c})
			}
		}
	}
	sort.Strings(themes)
	return events, themes
}

// loadEvent reads one event directory into a cluster, with real metadata: the prompt
// carries the capture span, altitude and GPS, so an eval that faked them would not be
// measuring the prompt the tool actually sends.
func loadEvent(t *testing.T, dir string) (photo.Cluster, bool) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading event directory %q: %v", dir, err)
	}
	var photos []photo.Photo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		format, ok := photo.FormatFromExt(e.Name())
		if !ok {
			continue
		}
		p, err := exifmeta.Read(filepath.Join(dir, e.Name()), format)
		if err != nil {
			t.Logf("skipping unreadable %s: %v", filepath.Join(dir, e.Name()), err)
			continue
		}
		photos = append(photos, p)
	}
	if len(photos) == 0 {
		t.Logf("skipping %s: no readable images", dir)
		return photo.Cluster{}, false
	}
	sort.Slice(photos, func(i, j int) bool { return photos[i].Taken.Before(photos[j].Taken) })
	return photo.Cluster{Photos: photos, Start: photos[0].Taken, End: photos[len(photos)-1].Taken}, true
}

// reportEval prints the measurement: overall accuracy, a per-theme breakdown with
// what each theme was confused for, every wrong answer with the confidence behind
// it, and the mean confidence of right versus wrong answers — the last being the
// number a --min-confidence threshold should be chosen from.
func reportEval(t *testing.T, outcomes []evalOutcome, themes []string) {
	t.Helper()
	correct, confusion := tallyOutcomes(outcomes)
	total := len(outcomes)
	t.Logf("accuracy %d/%d (%.1f%%)", correct, total, 100*float64(correct)/float64(total))

	for _, th := range themes {
		got := confusion[th]
		n, right := 0, got[th]
		for _, c := range got {
			n += c
		}
		if n == 0 {
			continue
		}
		t.Logf("  %-16s %d/%d %5.1f%%%s", th, right, n, 100*float64(right)/float64(n), confusedWith(got, th))
	}

	for _, o := range outcomes {
		if !o.correct() {
			t.Logf("  wrong: %s -> %s (method %s, confidence %.2f)",
				o.event.dir, o.got, o.method, o.confidence)
		}
	}
	reportConfidence(t, outcomes)
	reportMethods(t, outcomes)
}

// tallyOutcomes counts the right answers and builds the expected→got confusion map.
func tallyOutcomes(outcomes []evalOutcome) (int, map[string]map[string]int) {
	correct := 0
	confusion := make(map[string]map[string]int)
	for _, o := range outcomes {
		if confusion[o.event.expected] == nil {
			confusion[o.event.expected] = make(map[string]int)
		}
		confusion[o.event.expected][o.got]++
		if o.correct() {
			correct++
		}
	}
	return correct, confusion
}

// confusedWith renders what a theme's events were labeled instead, most frequent
// first, or "" when they were all right.
func confusedWith(got map[string]int, expected string) string {
	type pair struct {
		theme string
		n     int
	}
	var wrong []pair
	for theme, n := range got {
		if theme != expected {
			wrong = append(wrong, pair{theme, n})
		}
	}
	if len(wrong) == 0 {
		return ""
	}
	sort.Slice(wrong, func(i, j int) bool {
		if wrong[i].n != wrong[j].n {
			return wrong[i].n > wrong[j].n
		}
		return wrong[i].theme < wrong[j].theme
	})
	var b strings.Builder
	b.WriteString("  (")
	for i, w := range wrong {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%d -> %s", w.n, w.theme)
	}
	b.WriteString(")")
	return b.String()
}

// reportConfidence prints the mean reported confidence of right and wrong answers.
// A model whose wrong answers are as confident as its right ones is telling you that
// --min-confidence cannot help, which is worth knowing before setting one.
func reportConfidence(t *testing.T, outcomes []evalOutcome) {
	t.Helper()
	var rightSum, wrongSum float64
	var rightN, wrongN int
	for _, o := range outcomes {
		if o.confidence == 0 {
			continue // not reported: averaging it in would drag both means to zero
		}
		if o.correct() {
			rightSum, rightN = rightSum+o.confidence, rightN+1
		} else {
			wrongSum, wrongN = wrongSum+o.confidence, wrongN+1
		}
	}
	if rightN+wrongN == 0 {
		t.Log("confidence: never reported by this model — --min-confidence would have no effect")
		return
	}
	t.Logf("confidence: right %s, wrong %s (%d of %d answers reported one)",
		meanOf(rightSum, rightN), meanOf(wrongSum, wrongN), rightN+wrongN, len(outcomes))
}

func meanOf(sum float64, n int) string {
	if n == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.2f (n=%d)", sum/float64(n), n)
}

// reportMethods prints how many events each stage of the pipeline decided, so a
// disappointing accuracy can be read as "the model was wrong" or "the model never
// ran".
func reportMethods(t *testing.T, outcomes []evalOutcome) {
	t.Helper()
	counts := make(map[string]int)
	for _, o := range outcomes {
		counts[string(o.method)]++
	}
	methods := make([]string, 0, len(counts))
	for m := range counts {
		methods = append(methods, m)
	}
	sort.Strings(methods)
	for _, m := range methods {
		t.Logf("  method %-14s %d", m, counts[m])
	}
}
