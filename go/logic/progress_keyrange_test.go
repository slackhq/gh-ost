/*
   Copyright 2026 GitHub Inc.
	 See https://github.com/github/gh-ost/blob/master/LICENSE
*/

package logic

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/github/gh-ost/go/base"
	"github.com/github/gh-ost/go/sql"
)

func newKeyRangeTestMigrator(t *testing.T, mysqlType string, min, max, lastCopied interface{}) (*Migrator, *base.MigrationContext) {
	t.Helper()
	migrationContext := base.NewMigrationContext()
	columns := sql.NewColumnList([]string{"id"})
	columns.GetColumn("id").MySQLType = mysqlType
	migrationContext.UniqueKey = &sql.UniqueKey{Name: "PRIMARY", Columns: *columns}
	migrationContext.MigrationRangeMinValues = sql.ToColumnValues([]interface{}{min})
	migrationContext.MigrationRangeMaxValues = sql.ToColumnValues([]interface{}{max})
	migrator := NewMigrator(migrationContext, "1.2.3")
	migrator.applier = NewApplier(migrationContext)
	if lastCopied != nil {
		migrator.applier.LastIterationRangeMaxValues = sql.ToColumnValues([]interface{}{lastCopied})
	}
	return migrator, migrationContext
}

func TestProgressNeverReaches100WhileCopying(t *testing.T) {
	migrationContext := base.NewMigrationContext()
	migrator := NewMigrator(migrationContext, "1.2.3")
	migrationContext.TotalRowsCopied = 409039350

	pct, basis := migrator.getProgressPercentAndBasis(173991412)
	require.Equal(t, maxIncompleteProgressPct, pct)
	require.Equal(t, progressCapped, basis)

	// once row copy is complete the rows ratio is reported as-is
	atomic.StoreInt64(&migrator.rowCopyCompleteFlag, 1)
	pct, basis = migrator.getProgressPercentAndBasis(173991412)
	require.Greater(t, pct, 100.0)
	require.Equal(t, progressByRows, basis)
}

func TestProgressUsesRowsWhileUnderEstimate(t *testing.T) {
	migrator, migrationContext := newKeyRangeTestMigrator(t, "bigint unsigned", "0", "1000", "900")
	migrationContext.TotalRowsCopied = 250
	pct, basis := migrator.getProgressPercentAndBasis(1000)
	require.Equal(t, 25.0, pct)
	require.Equal(t, progressByRows, basis)
}

func TestProgressFallsBackToKeyRange(t *testing.T) {
	// MySQL text protocol scans integers as []byte
	migrator, migrationContext := newKeyRangeTestMigrator(t, "bigint unsigned",
		[]byte("2147483901"), []byte("12658289473408"), []byte("6330218478654"))
	migrationContext.TotalRowsCopied = 409039350
	now := time.Now()
	migrationContext.RowCopyStartTime = now.Add(-time.Hour)
	migrationContext.RowCopyEndTime = now

	pct, basis := migrator.getProgressPercentAndBasis(173991412)
	require.Equal(t, progressByKeyRange, basis)
	require.InDelta(t, 50.0, pct, 0.01)

	// half the key range in an hour: about an hour to go
	state, eta, etaDuration := migrator.getMigrationStateAndETA(173991412)
	require.Equal(t, "migrating", state)
	require.NotEqual(t, "due", eta)
	require.InDelta(t, time.Hour.Seconds(), etaDuration.Seconds(), 60)
}

func TestProgressKeyRangeCappedBelow100(t *testing.T) {
	migrator, migrationContext := newKeyRangeTestMigrator(t, "int", int64(1), int64(1000), int64(1000))
	migrationContext.TotalRowsCopied = 5000
	pct, basis := migrator.getProgressPercentAndBasis(1000)
	require.Equal(t, progressByKeyRange, basis)
	require.Equal(t, maxIncompleteProgressPct, pct)
}

func TestKeyRangeProgressFractionUnsupported(t *testing.T) {
	cases := []struct {
		name      string
		mysqlType string
		min, max  interface{}
	}{
		{"varchar key", "varchar(255)", "a", "z"},
		{"point is not an int", "point", "1", "9"},
		{"empty range", "bigint", "5", "5"},
		{"unparseable", "bigint", "x", "9"},
		{"nil", "bigint", nil, "9"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			migrator, _ := newKeyRangeTestMigrator(t, tc.mysqlType, tc.min, tc.max, "3")
			_, ok := migrator.keyRangeProgressFraction()
			require.False(t, ok)
		})
	}

	t.Run("composite key", func(t *testing.T) {
		migrator, migrationContext := newKeyRangeTestMigrator(t, "bigint", "1", "9", "3")
		columns := sql.NewColumnList([]string{"a", "b"})
		migrationContext.UniqueKey = &sql.UniqueKey{Name: "ab", Columns: *columns}
		_, ok := migrator.keyRangeProgressFraction()
		require.False(t, ok)
	})
}

func TestKeyRangeProgressFractionBeforeFirstChunk(t *testing.T) {
	migrator, _ := newKeyRangeTestMigrator(t, "bigint", "1", "9", nil)
	fraction, ok := migrator.keyRangeProgressFraction()
	require.True(t, ok)
	require.Equal(t, 0.0, fraction)
}
