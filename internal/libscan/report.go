package libscan

import "encoding/json"

// Fit makes the report fit the limits: at most MaxItems items and MaxReportBytes
// of JSON. It trims notes first (to four, then two, then none), then drops the
// items worth least (invalid, then warnings, from the end of the list) and
// marks the report partial. The same function runs before a report is sent and
// before it is printed.
func Fit(r *Report) {
	if len(r.Items) > MaxItems {
		dropLeast(r, len(r.Items)-MaxItems)
	}
	if size(r) <= MaxReportBytes {
		return
	}
	for _, keep := range []int{4, 2, 0} {
		for i := range r.Items {
			if len(r.Items[i].Notes) > keep {
				r.Items[i].Notes = r.Items[i].Notes[:keep]
			}
		}
		if size(r) <= MaxReportBytes {
			return
		}
	}
	for size(r) > MaxReportBytes && len(r.Items) > 0 {
		n := max(1, len(r.Items)/10)
		dropLeast(r, n)
	}
}

func size(r *Report) int {
	b, err := json.Marshal(r)
	if err != nil {
		return MaxReportBytes + 1
	}
	return len(b)
}

// dropLeast removes n items: first the invalid ones, then those with warnings,
// then valid ones, each from the end of the (sorted) list.
func dropLeast(r *Report, n int) {
	if n <= 0 {
		return
	}
	r.Partial = true
	drop := map[int]bool{}
	for _, v := range []string{Invalid, Warning, Valid} {
		for i := len(r.Items) - 1; i >= 0 && len(drop) < n; i-- {
			if r.Items[i].Verdict == v && !drop[i] {
				drop[i] = true
			}
		}
	}
	kept := r.Items[:0:0]
	for i, it := range r.Items {
		if !drop[i] {
			kept = append(kept, it)
		}
	}
	r.Items = kept
}
