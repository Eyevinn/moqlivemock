module github.com/Eyevinn/moqlivemock

go 1.26.0

require (
	github.com/Dash-Industry-Forum/livesim2 v1.9.0
	github.com/Eyevinn/moqtransport v0.15.1
	github.com/Eyevinn/mp4ff v0.60.0
	github.com/quic-go/quic-go v0.62.0
	github.com/quic-go/webtransport-go v0.13.0
	github.com/stretchr/testify v1.12.1
)

require github.com/Eyevinn/go-608 v0.9.0

require github.com/mengelbart/qlog v0.1.1-0.20260825155429-978b2062c546

require go.yaml.in/yaml/v3 v3.0.5 // indirect

require (
	github.com/Eyevinn/locmaf v0.2.1
	github.com/beevik/etree v1.5.0 // indirect
	github.com/dunglas/httpsfv v1.1.1 // indirect
	github.com/quic-go/qpack v0.6.0 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)

replace github.com/quic-go/webtransport-go => github.com/Eyevinn/webtransport-go v0.0.0-20261016122133-4c2adc0635bf
