package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/photo"
)

func mkPhotos(n int, base time.Time) []photo.Photo {
	out := make([]photo.Photo, n)
	for i := range out {
		out[i] = photo.Photo{
			Path:   "/src/IMG_" + itoa(i) + ".jpg",
			Name:   "IMG_" + itoa(i) + ".jpg",
			Taken:  base.Add(time.Duration(i) * time.Minute),
			Format: photo.JPEG,
		}
	}
	return out
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func TestAddGroupAllocatesStableIDs(t *testing.T) {
	s := New("/src", "/src/_trie")
	base := time.Date(2025, 8, 12, 8, 0, 0, 0, time.UTC)

	g1 := s.AddGroup("a", "a/2025-08-12", base, base, mkPhotos(2, base))
	g2 := s.AddGroup("b", "b/2025-08-13", base.Add(24*time.Hour), base.Add(24*time.Hour), mkPhotos(1, base.Add(24*time.Hour)))

	if g1.ID != "g1" || g2.ID != "g2" {
		t.Fatalf("group IDs = %q, %q; want g1, g2", g1.ID, g2.ID)
	}
	if g1.Photos[0].ID != "p1" || g1.Photos[1].ID != "p2" || g2.Photos[0].ID != "p3" {
		t.Fatalf("photo IDs not monotonic: %q %q %q", g1.Photos[0].ID, g1.Photos[1].ID, g2.Photos[0].ID)
	}
	// Index points each photo to its group.
	if got := s.index["p1"]; got != "g1" {
		t.Errorf("index[p1] = %q; want g1", got)
	}
	if got := s.index["p3"]; got != "g2" {
		t.Errorf("index[p3] = %q; want g2", got)
	}
}

func TestAddGroupEmptyReturnsNil(t *testing.T) {
	s := New("/src", "/dst")
	if g := s.AddGroup("x", "x", time.Now(), time.Now(), nil); g != nil {
		t.Fatalf("AddGroup with no photos = %v; want nil (I3)", g)
	}
	if len(s.Snapshot().Groups) != 0 {
		t.Fatal("empty group must not be stored or serialised")
	}
}

func TestSnapshotChronologicalOrderAndCount(t *testing.T) {
	s := New("/src", "/dst")
	t1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	t3 := time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)

	// Insert out of chronological order.
	s.AddGroup("mid", "mid", t2, t2, mkPhotos(3, t2))
	s.AddGroup("late", "late", t3, t3, mkPhotos(1, t3))
	s.AddGroup("early", "early", t1, t1, mkPhotos(2, t1))

	snap := s.Snapshot()
	if len(snap.Groups) != 3 {
		t.Fatalf("got %d groups; want 3", len(snap.Groups))
	}
	wantLabels := []string{"early", "mid", "late"}
	wantCounts := []int{2, 3, 1}
	for i, g := range snap.Groups {
		if g.Label != wantLabels[i] {
			t.Errorf("group %d label = %q; want %q (chronological)", i, g.Label, wantLabels[i])
		}
		if g.Count != wantCounts[i] {
			t.Errorf("group %d count = %d; want %d", i, g.Count, wantCounts[i])
		}
		if g.Count != len(g.Photos) {
			t.Errorf("group %d count %d != len(photos) %d", i, g.Count, len(g.Photos))
		}
	}
}

func TestSnapshotJSONSnakeCase(t *testing.T) {
	s := New("/src", "/dst")
	base := time.Date(2025, 8, 12, 8, 14, 0, 0, time.UTC)
	s.AddGroup("sortie montagne", "sortie montagne/2025-08-12", base, base, mkPhotos(1, base))

	b, err := json.Marshal(s.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	for _, key := range []string{`"groups"`, `"dest_subdir"`, `"thumb_url"`, `"photo_url"`, `"count"`} {
		if !contains(js, key) {
			t.Errorf("JSON missing snake_case key %s in: %s", key, js)
		}
	}
	if contains(js, `"DestSubdir"`) || contains(js, `"ThumbURL"`) {
		t.Errorf("JSON leaked Go field names: %s", js)
	}
	// thumb_url / photo_url derived from photo ID.
	if !contains(js, `"thumb_url":"/thumb/p1"`) || !contains(js, `"photo_url":"/photo/p1"`) {
		t.Errorf("derived URLs wrong: %s", js)
	}
}

func TestPhotoLookup(t *testing.T) {
	s := New("/src", "/dst")
	base := time.Now()
	s.AddGroup("g", "g", base, base, mkPhotos(2, base))

	ref, ok := s.Photo("p2")
	if !ok {
		t.Fatal("Photo(p2) not found")
	}
	if ref.Name != "IMG_1.jpg" {
		t.Errorf("ref.Name = %q; want IMG_1.jpg", ref.Name)
	}
	if _, ok := s.Photo("p999"); ok {
		t.Error("Photo(p999) should not be found")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
