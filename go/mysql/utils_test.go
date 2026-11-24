/*
   Copyright 2026 GitHub Inc.
	 See https://github.com/github/gh-ost/blob/master/LICENSE
*/

package mysql

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestKillRejectsNonNumericConnectionID(t *testing.T) {
	// validated before touching the DB, so a nil *sql.DB is fine here
	require.Error(t, Kill(nil, "1; drop table x"))
	require.Error(t, Kill(nil, ""))
}
