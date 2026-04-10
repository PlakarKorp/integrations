package logical

import (
	"context"
	"os"
	"testing"

	"github.com/PlakarKorp/integration-mysql/tests/testhelpers"
)

// TestMain pre-builds the plakar test images for all variants before any test
// runs, so that individual tests share the cached image and do not each pay
// the build cost.
func TestMain(m *testing.M) {
	ctx := context.Background()
	for _, v := range testhelpers.DBVariants {
		if err := testhelpers.PreBuildPlakarImage(ctx, v); err != nil {
			// Non-fatal: the individual test will fail with a clearer error.
			_ = err
		}
	}
	os.Exit(m.Run())
}
