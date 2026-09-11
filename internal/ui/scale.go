package ui

// Display scaling moved to internal/scale when the dock needed it too:
// both tools have to draw at the same size as the desktop around them, and
// there is only one right answer to what that size is.
//
// These wrappers stay because the launcher's tests are written against
// them, and because DetectScale reads better than scale.Detect at the one
// call site in this package.

import (
	"github.com/jezek/xgbutil"

	"github.com/mpdroog/osxflow/internal/scale"
)

// DetectScale returns the display scale factor, or 1 when nothing says
// otherwise. A positive override wins outright, which is what the -scale
// flag is for. The factor is always usable; the error says why it may not
// match the desktop, as scale.Detect describes.
func DetectScale(xu *xgbutil.XUtil, override float64) (float64, error) {
	return scale.Detect(xu, override)
}

func scaleFromEnv() (float64, error) { return scale.FromEnv() }

func scaleFromXfconfFile(path string) (float64, error) { return scale.FromXfconfFile(path) }
