package main

import (
	"context"
	"crypto/tls"
	"io"

	"github.com/Eyevinn/moqlivemock/internal/sub"
	"github.com/Eyevinn/moqtransport"
	"github.com/Eyevinn/moqtransport/quicmoq"
	"github.com/Eyevinn/moqtransport/webtransportmoq"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/webtransport-go"
)

func runClientWithDial(
	ctx context.Context, addr string, useWebTransport bool, h *sub.Handler, outs map[string]io.Writer,
) error {
	var conn moqtransport.Connection
	var err error
	if useWebTransport {
		conn, err = dialWebTransport(ctx, addr)
	} else {
		conn, err = dialQUIC(ctx, addr)
	}
	if err != nil {
		return err
	}
	h.Outs = outs
	return h.RunWithConn(ctx, conn)
}

func dialQUIC(ctx context.Context, addr string) (moqtransport.Connection, error) {
	conn, err := quic.DialAddr(ctx, addr, &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"moq-00"},
	}, &quic.Config{
		EnableDatagrams:                  true,
		EnableStreamResetPartialDelivery: true,
	})
	if err != nil {
		return nil, err
	}
	return quicmoq.NewClient(conn), nil
}

func dialWebTransport(ctx context.Context, addr string) (moqtransport.Connection, error) {
	dialer := webtransport.Dialer{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
		QUICConfig: &quic.Config{
			EnableDatagrams:                  true,
			EnableStreamResetPartialDelivery: true,
		},
	}
	_, session, err := dialer.Dial(ctx, addr, nil)
	if err != nil {
		return nil, err
	}
	return webtransportmoq.NewClient(session), nil
}
