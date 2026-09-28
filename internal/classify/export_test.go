package classify

import (
	"context"

	"github.com/sgaunet/moraine/internal/photo"
)

// Test-only exports so black-box tests can reach unexported helpers.
var (
	EvenlySpaced   = evenlySpaced
	NormaliseTheme = normaliseTheme
	Shrink         = shrink
	MaxImageDim    = maxImageDim
	MaxImagePixels = maxImagePixels
	TallyVotes     = tallyVotes
)

func (o *OllamaClassifier) ChoosePhotos(c photo.Cluster) []photo.Photo { return o.choosePhotos(c) }

func (o *OllamaClassifier) SampleImages(ctx context.Context, c photo.Cluster) []string {
	sample := o.sampleImages(ctx, c)
	if sample == nil {
		return nil
	}
	images := make([]string, len(sample))
	for i, s := range sample {
		images[i] = s.data
	}
	return images
}

// DescribeSystemPrompt is the prompt every describe request carries.
const DescribeSystemPrompt = describeSystemPrompt
