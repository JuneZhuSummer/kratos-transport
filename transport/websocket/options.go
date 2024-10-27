package websocket

import (
	"crypto/tls"
	"net"
	"time"
)

type ServerOption func(o *Server)

func WithNetwork(network string) ServerOption {
	return func(s *Server) {
		s.network = network
	}
}

func WithAddress(addr string) ServerOption {
	return func(s *Server) {
		s.address = addr
	}
}

func WithTimeout(timeout time.Duration) ServerOption {
	return func(s *Server) {
		s.timeout = timeout
	}
}

func WithPath(path string) ServerOption {
	return func(s *Server) {
		s.path = path
	}
}

func WithConnectHandle(h ConnectHandler) ServerOption {
	return func(s *Server) {
		s.sessionMgr.RegisterConnectHandler(h)
	}
}

func WithTLSConfig(c *tls.Config) ServerOption {
	return func(o *Server) {
		o.tlsConf = c
	}
}

func WithListener(lis net.Listener) ServerOption {
	return func(s *Server) {
		s.lis = lis
	}
}

func WithMarshalFunc(f MarshalMessageFunc[any]) ServerOption {
	return func(s *Server) {
		s.marshalFunc = f
	}
}

func WithUnmarshalFunc(f UnmarshalMessageFunc) ServerOption {
	return func(s *Server) {
		s.unmarshalFunc = f
	}
}

func WithSessionChannelBufferSize(size int) ServerOption {
	return func(_ *Server) {
		channelBufSize = size
	}
}
