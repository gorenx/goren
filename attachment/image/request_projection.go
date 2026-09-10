package image

import (
	"errors"
	"math"
)

// RequestImageDimensions reproduces Harness's inward-rounded projection under
// a total-pixel budget. It does not enlarge small images. Inputs must be positive
// and their area must fit the exact integer range of the source JSON protocol.
func RequestImageDimensions(width, height, maxPixels int64) (ImageDimensions, error) {
	const maxSafeInteger int64 = 1<<53 - 1
	if width <= 0 || height <= 0 || maxPixels <= 0 ||
		width > maxSafeInteger/height || maxPixels > maxSafeInteger {
		return ImageDimensions{}, errors.New("attachment: image dimensions and pixel budget must be positive safe integers")
	}
	scale := math.Min(1, math.Sqrt(float64(maxPixels)/float64(width*height)))
	if scale == 1 {
		return ImageDimensions{
			Width:  width,
			Height: height,
		}, nil
	}
	if width >= height {
		projectedWidth := max(int64(1), int64(math.Floor(float64(width)*scale)))
		projectedHeight := max(int64(1), int64(math.Round(float64(projectedWidth)*float64(height)/float64(width))))
		for projectedWidth > maxPixels/projectedHeight && projectedWidth > 1 {
			projectedWidth--
			projectedHeight = max(int64(1), int64(math.Round(float64(projectedWidth)*float64(height)/float64(width))))
		}
		return ImageDimensions{
			Width:  projectedWidth,
			Height: projectedHeight,
		}, nil
	}
	projectedHeight := max(int64(1), int64(math.Floor(float64(height)*scale)))
	projectedWidth := max(int64(1), int64(math.Round(float64(projectedHeight)*float64(width)/float64(height))))
	for projectedWidth > maxPixels/projectedHeight && projectedHeight > 1 {
		projectedHeight--
		projectedWidth = max(int64(1), int64(math.Round(float64(projectedHeight)*float64(width)/float64(height))))
	}
	return ImageDimensions{
		Width:  projectedWidth,
		Height: projectedHeight,
	}, nil
}
