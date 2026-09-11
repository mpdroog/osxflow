package dock

import (
	"errors"

	"github.com/mpdroog/osxflow/internal/stack"
)

// errNoTrashDir reports a Builder with no trash directory, which is what
// the dock is left with when stack.TrashDir could not find one. The dock
// logged why at startup; this keeps the trash icon from quietly claiming
// to be empty on every refresh after that.
var errNoTrashDir = errors.New("no trash directory")

// defaultTrashCount is split out so that model.go has no direct dependency
// on the filesystem, which is what lets the item-building tests run
// without a trash directory.
func defaultTrashCount(dir string) (int, error) {
	if dir == "" {
		return 0, errNoTrashDir
	}
	return stack.TrashCount(dir)
}
