package scanartifacts

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// BOMRow is one component of a CBOM or AIBOM reduced to identifier fields: a
// name and version cleaned to identifier characters, the CycloneDX type, and
// the Vulnetix category and post-quantum status properties. No description,
// evidence or free text is read.
type BOMRow struct {
	Type     string
	Name     string
	Version  string
	Category string
	PQC      string
}

// BOMRows reads at most limit components of a CycloneDX document as BOMRow
// values. It streams the components array, so a large document is never held
// whole, and a field that is not identifier-shaped is cleaned the way the
// review findings are (plain).
func BOMRows(ctx context.Context, path string, maxBytes int64, limit int) ([]BOMRow, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	max := int64(HardMaxBytes)
	if maxBytes > 0 && maxBytes < max {
		max = maxBytes
	}
	dec := json.NewDecoder(io.LimitReader(file, max))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("parse cyclonedx: not an object")
	}
	var rows []BOMRow
	for dec.More() {
		if ctx.Err() != nil {
			return rows, ctx.Err()
		}
		tok, err := dec.Token()
		if err != nil {
			return rows, err
		}
		if key, _ := tok.(string); key != "components" {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return rows, err
			}
			continue
		}
		err = streamArray(ctx, dec, func() error {
			var c cdxComponent
			if err := dec.Decode(&c); err != nil {
				return err
			}
			if len(rows) >= limit {
				return nil
			}
			cat := componentProp(&c, "vulnetix:crypto/category")
			if cat == "" {
				cat = componentProp(&c, "vulnetix:ai/category")
			}
			rows = append(rows, BOMRow{
				Type: plain(c.Type), Name: plain(c.Name), Version: plain(c.Version),
				Category: plain(cat), PQC: strings.ToLower(plain(componentProp(&c, "vulnetix:crypto/pqc-status"))),
			})
			return nil
		})
		if err != nil {
			return rows, err
		}
	}
	return rows, nil
}

// ReadSARIFFindings reads the first run of a SARIF file as ReviewFinding
// values of the given kind: one per rule and file, identifiers only. Unlike
// ReadReviewFindings it does not require the file to record a commit.
func ReadSARIFFindings(path, kind string) ([]ReviewFinding, error) {
	return sarifFindings(path, kind)
}
