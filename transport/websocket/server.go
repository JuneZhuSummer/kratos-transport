package websocket

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-kratos/kratos/v2/encoding"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/transport"
	ws "github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
)

type MarshalMessageFunc[T any] func(reply T) (message []byte, err error)
type UnmarshalMessageFunc func(messageType int, message []byte) (key string)

func DefaultMarshalFunc[T any](reply T) (message []byte, err error) {
	//默认json解析
	return json.Marshal(reply)
}
func DefaultUnmarshalFunc(messageType int, message []byte) (key string) {
	//client发送的数据默认是text数据
	if messageType != ws.TextMessage {
		return ""
	}
	//client默认发送格式为: {"op": "operate"} eg. {"op": "subscribe", "args": [{"channel": "ch1", "foo": "bar"}]}
	msg := string(message)

	params := gjson.Parse(msg)
	key = params.Get("op").String()
	return key
}

var (
	_ transport.Server     = (*Server)(nil)
	_ transport.Endpointer = (*Server)(nil)
)

type Server struct {
	*http.Server

	lis      net.Listener
	tlsConf  *tls.Config
	upgrader *ws.Upgrader

	network string
	address string
	path    string

	timeout time.Duration

	err error

	mp    *Map //map[messageType]*HB
	codec encoding.Codec

	sessionMgr *SessionManager
	register   chan *Session
	unregister chan *Session

	marshalFunc   MarshalMessageFunc[any]
	unmarshalFunc UnmarshalMessageFunc

	writeMessageType int

	autoReply bool

	log *log.Helper
}

func (s *Server) init(opts ...ServerOption) {
	for _, o := range opts {
		o(s)
	}

	s.Server = &http.Server{
		TLSConfig: s.tlsConf,
	}

	http.HandleFunc(s.path, s.handler)
}

func NewServer(opts ...ServerOption) *Server {
	srv := &Server{
		upgrader: &ws.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin:     func(r *http.Request) bool { return true },
		},
		network:          "tcp",
		address:          ":0",
		path:             "/",
		timeout:          1 * time.Second,
		mp:               NewMap(),
		codec:            encoding.GetCodec("json"),
		sessionMgr:       NewSessionManager(),
		register:         make(chan *Session, 1),
		unregister:       make(chan *Session, 1),
		marshalFunc:      DefaultMarshalFunc[any],
		unmarshalFunc:    DefaultUnmarshalFunc,
		writeMessageType: ws.BinaryMessage,
		autoReply:        true,
		log:              log.NewHelper(log.GetLogger(), log.WithMessageKey("Server")),
	}

	srv.init(opts...)

	srv.err = srv.listen()

	return srv
}

func (s *Server) RegisterMessageHandler(mark string, handler Handler[any, any], binder Binder) {
	hb := &HB{
		H: handler,
		B: binder,
	}
	s.mp.Set(mark, hb)
}

func (s *Server) DeregisterMessageHandler(mark string) {
	delete(s.mp.m, mark)
}

func RegisterServerMessageHandler[T, Z any](srv *Server, mark string, handler Handler[*T, *Z]) {
	srv.RegisterMessageHandler(
		mark,
		func(ctx context.Context, sessionId SessionID, req any) (reply any, err error) {
			switch t := req.(type) {
			case *T:
				return handler(ctx, sessionId, t)
			default:
				return nil, errors.New("invalid payload struct type")
			}
		},
		func() any {
			var t T
			return &t
		},
	)
}

func (s *Server) handler(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.log.Errorf("upgrade exception: %v", err)
		return
	}

	session := NewSession(conn, r, s)
	session.server.register <- session
	session.Listen()
}

func (s *Server) Name() string {
	return string(KindWebsocket)
}

func (s *Server) SessionCount() int {
	return s.sessionMgr.Count()
}

func (s *Server) Endpoint() (*url.URL, error) {
	addr := s.address

	prefix := "ws://"
	if s.tlsConf == nil {
		if !strings.HasPrefix(addr, "ws://") {
			prefix = "ws://"
		}
	} else {
		if !strings.HasPrefix(addr, "wss://") {
			prefix = "wss://"
		}
	}
	addr = prefix + addr

	var endpoint *url.URL
	endpoint, s.err = url.Parse(addr)
	return endpoint, nil
}

