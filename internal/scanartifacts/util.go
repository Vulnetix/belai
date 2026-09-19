package scanartifacts

import (
	"errors"
	"fmt"
	"io"
)

// budgetReadAll reads r up to maxBytes. maxBytes <= 0 means no limit (other
// than the HardMaxBytes hard cap).
func budgetReadAll(r io.Reader, maxBytes int64) ([]byte, error) {
	limit := int64(HardMaxBytes)
	if maxBytes > 0 && maxBytes < limit {
		limit = maxBytes
	}
	data, err := io.ReadAll(io.LimitReader(r, limit))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) >= limit {
		return nil, fmt.Errorf("artifact exceeds %d byte budget: %w", limit, errors.New("size limit"))
	}
	return data, nil
}

func parseFloatString(s string) (float64, error) {
	var f float64
	_, err := fmt.Sscanf(s, "%f", &f)
	return f, err
}
