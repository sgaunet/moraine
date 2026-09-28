package classify_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/classify"
	"github.com/sgaunet/moraine/internal/photo"
)

// colourPNG encodes a small solid image whose red channel names photo i, so a fake
// server can tell which photo a request carries. PNG is lossless and a small image
// is sent as it is, so the value survives the trip exactly.
func colourPNG(t *testing.T, i int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for x := range 4 {
		for y := range 4 {
			img.Set(x, y, color.RGBA{R: uint8(i + 1), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// colourCluster is n PNG photos, photo i coloured by colourPNG(i).
func colourCluster(t *testing.T, n int) photo.Cluster {
	t.Helper()
	dir := t.TempDir()
	ps := make([]photo.Photo, 0, n)
	for i := range n {
		p := filepath.Join(dir, fmt.Sprintf("p%02d.png", i))
		if err := os.WriteFile(p, colourPNG(t, i), 0o600); err != nil {
			t.Fatal(err)
		}
		ps = append(ps, photo.Photo{Path: p, Name: filepath.Base(p), Format: photo.PNG})
	}
	return photo.Cluster{Photos: ps}
}

// photoOf reads back which photo an image is.
func photoOf(t *testing.T, b64 string) int {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Error(err)
		return -1
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Error(err)
		return -1
	}
	c, ok := color.RGBAModel.Convert(img.At(0, 0)).(color.RGBA)
	if !ok {
		t.Errorf("unexpected colour model %T", img.At(0, 0))
		return -1
	}
	return int(c.R) - 1
}

// describeReq is one describe request as the fake saw it.
type describeReq struct {
	system    string
	images    int
	photo     int
	options   map[string]any
	keepAlive string
}

// visionFake answers describe requests (no format) with text(photo) and single-call
// classifications (with a format) with category, recording both.
type visionFake struct {
	mu         sync.Mutex
	describes  []describeReq
	classifies int
	inFlight   int
	peak       int
	delay      time.Duration
	text       func(photo int) string
	category   string
}

func (f *visionFake) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(chatOnly(t, func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Error(err)
			return
		}
		var msgs []struct {
			Role    string   `json:"role"`
			Content string   `json:"content"`
			Images  []string `json:"images"`
		}
		_ = json.Unmarshal(raw["messages"], &msgs)
		if _, ok := raw["format"]; ok {
			f.mu.Lock()
			f.classifies++
			f.mu.Unlock()
			_, _ = w.Write([]byte(`{"message":{"content":"{\"category\":\"` + f.category + `\"}"}}`))
			return
		}
		req := describeReq{photo: -1}
		_ = json.Unmarshal(raw["options"], &req.options)
		_ = json.Unmarshal(raw["keep_alive"], &req.keepAlive)
		for _, m := range msgs {
			if m.Role == "system" {
				req.system = m.Content
			}
			req.images += len(m.Images)
			for _, img := range m.Images {
				req.photo = photoOf(t, img)
			}
		}
		f.mu.Lock()
		f.describes = append(f.describes, req)
		f.inFlight++
		f.peak = max(f.peak, f.inFlight)
		f.mu.Unlock()
		time.Sleep(f.delay)
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
		text := fmt.Sprintf("photo %d", req.photo)
		if f.text != nil {
			text = f.text(req.photo)
		}
		body, _ := json.Marshal(map[string]any{"message": map[string]string{"content": text}})
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *visionFake) counts() (describes, classifies int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.describes), f.classifies
}

// fakeDecider is an in-memory classify.Decider recording what it is asked.
type fakeDecider struct {
	mu        sync.Mutex
	ready     func(ctx context.Context) error
	readies   int
	decisions []classify.Decision
	decide    func(n int, d classify.Decision) (classify.Verdict, error)
}

func (f *fakeDecider) Ready(ctx context.Context, _ classify.Decision) error {
	f.mu.Lock()
	f.readies++
	f.mu.Unlock()
	if f.ready != nil {
		return f.ready(ctx)
	}
	return nil
}

func (f *fakeDecider) Decide(_ context.Context, d classify.Decision) (classify.Verdict, error) {
	f.mu.Lock()
	f.decisions = append(f.decisions, d)
	n := len(f.decisions)
	f.mu.Unlock()
	if f.decide != nil {
		return f.decide(n, d)
	}
	// The real client marks its verdicts Decided; this fake deliberately does not, so
	// the tests prove the classifier does it itself.
	return classify.Verdict{Theme: "nature", Confidence: 0.8}, nil
}

func (f *fakeDecider) snapshot() (readies int, decisions []classify.Decision) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.readies, append([]classify.Decision(nil), f.decisions...)
}

// describer wires a classifier to a vision fake and a decider, logging at debug.
func describer(t *testing.T, vf *visionFake, fd *fakeDecider, sample int) (*classify.OllamaClassifier, *safeBuffer) {
	t.Helper()
	logs := &safeBuffer{}
	oc := classify.NewOllama(vf.server(t).URL, "m", sample, themes)
	oc.Logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	oc.Decider = fd
	oc.Fallback = "other"
	oc.DeciderAttrs = []any{"backend", "laya", "url", "http://decider.test"}
	return oc, logs
}

func TestDescribeMakesOneCallPerSampledPhoto(t *testing.T) {
	vf := &visionFake{category: "family"}
	fd := &fakeDecider{}
	oc, _ := describer(t, vf, fd, 3)

	got, err := oc.Classify(context.Background(), colourCluster(t, 3))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if want := (classify.Verdict{Theme: "nature", Confidence: 0.8, Decided: true}); got != want {
		t.Errorf("verdict = %+v; want %+v", got, want)
	}
	describes, classifies := vf.counts()
	if describes != 3 || classifies != 0 {
		t.Fatalf("describe calls = %d, classify calls = %d; want 3 and 0", describes, classifies)
	}
	for i, r := range vf.describes {
		if r.images != 1 || r.system != classify.DescribeSystemPrompt || r.keepAlive != classify.DefaultKeepAlive {
			t.Errorf("describe request %d = %+v", i, r)
		}
		if r.options["temperature"] != 0.0 || r.options["seed"] != 42.0 {
			t.Errorf("describe request %d options = %v; want temperature 0, seed 42", i, r.options)
		}
	}
	_, decisions := fd.snapshot()
	if len(decisions) != 1 {
		t.Fatalf("decisions = %d; want 1", len(decisions))
	}
	if want := []string{"photo 0", "photo 1", "photo 2"}; !reflect.DeepEqual(decisions[0].Descriptions, want) {
		t.Errorf("descriptions = %q; want %q, in photo order", decisions[0].Descriptions, want)
	}
}

func TestDescriptionsAreTrimmedAndCutToTheBudget(t *testing.T) {
	long := "  " + strings.Repeat("é", 500) + " \n"
	for _, tc := range []struct {
		photos, want int
	}{
		{1, 300},  // the per-description cap binds
		{10, 240}, // the shared 2400-character budget binds
	} {
		t.Run(strconv.Itoa(tc.photos), func(t *testing.T) {
			vf := &visionFake{text: func(int) string { return long }}
			fd := &fakeDecider{}
			oc, _ := describer(t, vf, fd, tc.photos)
			if _, err := oc.Classify(context.Background(), colourCluster(t, tc.photos)); err != nil {
				t.Fatalf("Classify: %v", err)
			}
			_, decisions := fd.snapshot()
			for _, d := range decisions[0].Descriptions {
				if d != strings.Repeat("é", tc.want) {
					t.Fatalf("description = %q (%d bytes); want %d é", d, len(d), tc.want)
				}
			}
		})
	}
}

func TestEmptyDescriptionIsDropped(t *testing.T) {
	vf := &visionFake{text: func(p int) string {
		if p == 1 {
			return "   "
		}
		return fmt.Sprintf("photo %d", p)
	}}
	fd := &fakeDecider{}
	oc, _ := describer(t, vf, fd, 3)
	if _, err := oc.Classify(context.Background(), colourCluster(t, 3)); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	_, decisions := fd.snapshot()
	if want := []string{"photo 0", "photo 2"}; !reflect.DeepEqual(decisions[0].Descriptions, want) {
		t.Errorf("descriptions = %q; want %q", decisions[0].Descriptions, want)
	}
}

func TestDescribeFanOutIsBounded(t *testing.T) {
	vf := &visionFake{delay: 20 * time.Millisecond}
	oc, _ := describer(t, vf, &fakeDecider{}, 6)
	if _, err := oc.Classify(context.Background(), colourCluster(t, 6)); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	vf.mu.Lock()
	defer vf.mu.Unlock()
	if vf.peak != 2 {
		t.Errorf("peak describe calls in flight = %d; want 2 (overlapping, and no wider)", vf.peak)
	}
}

func TestDescribeThenDecideAsksTheRunsQuestion(t *testing.T) {
	vf := &visionFake{}
	fd := &fakeDecider{}
	oc, logs := describer(t, vf, fd, 3)
	c := colourCluster(t, 2)
	alt := 2710.0
	c.Photos[0].Altitude = &alt
	c.Start = time.Date(2025, 3, 2, 9, 10, 0, 0, time.UTC)
	c.End = time.Date(2025, 3, 2, 14, 55, 0, 0, time.UTC)

	for range 3 {
		if _, err := oc.Classify(context.Background(), c); err != nil {
			t.Fatalf("Classify: %v", err)
		}
	}
	readies, decisions := fd.snapshot()
	if readies != 1 {
		t.Errorf("Ready called %d times; want once per run", readies)
	}
	d := decisions[0]
	if !reflect.DeepEqual(d.Context, classify.ContextLines(c)) || len(d.Context) != 2 {
		t.Errorf("context = %q; want the capture context %q", d.Context, classify.ContextLines(c))
	}
	want := []classify.ThemeOption{
		{Slug: "family", Description: "people, portraits, family gatherings, children, daily life"},
		{Slug: "mountain", Description: "mountains, peaks, alpine landscapes, hiking, snow, skiing"},
		{Slug: "special-events", Description: "weddings, parties, concerts, ceremonies, celebrations"},
		{Slug: "nature"},
		{Slug: "other", Description: "the photos fit none of the other themes"},
	}
	if !reflect.DeepEqual(d.Options, want) || d.Fallback != "other" {
		t.Errorf("options = %+v, fallback %q; want %+v, other", d.Options, d.Fallback, want)
	}
	out := logs.String()
	if strings.Count(out, "decision service ready") != 1 || !strings.Contains(out, "msg=description path=") {
		t.Errorf("want one ready line and description records; got:\n%s", out)
	}
}

func TestUnusableDeciderFallsBackToTheSingleCall(t *testing.T) {
	vf := &visionFake{category: "family"}
	fd := &fakeDecider{ready: func(context.Context) error { return errors.New("401 invalid or missing bearer token") }}
	oc, logs := describer(t, vf, fd, 3)
	for range 3 {
		got, err := oc.Classify(context.Background(), colourCluster(t, 2))
		if err != nil {
			t.Fatalf("Classify: %v", err)
		}
		if got.Decided || got.Theme != "family" {
			t.Errorf("verdict = %+v; want the vision model's single-call family", got)
		}
	}
	readies, decisions := fd.snapshot()
	describes, classifies := vf.counts()
	if readies != 1 || len(decisions) != 0 || describes != 0 || classifies != 3 {
		t.Errorf("ready=%d decisions=%d describes=%d classifies=%d; want 1, 0, 0, 3",
			readies, len(decisions), describes, classifies)
	}
	if n := strings.Count(logs.String(), "decision service unusable: classifying with the vision model alone"); n != 1 {
		t.Errorf("unusable warnings = %d; want exactly 1:\n%s", n, logs.String())
	}
	if !strings.Contains(logs.String(), "backend=laya url=http://decider.test reason=") {
		t.Errorf("the warning must name the service and the reason:\n%s", logs.String())
	}
}

func TestInterruptedReadinessIsNotUnusable(t *testing.T) {
	vf := &visionFake{category: "family"}
	ctx, cancel := context.WithCancel(context.Background())
	fd := &fakeDecider{}
	fd.ready = func(ctx context.Context) error {
		cancel()
		return ctx.Err()
	}
	oc, logs := describer(t, vf, fd, 3)
	if _, err := oc.Classify(ctx, colourCluster(t, 2)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v; want the interrupt", err)
	}
	if strings.Contains(logs.String(), "unusable") {
		t.Errorf("an interrupt must not mark the service unusable:\n%s", logs.String())
	}
	fd.ready = nil
	got, err := oc.Classify(context.Background(), colourCluster(t, 2))
	if err != nil || !got.Decided {
		t.Errorf("after the interrupt: verdict %+v, err %v; want the readiness asked again and a decision", got, err)
	}
}

// countingExtractor serves one PNG preview per call and counts the calls, which is
// how a test sees whether an event was sampled twice.
type countingExtractor struct {
	data  []byte
	calls atomic.Int32
}

func (e *countingExtractor) Extract(context.Context, string) ([]byte, error) {
	e.calls.Add(1)
	return e.data, nil
}

func TestFailedDecisionReusesTheSampledImages(t *testing.T) {
	vf := &visionFake{category: "family"}
	fd := &fakeDecider{decide: func(n int, _ classify.Decision) (classify.Verdict, error) {
		if n == 2 {
			return classify.Verdict{}, errors.New("decision out of set: \"concert\"")
		}
		return classify.Verdict{Theme: "nature", Confidence: 0.8}, nil
	}}
	oc, logs := describer(t, vf, fd, 3)
	ex := &countingExtractor{data: colourPNG(t, 0)}
	oc.RawPreview = ex

	got := make([]classify.Verdict, 0, 3)
	for range 3 {
		v, err := oc.Classify(context.Background(), rawCluster(2))
		if err != nil {
			t.Fatalf("Classify: %v", err)
		}
		got = append(got, v)
	}
	if !got[0].Decided || got[1].Decided || got[1].Theme != "family" || !got[2].Decided {
		t.Errorf("verdicts = %+v; want event 2 alone classified by the vision model", got)
	}
	if n := ex.calls.Load(); n != 6 {
		t.Errorf("previews extracted = %d; want 6 (each event sampled once)", n)
	}
	if n := strings.Count(logs.String(), "decision failed: this event is classified by the vision model alone"); n != 1 {
		t.Errorf("decision-failed warnings = %d; want 1:\n%s", n, logs.String())
	}
}

func TestNoDescriptionTakesTheSingleCall(t *testing.T) {
	vf := &visionFake{category: "family", text: func(int) string { return "" }}
	fd := &fakeDecider{}
	oc, _ := describer(t, vf, fd, 3)
	got, err := oc.Classify(context.Background(), colourCluster(t, 2))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if _, decisions := fd.snapshot(); len(decisions) != 0 || got.Decided || got.Theme != "family" {
		t.Errorf("verdict %+v after %d decisions; want the single call and no decision", got, len(decisions))
	}
}

func TestEffectiveDescriptions(t *testing.T) {
	got := classify.EffectiveDescriptions([]string{"cook", "road-trips", "family"}, "other",
		map[string]string{"cook": "food, market stalls", "other": "screenshots, receipts"})
	want := []classify.ThemeOption{
		{Slug: "cook", Description: "food, market stalls"},
		{Slug: "road-trips"},
		{Slug: "family", Description: "people, portraits, family gatherings, children, daily life"},
		{Slug: "other", Description: "screenshots, receipts"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("EffectiveDescriptions = %+v; want %+v", got, want)
	}
	fallback := classify.EffectiveDescriptions(nil, "other", nil)
	if len(fallback) != 1 || fallback[0].Description != "the photos fit none of the other themes" {
		t.Errorf("the fallback's built-in description = %+v", fallback)
	}
}

func TestDecisionCarriesTheThemeDescriptions(t *testing.T) {
	vf := &visionFake{}
	fd := &fakeDecider{}
	oc, _ := describer(t, vf, fd, 3)
	oc.ThemeDescriptions = map[string]string{"nature": "forests, lakes, wildlife"}
	if _, err := oc.Classify(context.Background(), colourCluster(t, 1)); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	_, decisions := fd.snapshot()
	if !slices.Contains(decisions[0].Options, classify.ThemeOption{Slug: "nature", Description: "forests, lakes, wildlife"}) {
		t.Errorf("options = %+v; want nature described by the override", decisions[0].Options)
	}
}
