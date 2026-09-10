package image_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/gorenx/goren/attachment/image"
)

func TestHarnessRequestProjectionFixtures(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("testdata/request-projection.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Width     int64                 `json:"width"`
		Height    int64                 `json:"height"`
		MaxPixels int64                 `json:"maxPixels"`
		Expected  image.ImageDimensions `json:"expected"`
	}
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) < 100 {
		t.Fatal("missing differential geometry cases")
	}
	for _, sample := range fixtures {
		got, err := image.RequestImageDimensions(sample.Width, sample.Height, sample.MaxPixels)
		if err != nil || got != sample.Expected {
			t.Fatalf("%dx%d budget %d: got %+v, %v; want %+v", sample.Width, sample.Height, sample.MaxPixels, got, err, sample.Expected)
		}
		if got.Width*got.Height > sample.MaxPixels || got.Width > sample.Width || got.Height > sample.Height {
			t.Fatalf("projection exceeded input or budget: %+v", sample)
		}
	}
}

func TestRequestProjectionRejectsInvalidArithmetic(t *testing.T) {
	t.Parallel()
	for _, input := range [][3]int64{
		{0, 1, 1}, {-1, 1, 1}, {1, 0, 1}, {1, 1, 0},
		{1 << 53, 1, 1}, {1 << 32, 1 << 32, 1}, {1, 1, 1 << 53},
	} {
		if _, err := image.RequestImageDimensions(input[0], input[1], input[2]); err == nil {
			t.Fatalf("accepted invalid geometry %v", input)
		}
	}
}
