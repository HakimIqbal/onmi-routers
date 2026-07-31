package compression

import (
	"foxrouters/internal/rtk"
)

// rtkCompressInPlace runs the existing RTK tool-output filter engine on the
// body in place. Returns the RTK stats (nil if nothing was compressed). This
// bridges the aggressive pipeline's Step 1 to the battle-tested RTK filters.
func rtkCompressInPlace(body map[string]any) *rtk.Stats {
	return rtk.CompressMessages(body)
}
