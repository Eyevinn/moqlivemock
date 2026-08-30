module github.com/Eyevinn/moqlivemock

go 1.25.0

require (
	github.com/Dash-Industry-Forum/livesim2 v1.9.0
	github.com/Eyevinn/moqtransport v0.10.0
	github.com/Eyevinn/mp4ff v0.56.0
	github.com/quic-go/quic-go v0.61.0
	github.com/quic-go/webtransport-go v0.12.0
	github.com/stretchr/testify v1.11.1
)

require github.com/Eyevinn/go-608 v0.9.0

require github.com/mengelbart/qlog v0.1.0 // indirect

require (
	github.com/Eyevinn/locmaf v0.2.1
	github.com/beevik/etree v1.5.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/dunglas/httpsfv v1.1.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/quic-go/qpack v0.6.0 // indirect
	golang.org/x/crypto v0.54.0 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// The draft-18 rewrite of moqtransport is developed alongside this module, so
// this points at the sibling checkout rather than a release. It must not
// survive onto main: a local path replace breaks the build for anyone else.
replace github.com/Eyevinn/moqtransport => ../moqtransport

replace github.com/quic-go/webtransport-go => github.com/Eyevinn/webtransport-go v0.0.0-20260806102014-dfc839273d65
