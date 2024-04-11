package annotate

import (
	"context"
	"fmt"
)

func Err(ctx context.Context, prefix string, err error) error {
	ctxErr := ctx.Err()

	if ctxErr == nil {
		// The context is still valid/not done, we don't have extra info to add.
		return fmt.Errorf("%s: %w", prefix, err)
	}

	// Add info that the context was cancelled or deadline exceeded.
	return fmt.Errorf("%s: %w (%w)", prefix, err, ctxErr)
}
