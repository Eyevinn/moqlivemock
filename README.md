# moqlivemock

![Test](https://github.com/Eyevinn/moqlivemock/workflows/Go/badge.svg)
[![Coverage Status](https://coveralls.io/repos/github/Eyevinn/moqlivemock/badge.svg?branch=master)](https://coveralls.io/github/Eyevinn/moqlivemock?branch=master)
[![GoDoc](https://godoc.org/github.com/Eyevinn/moqlivemock?status.svg)](http://godoc.org/github.com/Eyevinn/moqlivemock)
[![Go Report Card](https://goreportcard.com/badge/github.com/Eyevinn/moqlivemock)](https://goreportcard.com/report/github.com/Eyevinn/moqlivemock)
[![license](https://img.shields.io/github/license/Eyevinn/moqlivemock.svg)](https://github.com/Eyevinn/moqlivemock/blob/master/LICENSE)

moqlivemock is a simple media test service for [MoQ][moq] (Media over QUIC)
and the [WARP][WARP] streaming format by providing a server which
publishes an asset with wall-clock synchronized multi-bitrate video and
two audio tracks, as well as a client that can receive these streams and even multiplex
them for playback with ffplay like `mlmsub -muxout - | ffplay -`.

The input media is 10s of video and audio which is then disassembled
into frames. One or more frames are then combined into a MoQ object as a CMAF chunk.
How many frames are combined is configurable via the `-audiobatch` and `-videobatch` options.

LOC is currently not supported, but one possible scenario is to send LOC over the wire and
then reassamble CMAF on the receiving side again.

This project uses [moqtransport][moqtransport] for the MoQ transport layer.
As the MoQ transport layer is still work in progress, this project is also
work in progress.

## Session setup

The first things that happens after the session establishment is that the namespace is
announced by the server. The client next subscribes to the WARP catalog.
Once it has the catalog, it subscribes to the first video and audio track from the catalog
or tracks that match the `-videoname` and `-audioname` options.

It should later be possible to switch bitrate by unsubscribing to one
track and subscribing to another, with no repeated or lost frames.

## Requirements

* Go 1.23 or later

## Installation and Usage

As usual with Go, run

```shell
go mod tidy
```

to get up and running.

There are two commands

* `mlmpub` is the server and publisher
* `mlmsub` is the client and subscriber

The content used is in the `content` directory, and was
generate using the code in `utils/videogen`.

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

### WARP browser player

The browser player [warp-player][warp-player] has been created to match the
mlmpub publisher. It will subscribe to and read a catalog.
One can then choose video and audio tracks and start playing synchronized
video and audio.

For that to work, one typically need better certificates.

#### Using mkcert (recommended for development)

One way to do that is with mkcert:

```sh
> mkcert -key-file key.pem -cert-file cert.pem localhost 127.0.0.1 ::1
> mkcert -install
> go run . -cert cert.pem -key key.pem -addr localhost:4443
```

#### Using certificate fingerprint

Alternatively, you can use the certificate fingerprint feature for self-signed certificates without installing them in the browser:

```sh
> go run . -cert cert.pem -key key.pem -addr 0.0.0.0:4443 -fingerprintport 8081
```

This will:
- Start the MoQ server on port 4443 (listening on all interfaces)
- Start an HTTP server on port 8081 that serves the certificate's SHA-256 fingerprint

The warp-player can then connect using the fingerprint URL to authenticate the self-signed certificate. Use `-fingerprintport 0` to disable the fingerprint server.


## Development

Use plain Go environment, with go 1.23 or later.
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

[moq]: https://datatracker.ietf.org/doc/draft-ietf-moq-transport/
[WARP]: https://datatracker.ietf.org/doc/html/draft-ietf-moq-warp-00
[moqtransport]: https://github.com/mengelbart/moqtransport
[warp-player]: https://github.com/Eyevinn/warp-player
