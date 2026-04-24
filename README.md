# moqlivemock

![Test](https://github.com/Eyevinn/moqlivemock/workflows/Go/badge.svg)
[![Coverage Status](https://coveralls.io/repos/github/Eyevinn/moqlivemock/badge.svg?branch=main)](https://coveralls.io/github/Eyevinn/moqlivemock?branch=main)
[![GoDoc](https://godoc.org/github.com/Eyevinn/moqlivemock?status.svg)](http://godoc.org/github.com/Eyevinn/moqlivemock)
[![Go Report Card](https://goreportcard.com/badge/github.com/Eyevinn/moqlivemock)](https://goreportcard.com/report/github.com/Eyevinn/moqlivemock)
[![license](https://img.shields.io/github/license/Eyevinn/moqlivemock.svg)](https://github.com/Eyevinn/moqlivemock/blob/master/LICENSE)

moqlivemock is a simple media test service for [MOQ Transport][moqt]
and the [MSF][MSF]/[CMSF][CMSF] streaming format by providing a server which
publishes an asset with wall-clock synchronized multi-bitrate video,
audio tracks, and dynamically-generated subtitle tracks (WVTT and STPP),
as well as a client that can receive these streams and even multiplex
video and audio for playback with ffplay like `mlmsub -muxout - | ffplay -`.

Video tracks use `avc1` (H.264) and `hvc1` (HEVC) sample descriptors with
parameter sets stored in the init segment, which is required for FairPlay
DRM support in Safari 26.4+.

The input media is 10s of video and audio which is then disassembled
into frames. One or more frames are then combined into a MoQ object as a CMAF chunk.
How many frames are combined is configurable via the `-audiobatch` and `-videobatch` options.

Subtitles are generated on the fly and delivered as 1s groups with 1 object per group.
That object is published at the start of each second in order to not increase the latency.

### Wall-clock alignment

All streams are aligned to UTC wall-clock time at two levels:

1. **The 10-second asset loop** is aligned to UTC modulo 10 seconds.
   The first sample of the clip maps to epoch times where `seconds % 10 == 0`.
   This means every subscriber joining at the same wall-clock time receives
   the same content, regardless of when the publisher was started.
2. **MoQ groups** are aligned to full UTC seconds. Each group number is
   `Unix_epoch_ms / 1000`, so group boundaries fall on exact second boundaries.
   Audio is typically not compatible with integral seconds, so minimal
   displacement is applied without accumulated drift over time.

LOC is currently not supported, but one possible scenario is to send LOC over the wire and
then reassamble CMAF on the receiving side again.

This project uses [moqtransport][moqtransport] for the MoQ transport layer,
supporting both draft-14 and draft-16 of MOQT. Draft-16 uses ALPN-based version
negotiation (`moqt-16`) and `WT-Available-Protocols` for WebTransport. Draft-14
(`moq-00`) is supported for backward compatibility.

## Namespaces

mlmpub announces one or more namespaces depending on the configured protection modes.
Each namespace has its own MSF catalog containing only the relevant tracks:

| Namespace | Condition | Track suffix | Description |
|-----------|-----------|--------------|-------------|
| `cmsf/clear` | Always | *(none)* | Unencrypted tracks |
| `cmsf/drm-{scheme}` | `-drmpath` set | `_drm` | Commercial DRM (Widevine/PlayReady/FairPlay via CPIX) |
| `cmsf/eccp-{scheme}` | `-kid`/`-iv` set | `_eccp` | ClearKey/ECCP (explicit key over HTTP) |

Both DRM and ECCP can be active simultaneously — they use independent encryption keys
and produce separate sets of protected tracks.

Subtitle tracks are included in all namespaces since they are not encrypted.

## Session setup

After session establishment, the server announces all configured namespaces.
The client subscribes to the MSF catalog in the desired namespace.
Once it has the catalog, it can subscribe to media tracks listed in that catalog.

The bundled `mlmsub` client connects to a single namespace (default: `cmsf/clear`,
configurable via `-namespace`). It subscribes to the first video and audio track
from the catalog or tracks that match `-videoname`, `-audioname`.
For subtitles, see below.

## Subtitle Tracks

The publisher generates subtitle tracks dynamically, showing UTC timestamp and group number.
Two subtitle formats are supported:

- **WVTT** (WebVTT in CMAF) - codec: `wvtt`
- **STPP** (TTML in CMAF) - codec: `stpp.ttml.im1t`

By default, one Swedish WVTT track (`subs_wvtt_sv`) and one English STPP track (`subs_stpp_en`) are created.
You can configure multiple languages:

```shell
# Multiple languages for both formats
./mlmpub -subswvtt "en,sv,de" -subsstpp "en,fr"

# Only WVTT subtitles
./mlmpub -subswvtt "en,sv" -subsstpp ""

# No subtitles
./mlmpub -subswvtt "" -subsstpp ""
```

Subtitle track names follow the pattern `subs_wvtt_{lang}` and `subs_stpp_{lang}`.

To receive subtitles with the mlmsub subscriber:

```shell
# Subscribe to WVTT subtitles
./mlmsub -subsout subs.mp4 -subsname wvtt

# Subscribe to a specific language
./mlmsub -subsout subs_sv.mp4 -subsname subs_wvtt_sv
```

## Requirements

* Go 1.25 or later

## Installation and Usage

As usual with Go, run

```shell
go mod tidy
```

to get up and running.

There are three commands

* `mlmpub` is the server and publisher
* `mlmsub` is the client and subscriber
* `mlmtest` is an interop test client for the [moq-interop-runner][interop-runner]

The content used is in the `assets/test10s` directory, and was
generated using the tools in `utils/contentgen`.

To run the system, first start the publisher

```shell
cd cmd/mlmpub
go run .
```

You can also build the binary and then run it

```shell
cd cmd/mlmpub
go build .
./mlmpub
```

You can also specify options for the publisher:

```shell
./mlmpub -audiobatch 4 -videobatch 2
```

In another shell, start the subscriber and choose if the video, the audio,
or a muxed combination should be output, e.g.

```shell
cd cmd/mlmsub
go run . -muxout - | ffplay -
```

or build it similarly to `mlmpub` before you run it. This time with some other options

```shell
cd cmd/mlmsub
go build .
./mlmsub -videoname 600 -audioname scale -loglevel debug -muxout - | ffplay -
```

to directly play with ffplay.
There are more options to change the loglevel, choose track etc.

The subscriber will connect to the publisher and start receiving
video and audio frames if some tracks are selected.

### Use with Eyevinn's browser player

The browser player [warp-player][warp-player] has been created to match the
mlmpub publisher. It will subscribe to and read a catalog.
One can then choose video and audio tracks and start playing synchronized
video and audio with configurable latency.

For that to work, one either need certificates or use of the fingerprint mechanism.

#### Using mkcert (recommended for development)

One way to do that is with mkcert:

```sh
> mkcert -key-file key.pem -cert-file cert.pem localhost 127.0.0.1 ::1
> mkcert -install
> go run . -cert cert.pem -key key.pem
```

#### Using certificate fingerprint

For browsers that support WebTransport certificate fingerprints (e.g., Chrome),
you can use self-signed certificates without installing them. This is especially
useful when running the server locally.

**Run mlmpub with fingerprint support**:
```sh
> go run . -sideport 8081
```

This will automatically generate a WebTransport-compatible certificate with:
- ECDSA algorithm (not RSA)
- 14-day validity (WebTransport maximum)
- Self-signed

Alternatively, you can use your own certificate (e.g., generated with the included `generate-webtransport-cert.sh` script):
```sh
cd cmd/mlmpub
./generate-webtransport-cert.sh
go run . -cert cert-fp.pem -key key-fp.pem -sideport 8081
```

This will:
- Start the MoQ server on port 4443 (default address is `0.0.0.0:4443`, listening on all interfaces)
- Start an HTTP side server on port 8081 serving `/fingerprint` and `/clearkey`
- Validate that the certificate meets WebTransport requirements

The warp-player can then connect using:
- Server URL: `https://localhost:4443/moq` or `https://127.0.0.1:4443/moq`
- Fingerprint URL: `http://localhost:8081/fingerprint` or `http://127.0.0.1:8081/fingerprint`

**Notes**:
- The side server is disabled by default (`-sideport 0`).
  Enable it when using certificate fingerprints or ClearKey/ECCP encryption.
- If no certificate files are provided, mlmpub will generate WebTransport-compatible certificates automatically.

### Using DRM

moqlivemock supports two independent content protection modes that can run simultaneously:

#### ClearKey / ECCP (explicit key)

Use `-kid`, `-iv`, and optionally `-cenckey` flags. If no cenc key is provided, the
key-id is used as the key. The ClearKey license endpoint is served at `/clearkey` on
the side server, so `-sideport` must be set. For production behind a reverse proxy,
use `-laurl` to specify the external license URL announced in the catalog.

```sh
# Local development
go run . -kid 39112233445566778899aabbccddeeff -iv 41112233445566778899aabbccddeeff -scheme cbcs -sideport 8081

# Behind a reverse proxy (e.g. Caddy forwarding /clearkey → localhost:8081/clearkey)
go run . -kid 39112233445566778899aabbccddeeff -iv 41112233445566778899aabbccddeeff -scheme cbcs \
         -sideport 8081 -laurl https://moqlivemock.demo.osaas.io/clearkey
```

This announces namespace `cmsf/eccp-cbcs` with tracks like `video_400kbps_avc_eccp`.

#### Commercial DRM (CPIX)

Use `-drmpath` pointing to a config JSON file in the same format as `assets/testdrm/drm_config_test.json`.
Supported systems: Widevine, PlayReady, FairPlay.

```sh
go run . -drmpath ../../assets/testdrm/drm_config_test.json
```

This announces namespace `cmsf/drm-{scheme}` with tracks like `video_400kbps_avc_drm`.

#### Both simultaneously

Both modes can be active at the same time, each with independent encryption keys:

```sh
go run . -drmpath ../../assets/drm/drm_config.json \
         -kid 39112233445566778899aabbccddeeff -iv 41112233445566778899aabbccddeeff -scheme cbcs \
         -sideport 8081 -laurl https://moqlivemock.demo.osaas.io/clearkey
```

This announces three namespaces: `cmsf/clear`, `cmsf/drm-cbcs`, and `cmsf/eccp-cbcs`.

#### Subscriber examples

The subscriber uses information from the catalog to make license requests,
so no extra flags are needed except choosing the right namespace and track names:

```sh
# Clear content (default namespace)
go run . -muxout - | ffplay -

# ECCP-protected content
go run . -namespace cmsf/eccp-cbcs -videoname _eccp -audioname _eccp -muxout - | ffplay -

# DRM-protected content
go run . -namespace cmsf/drm-cbcs -videoname _drm -audioname _drm -muxout - | ffplay -
```

### LOCMAF
This repository implements Low Overhead CMAF (LOCMAF), a LOC-inspirded variant of CMAF which uses MoQT key-value pairs to extract only the required information from CMAF headers in order to create a low overhead. This can be enabled by adding the `-packaging locmaf` flag to `mlmpub`, or you can include both locmaf and uncompressed media in different namespaces by using `-packaging cmaf-and-locmaf`. If this option is used the MSF packaging will be `locmaf` and both the MSF `initData` field and object payloads will use key-value pairs for storing CMAF headers. The first moof header in a group is always sent as a complete locmaf header, but the following moof headers in a group will be sent as delta moof headers which only store the difference between two consecutive moof headers.

## QUIC / WebTransport Configuration

Since `quic-go` v0.59.0 and `webtransport-go` v0.10.0, the QUIC config must enable
`EnableStreamResetPartialDelivery` in addition to `EnableDatagrams`. Without it,
WebTransport connections will fail with `ERR_METHOD_NOT_SUPPORTED` in the browser.

For WebTransport servers, `webtransport.ConfigureHTTP3Server(h3Server)` must also be
called before serving connections. This sets the `ENABLE_WEBTRANSPORT` HTTP/3 setting
that browsers require during the WebTransport handshake.

Example QUIC config:

```go
&quic.Config{
    EnableDatagrams:                  true,
    EnableStreamResetPartialDelivery: true,
}
```

## Development

Use plain Go environment, with go 1.25 or later.
The Makefile helps out with some tasks.

## Contributing

See [CONTRIBUTING](CONTRIBUTING.md)

## License

This project is licensed under the MIT License, see [LICENSE](LICENSE).
Some code is based on [moqtransport][moqtransport which is also licensed under MIT]

# Support

Join our [community on Slack](http://slack.streamingtech.se) where you can post any questions regarding any of our open source projects. Eyevinn's consulting business can also offer you:

- Further development of this component
- Customization and integration of this component into your platform
- Support and maintenance agreement

Contact [sales@eyevinn.se](mailto:sales@eyevinn.se) if you are interested.

# About Eyevinn Technology

[Eyevinn Technology](https://www.eyevinntechnology.se) is an independent consultant firm specialized in video and streaming. Independent in a way that we are not commercially tied to any platform or technology vendor. As our way to innovate and push the industry forward we develop proof-of-concepts and tools. The things we learn and the code we write we share with the industry in [blogs](https://dev.to/video) and by open sourcing the code we have written.

Want to know more about Eyevinn and how it is to work here. Contact us at work@eyevinn.se!

[moqt]: https://datatracker.ietf.org/doc/draft-ietf-moq-transport/
[moqt-14]: https://datatracker.ietf.org/doc/html/draft-ietf-moq-transport-14
[MSF]: https://datatracker.ietf.org/doc/html/draft-ietf-moq-msf-00
[CMSF]: https://datatracker.ietf.org/doc/html/draft-ietf-moq-cmsf-00
[moqtransport]: https://github.com/Eyevinn/moqtransport
[warp-player]: https://github.com/Eyevinn/warp-player
[interop-runner]: https://github.com/englishm/moq-interop-runner
