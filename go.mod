module academic/backend

go 1.24.0

require (
	github.com/coder/websocket v1.8.14
	github.com/go-chi/chi/v5 v5.2.3
	github.com/jackc/pgx/v5 v5.7.6
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e
	golang.org/x/crypto v0.37.0
	golang.org/x/net v0.38.0
)

require github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect

require (
	academic/agent/framework v0.0.0
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/sync v0.13.0 // indirect
	golang.org/x/text v0.24.0 // indirect
)

replace academic/agent/framework => ../agent/framework
