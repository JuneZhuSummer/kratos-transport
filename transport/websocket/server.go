package websocket

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/transport"
	ws "github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type MarshalMessageFunc[T any] func(reply T) (message []byte, err error)
type UnmarshalMessageFunc func(messageType int, message []byte) (hbKey string)

func DefaultMarshalFunc[T any](reply T) (message []byte, err error) {
	//默认json解析
	marshal, err := json.Marshal(reply)
	if err != nil {
		return nil, err
	}
	return marshal, nil
}
func DefaultUnmarshalFunc(messageType int, message []byte) (hbKey string) {
	if messageType != ws.TextMessage {
		//client发送的数据默认是text数据
		return ""
	}
	//client默认发送格式为: {"op": "operate"} eg. {"op": "subscribe", "args": [{"channel": "ch1", "foo": "bar"}]}
	msg := string(message)

	params := gjson.Parse(msg)
	op := params.Get("op").String()
	return op
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

	mp *Map //map[messageType]*HB

	sessionMgr *SessionManager
	register   chan *Session
	unregister chan *Session

	marshalFunc   MarshalMessageFunc[any]
	unmarshalFunc UnmarshalMessageFunc

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
		network:       "tcp",
		address:       ":0",
		path:          "/",
		timeout:       1 * time.Second,
		mp:            NewMap(),
		sessionMgr:    NewSessionManager(),
		register:      make(chan *Session, 1),
		unregister:    make(chan *Session, 1),
		marshalFunc:   DefaultMarshalFunc[any],
		unmarshalFunc: DefaultUnmarshalFunc,
		log:           log.NewHelper(log.GetLogger(), log.WithMessageKey("Server")),
	}

	srv.init(opts...)

	srv.err = srv.listen()

	return srv
}

func (s *Server) RegisterMessageHandler(messageType string, handler Handler[any, any], binder Binder) {
	hb := &HB{
		H: handler,
		B: binder,
	}
	s.mp.Set(messageType, hb)
}

func (s *Server) DeregisterMessageHandler(messageType string) {
	delete(s.mp.m, messageType)
}

func RegisterServerMessageHandler[T, Z any](srv *Server, messageType string, handler Handler[*T, *Z]) {
	srv.RegisterMessageHandler(
		messageType,
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
	return s.Shutdown(ctx)
}

func (s *Server) run() {
	for {
		select {
		case client := <-s.register:
			s.sessionMgr.Add(client)
		case client := <-s.unregister:
			s.sessionMgr.Remove(client)
		}
	}
}

func (s *Server) messageHandle(ctx context.Context, sessionId SessionID, messageType int, msg []byte) error {
	hbKey := s.unmarshalFunc(messageType, msg)
	if len(hbKey) == 0 {
		//获取key失败,不再往下走
		s.log.Debugf("messageType:%d, msg:%s", messageType, string(msg))
		return errors.New("parse hbKey fail")
	}

	hb, ok := s.mp.Get(hbKey)
	if !ok {
		return errors.New("unknown msgType")
	}

	req := hb.B()
	_ = json.Unmarshal(msg, &req) //默认使用json解析

	reply, err := hb.H(ctx, sessionId, req)
	if err != nil {
		return err
	}

	session, ok := s.sessionMgr.Get(sessionId)
	if !ok {
		return errors.New("invalid sessionId")
	}

	message, err := s.marshalFunc(reply)
	if err != nil {
		return err
	}

	s.log.Debugf("message:%v", string(message))

	//推送响应数据
	session.pushToChan(message)

	return nil
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
