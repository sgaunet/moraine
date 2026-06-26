package classify

import (
	"context"

	"github.com/sgaunet/moraine/internal/photo"
)

// Test-only exports so black-box tests can reach unexported helpers.
var (
	EvenlySpaced   = evenlySpaced
	NormaliseTheme = normaliseTheme
)

func (o *OllamaClassifier) ChoosePhotos(c photo.Cluster) []photo.Photo { return o.choosePhotos(c) }

func (o *OllamaClassifier) SampleImages(ctx context.Context, c photo.Cluster) []string {
	return o.sampleImages(ctx, c)
}
