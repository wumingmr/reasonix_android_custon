package browser

import (
	"fmt"
	"image/png"
	"io"
	"os"
)

const screenshotMaxPixels = 16_777_216

// Validate before either image admission or the oversize-file response. An empty
// or corrupt host artifact must never be described as a successful screenshot.
func validateScreenshot(f *os.File, shot Screenshot) error {
	if shot.MIME != "" && shot.MIME != "image/png" {
		return fmt.Errorf("invalid_image: screenshot MIME is not image/png")
	}
	config, err := png.DecodeConfig(io.LimitReader(f, 1<<20))
	if err != nil {
		return fmt.Errorf("invalid_image: invalid PNG header: %w", err)
	}
	if config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > screenshotMaxPixels {
		return fmt.Errorf("invalid_image: screenshot exceeds the pixel budget")
	}
	if config.Width != shot.Width || config.Height != shot.Height {
		return fmt.Errorf("invalid_image: screenshot dimensions %dx%d disagree with host %dx%d", config.Width, config.Height, shot.Width, shot.Height)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := png.Decode(io.LimitReader(f, 64<<20)); err != nil {
		return fmt.Errorf("invalid_image: screenshot PNG cannot be decoded: %w", err)
	}
	_, err = f.Seek(0, io.SeekStart)
	return err
}
