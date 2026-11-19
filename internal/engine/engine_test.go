package engine

import "testing"

func TestValidateDSNHost(t *testing.T) {
	cases := []struct {
		dsn     string
		allow   bool
		wantErr bool
	}{
		{"postgres://u:p@localhost:5432/db", false, false},
		{"postgres://u:p@127.0.0.1:5432/db", false, false},
		{"postgres://u:p@[::1]:5432/db", false, false},
		{"host=/var/run/postgresql dbname=x", false, false},
		{"postgres://u:p@db.internal:5432/db", false, true},
		{"postgres://u:p@10.0.0.5:5432/db", false, true},
		{"postgres://u:p@db.internal:5432/db", true, false},
		{"u:p@tcp(127.0.0.1:3306)/db", false, false},
		{"u:p@tcp(db.internal:3306)/db", false, true},
		{"u:p@tcp(db.internal:3306)/db", true, false},
	}
	for _, tc := range cases {
		err := ValidateDSNHost(tc.dsn, tc.allow)
		if (err != nil) != tc.wantErr {
			t.Errorf("ValidateDSNHost(%q, %v) err=%v wantErr=%v", tc.dsn, tc.allow, err, tc.wantErr)
		}
	}
}
