package dock

import "github.com/mpdroog/osxflow/internal/stack"

// defaultTrashCount is split out so that model.go has no direct dependency
// on the filesystem, which is what lets the item-building tests run
// without a trash directory.
func defaultTrashCount(dir string) int {
	if dir == "" {
		return 0
	}
	return stack.TrashCount(dir)
}
