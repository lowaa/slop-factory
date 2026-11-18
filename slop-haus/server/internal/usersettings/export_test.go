package usersettings

// LatestSQL is the SQL the store itself runs to read a user's latest rows.
func LatestSQL(s *PostgresStore) string { return s.table.LatestSQL() }

var AsUser = asUser
