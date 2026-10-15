module github.com/PeacexF/pigeon/contrib/pgqueue

go 1.27.0

require (
	github.com/PeacexF/pigeon v0.0.0-00010101000000-000000000000
	github.com/jackc/pgx/v5 v5.11.0
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/sync v0.17.0 // indirect
	golang.org/x/text v0.29.0 // indirect
)

// Developed against the pigeon in this repository; consumers get the
// version required above, since replace only applies to the main module.
replace github.com/PeacexF/pigeon => ../..