func (s *Server) listen() error {
	if s.lis == nil {
		lis, err := net.Listen(s.network, s.address)
		if err != nil {
			s.err = err
			return err
		}
		s.lis = lis
	}

	return nil
}

func (s *Server) Start(ctx context.Context) error {
	if s.err != nil {
		return s.err
	}
	s.BaseContext = func(net.Listener) context.Context {
		return ctx
	}
	s.log.Infof("ws server listening on: %s", s.lis.Addr().String())

	go s.run()

	var err error
	if s.tlsConf != nil {
		err = s.ServeTLS(s.lis, "", "")
	} else {
		err = s.Serve(s.lis)
	}
	if !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) Stop(ctx context.Context) error {
	s.log.Info("ws server stopping")
	close(s.register)
	close(s.unregister)
	return s.Shutdown(ctx)
}

func (s *Server) run() {
	for {
		select {
		case client, ok := <-s.register:
			if !ok {
				return
			}
			s.sessionMgr.Add(client)
		case client, ok := <-s.unregister:
			if !ok {
				return
			}
			s.sessionMgr.Remove(client)
		}
	}
}

func (s *Server) unmarshal(inputData []byte, outValue any) error {
	if s.codec != nil {
		if err := s.codec.Unmarshal(inputData, outValue); err != nil {
			return err
		}
	} else if outValue == nil {
		outValue = inputData
	}
	return nil
}

func (s *Server) messageHandle(ctx context.Context, sessionId SessionID, messageType int, msg []byte) error {
	key := s.unmarshalFunc(messageType, msg)
	if len(key) == 0 {
		//获取key失败,不再往下走
		//s.log.Debugf("messageType:%d, msg:%s", messageType, string(msg))
		return errors.New("parse key fail")
	}

	hb, ok := s.mp.Get(key)
	if !ok {
		return errors.New("unknown msgType")
	}

	req := hb.B()
	err := s.unmarshal(msg, &req)
	if err != nil {
		return err
	}

	reply, err := hb.H(ctx, sessionId, req)
	if err != nil {
		return err
	}

	//自动推送响应数据
	if s.autoReply {
		session, ok2 := s.sessionMgr.Get(sessionId)
		if !ok2 {
			return errors.New("invalid sessionId")
		}

		message, err2 := s.marshalFunc(reply)
		if err2 != nil {
			return err2
		}

		//s.log.Debugf("message:%v", string(message))

		session.pushToChan(message)
	}

	return nil
}

func (s *Server) Session(sessionId SessionID) (*Session, bool) {
	return s.sessionMgr.Get(sessionId)
}

// SendMessage 单发
func (s *Server) SendMessage(sessionId SessionID, msg []byte) {
	session, ok := s.sessionMgr.Get(sessionId)
	if !ok {
		s.log.Error("unknown sessionId")
		return
	}
	session.pushToChan(msg)
}

// Broadcast 全部群发
func (s *Server) Broadcast(msg []byte) {
	s.sessionMgr.Range(func(session *Session) {
		session.pushToChan(msg)
	})
}

// BroadcastPart 部分群发
func (s *Server) BroadcastPart(sessionIds []SessionID, msg []byte) {
	for _, sessionId := range sessionIds {
		if session, ok := s.sessionMgr.Get(sessionId); ok {
			session.pushToChan(msg)
		}
	}
}

type Binder func() any

type Handler[T, Z any] func(ctx context.Context, sessionId SessionID, req T) (reply Z, err error)

type HB struct {
	H Handler[any, any]
	B Binder
}

type Map struct {
	m  map[string]*HB
	mu *sync.RWMutex
}

func NewMap() *Map {
	return &Map{
		m:  make(map[string]*HB),
		mu: new(sync.RWMutex),
	}
}

func (m *Map) Get(key string) (*HB, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	v, ok := m.m[key]
	return v, ok
}

func (m *Map) Set(key string, val *HB) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.m[key]; ok {
		return
	}

	m.m[key] = val
}
