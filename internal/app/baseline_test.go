package app_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/sgaunet/moraine/internal/app"
	"github.com/sgaunet/moraine/internal/classify"
)

// TestOrganizeSingleCallBaseline pins what a run without a decision service reports,
// so the describe-then-decide feature can be shown to leave it alone (SC-004, the
// in-suite half; the binary-level diff is the other half). Two events, both
// classified by the vision model in one call each.
func TestOrganizeSingleCallBaseline(t *testing.T) {
	src, dest := eventSource(t, 2), t.TempDir()
	srv := ollamaStub(t, nil)
	defer srv.Close()
	cfg := modelCfg(src, dest, srv.URL)
	cfg.Sample = 3

	sum, err := app.Organize(context.Background(), cfg, quietLogger(), nil, nil)
	if err != nil {
		t.Fatalf("Organize: %v", err)
	}
	if len(sum.Events) != 2 {
		t.Fatalf("events = %d; want 2", len(sum.Events))
	}
	for i, ev := range sum.Events {
		if ev.Method != string(classify.MethodModelAll) || ev.Theme != "mountain" {
			t.Errorf("event %d = (%s, %s); want (mountain, %s)", i, ev.Theme, ev.Method, classify.MethodModelAll)
		}
	}
	sum.Events = nil
	want := app.Summary{Scanned: 2, Groups: 2, Copied: 2, BytesCopied: sum.BytesCopied}
	if !reflect.DeepEqual(sum, want) {
		t.Errorf("summary = %+v; want %+v", sum, want)
	}
	if sum.BytesCopied <= 0 {
		t.Errorf("bytes copied = %d; want > 0", sum.BytesCopied)
	}
}
