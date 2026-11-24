/*
   Copyright 2026 GitHub Inc.
	 See https://github.com/github/gh-ost/blob/master/LICENSE
*/

package logic

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
)

// maxIncompleteProgressPct is the highest progress reported while row copy is
// still running. The row estimate (EXPLAIN / table status, optionally a
// COUNT(*)) can be far below the real row count, so rows-copied/estimate can
// exceed 100% long before the copy finishes; never claim 100% until it has.
const maxIncompleteProgressPct = 99.9

// progressBasis says how a progress percentage was derived, which determines
// how (and whether) an ETA can be computed from it.
type progressBasis int

const (
	// progressByRows: rows copied / rows estimate (the default).
	progressByRows progressBasis = iota
	// progressByKeyRange: position of the copy cursor within the unique key's
	// [min, max] range; used once the row estimate has been exceeded.
	progressByKeyRange
	// progressCapped: the row estimate was exceeded and no key-range position is
	// available; progress is pinned at maxIncompleteProgressPct, ETA unknown.
	progressCapped
)

var integerColumnTypeRegexp = regexp.MustCompile(`^(tiny|small|medium|big)?int\b`)

// keyRangeProgressFraction returns how far (0..1) the row copy has advanced
// through the migration range, based on the upper bound of the last copied
// chunk. Only supported for single-column integer unique keys, where key
// distance is a meaningful proxy for remaining work.
func (mgtr *Migrator) keyRangeProgressFraction() (fraction float64, ok bool) {
	uniqueKey := mgtr.migrationContext.UniqueKey
	if uniqueKey == nil || uniqueKey.Len() != 1 {
		return 0, false
	}
	column := uniqueKey.Columns.Columns()[0]
	if !integerColumnTypeRegexp.MatchString(strings.ToLower(column.MySQLType)) {
		return 0, false
	}
	rangeMin := mgtr.migrationContext.MigrationRangeMinValues
	rangeMax := mgtr.migrationContext.MigrationRangeMaxValues
	if rangeMin == nil || rangeMax == nil || mgtr.applier == nil {
		return 0, false
	}
	minValue, minOK := columnValueToFloat(rangeMin.AbstractValues()[0])
	maxValue, maxOK := columnValueToFloat(rangeMax.AbstractValues()[0])
	if !minOK || !maxOK || maxValue <= minValue {
		return 0, false
	}

	mgtr.applier.LastIterationRangeMutex.Lock()
	lastCopied := mgtr.applier.LastIterationRangeMaxValues
	mgtr.applier.LastIterationRangeMutex.Unlock()
	if lastCopied == nil {
		// no chunk copied yet
		return 0, true
	}
	position, positionOK := columnValueToFloat(lastCopied.AbstractValues()[0])
	if !positionOK {
		return 0, false
	}

	fraction = (position - minValue) / (maxValue - minValue)
	if fraction < 0 {
		fraction = 0
	} else if fraction > 1 {
		fraction = 1
	}
	return fraction, true
}

// columnValueToFloat converts a scanned integer column value to float64.
// float64 loses precision above 2^53, which is irrelevant for a progress ratio.
func columnValueToFloat(value interface{}) (float64, bool) {
	var s string
	switch v := value.(type) {
	case nil:
		return 0, false
	case []byte:
		s = string(v)
	case string:
		s = v
	default:
		s = fmt.Sprintf("%v", v)
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// getProgressPercentAndBasis returns migration progress as a percent and how
// it was derived. See progressBasis.
func (mgtr *Migrator) getProgressPercentAndBasis(rowsEstimate int64) (progressPct float64, basis progressBasis) {
	progressPct = 100.0
	if rowsEstimate <= 0 {
		return progressPct, progressByRows
	}
	progressPct *= float64(mgtr.migrationContext.GetTotalRowsCopied()) / float64(rowsEstimate)
	if progressPct < 100.0 || atomic.LoadInt64(&mgtr.rowCopyCompleteFlag) == 1 {
		return progressPct, progressByRows
	}
	// Copied at least as many rows as estimated, but row copy is not done:
	// the estimate is wrong, so it can't tell us how far along we are.
	if fraction, ok := mgtr.keyRangeProgressFraction(); ok {
		return min(fraction*100.0, maxIncompleteProgressPct), progressByKeyRange
	}
	return maxIncompleteProgressPct, progressCapped
}
