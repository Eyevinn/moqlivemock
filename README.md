
# moqlivemock

![Test](https://github.com/Eyevinn/moqlivemock/workflows/Go/badge.svg)
[![Coverage Status](https://coveralls.io/repos/github/Eyevinn/moqlivemock/badge.svg?branch=master)](https://coveralls.io/github/Eyevinn/moqlivemock?branch=master)
[![GoDoc](https://godoc.org/github.com/Eyevinn/moqlivemock?status.svg)](http://godoc.org/github.com/Eyevinn/moqlivemock)
[![Go Report Card](https://goreportcard.com/badge/github.com/Eyevinn/moqlivemock)](https://goreportcard.com/report/github.com/Eyevinn/moqlivemock)
[![license](https://img.shields.io/github/license/Eyevinn/moqlivemock.svg)](https://github.com/Eyevinn/moqlivemock/blob/master/LICENSE)

moqlivemock is a simple media test service for [MoQ][moq] (Media over QUIC)
and the [WARP][WARP] streaming format by providing a server which
publishes an asset with wall-clock synchronized multi-bitrate video and
audio, as well as a client that can receive these streams and even multiplex
them for playback with ffplayh like `mlmsub -muxout - | ffplay -`.

The input media is 10s of video and audio which is then disassembled
into frames. Each frame is sent as a MoQ object as a CMAF chunk,
but it should be easy to combine a few frames into a chunk
to lower the packaging overhead. LOC is currently not supported, but
one possible scenario is to send LOC over the wire and then reassamble
CMAF on the receiving side again.

This project uses [moqtransport][moqtransport] for the MoQ transport layer.
As the MoQ transport layer is still work in progress, this project is also
work in progress.

## Session setup

The first things that happens after the session establishmend
is that the namespace is announced by the server.
The client will subscribe for a WARP catalog.
Once it has the catalog, it will subscribe to the first
video and audio track from the catalog.

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

In another shell, start the subscriber and choose if the video, the audio,
or a muxed combination should be output, e.g. 

```shell
cd cmd/mlmsub
go run . -muxout - | ffplay -
```

to directly play with ffplay.

The subscriber will connect to the publisher and start receiving
video and audio frames.
Note. Currently, there are ways too much logs being written.

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
